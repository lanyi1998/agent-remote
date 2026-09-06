package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"pi-remote/internal/protocol"
)

type ChunkHandler func([]byte)

type callResult struct {
	response *protocol.WireResponse
	err      error
}

type pendingCall struct {
	result  chan callResult
	onChunk ChunkHandler
}

type TerminalHandler func([]byte)

type terminalResult struct {
	exitCode     int
	err          *protocol.RPCError
	transportErr error
}

type terminalCall struct {
	result   chan terminalResult
	onOutput TerminalHandler
}

type Peer struct {
	Hello     protocol.Hello
	conn      *websocket.Conn
	token     string
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]*pendingCall
	terminals map[string]*terminalCall
	closed    chan struct{}
	closeOnce sync.Once
	onClose   func()
}

func NewPeer(conn *websocket.Conn, hello protocol.Hello, token string, onClose func()) *Peer {
	peer := &Peer{
		Hello:     hello,
		conn:      conn,
		token:     token,
		pending:   make(map[string]*pendingCall),
		terminals: make(map[string]*terminalCall),
		closed:    make(chan struct{}),
		onClose:   onClose,
	}
	conn.SetReadLimit(72 * 1024 * 1024)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	return peer
}

func (p *Peer) Start() {
	go p.readLoop()
	go p.pingLoop()
}

func (p *Peer) Call(ctx context.Context, id, toolName string, input json.RawMessage, onChunk ChunkHandler) (json.RawMessage, *protocol.RPCError, error) {
	pending := &pendingCall{result: make(chan callResult, 1), onChunk: onChunk}
	if err := p.addPending(id, pending); err != nil {
		return nil, nil, err
	}
	defer p.removePending(id)
	request := protocol.WireMessage{
		Type: protocol.MessageRequest,
		ID:   id,
		Request: &protocol.WireRequest{
			Tool:       toolName,
			PayloadB64: protocol.EncodePayload(input),
		},
	}
	if err := p.writeJSON(request); err != nil {
		return nil, nil, err
	}
	select {
	case result := <-pending.result:
		if result.err != nil {
			return nil, nil, result.err
		}
		if result.response.Error != nil {
			return nil, result.response.Error, nil
		}
		decoded, err := protocol.DecodePayload(result.response.ResultB64)
		if err != nil {
			return nil, nil, err
		}
		return json.RawMessage(decoded), nil, nil
	case <-ctx.Done():
		_ = p.writeJSON(protocol.WireMessage{Type: protocol.MessageCancel, ID: id})
		return nil, nil, ctx.Err()
	case <-p.closed:
		return nil, nil, errors.New("worker connection closed")
	}
}

func (p *Peer) OpenTerminal(ctx context.Context, id, toolName string, input json.RawMessage, onOutput TerminalHandler, ready chan<- error) (int, error) {
	call := &terminalCall{result: make(chan terminalResult, 1), onOutput: onOutput}
	if err := p.addTerminal(id, call); err != nil {
		signalTerminalReady(ready, err)
		return -1, err
	}
	defer p.removeTerminal(id)
	if err := p.writeJSON(protocol.WireMessage{
		Type: protocol.MessageTerminal,
		ID:   id,
		Terminal: &protocol.WireTerminal{
			Operation:  protocol.TerminalOpen,
			Tool:       toolName,
			PayloadB64: protocol.EncodePayload(input),
		},
	}); err != nil {
		signalTerminalReady(ready, err)
		return -1, err
	}
	signalTerminalReady(ready, nil)
	select {
	case result := <-call.result:
		if result.transportErr != nil {
			return -1, result.transportErr
		}
		if result.err != nil {
			return -1, fmt.Errorf("terminal failed: %s", result.err.Message)
		}
		return result.exitCode, nil
	case <-ctx.Done():
		_ = p.CloseTerminal(id)
		return -1, ctx.Err()
	case <-p.closed:
		return -1, errors.New("worker connection closed")
	}
}

func signalTerminalReady(ready chan<- error, err error) {
	if ready != nil {
		ready <- err
	}
}

func (p *Peer) SendTerminalInput(id string, data []byte) error {
	if !p.hasTerminal(id) {
		return errors.New("terminal session is not running")
	}
	return p.writeJSON(protocol.WireMessage{
		Type: protocol.MessageTerminal,
		ID:   id,
		Terminal: &protocol.WireTerminal{
			Operation: protocol.TerminalInput,
			DataB64:   protocol.EncodePayload(data),
		},
	})
}

func (p *Peer) ResizeTerminal(id string, width, height int) error {
	if !p.hasTerminal(id) {
		return errors.New("terminal session is not running")
	}
	return p.writeJSON(protocol.WireMessage{
		Type: protocol.MessageTerminal,
		ID:   id,
		Terminal: &protocol.WireTerminal{
			Operation: protocol.TerminalResize,
			Width:     width,
			Height:    height,
		},
	})
}

func (p *Peer) CloseTerminal(id string) error {
	if !p.hasTerminal(id) {
		return nil
	}
	return p.writeJSON(protocol.WireMessage{
		Type:     protocol.MessageTerminal,
		ID:       id,
		Terminal: &protocol.WireTerminal{Operation: protocol.TerminalClose},
	})
}

func (p *Peer) Close() {
	p.closeWithError(errors.New("worker connection closed"))
}

func (p *Peer) addPending(id string, call *pendingCall) error {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	if _, exists := p.pending[id]; exists {
		return fmt.Errorf("duplicate request id %q", id)
	}
	p.pending[id] = call
	return nil
}

func (p *Peer) removePending(id string) {
	p.pendingMu.Lock()
	delete(p.pending, id)
	p.pendingMu.Unlock()
}

func (p *Peer) addTerminal(id string, call *terminalCall) error {
	p.pendingMu.Lock()
	defer p.pendingMu.Unlock()
	if _, exists := p.terminals[id]; exists {
		return fmt.Errorf("duplicate terminal id %q", id)
	}
	p.terminals[id] = call
	return nil
}

func (p *Peer) removeTerminal(id string) {
	p.pendingMu.Lock()
	delete(p.terminals, id)
	p.pendingMu.Unlock()
}

func (p *Peer) hasTerminal(id string) bool {
	p.pendingMu.Lock()
	_, exists := p.terminals[id]
	p.pendingMu.Unlock()
	return exists
}

func (p *Peer) readLoop() {
	for {
		var envelope Envelope
		if err := p.conn.ReadJSON(&envelope); err != nil {
			p.closeWithError(err)
			return
		}
		plaintext, err := DecryptEnvelope(p.token, PurposeWorkerToGateway, WorkerToGatewayAAD, envelope)
		if err != nil {
			p.closeWithError(err)
			return
		}
		var message protocol.WireMessage
		if err := json.Unmarshal(plaintext, &message); err != nil {
			p.closeWithError(err)
			return
		}
		p.dispatch(message)
	}
}

func (p *Peer) dispatch(message protocol.WireMessage) {
	p.pendingMu.Lock()
	call := p.pending[message.ID]
	terminal := p.terminals[message.ID]
	p.pendingMu.Unlock()
	if message.Type == protocol.MessageTerminal {
		p.dispatchTerminal(terminal, message.Terminal)
		return
	}
	if call == nil {
		return
	}
	switch message.Type {
	case protocol.MessageChunk:
		if call.onChunk == nil || message.Chunk == nil {
			return
		}
		data, err := protocol.DecodePayload(message.Chunk.DataB64)
		if err == nil {
			call.onChunk(data)
		}
	case protocol.MessageResponse:
		if message.Result != nil {
			select {
			case call.result <- callResult{response: message.Result}:
			default:
			}
		}
	}
}

func (p *Peer) dispatchTerminal(call *terminalCall, message *protocol.WireTerminal) {
	if call == nil || message == nil {
		return
	}
	switch message.Operation {
	case protocol.TerminalOutput:
		data, err := protocol.DecodePayload(message.DataB64)
		if err == nil && call.onOutput != nil {
			call.onOutput(data)
		}
	case protocol.TerminalExit:
		select {
		case call.result <- terminalResult{exitCode: message.ExitCode, err: message.Error}:
		default:
		}
	}
}

func (p *Peer) pingLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.writeMu.Lock()
			err := p.conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(5*time.Second))
			p.writeMu.Unlock()
			if err != nil {
				p.closeWithError(err)
				return
			}
		case <-p.closed:
			return
		}
	}
}

func (p *Peer) writeJSON(message protocol.WireMessage) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	envelope, err := EncryptEnvelope(p.token, PurposeGatewayToWorker, GatewayToWorkerAAD, payload)
	if err != nil {
		return err
	}
	return p.conn.WriteJSON(envelope)
}

func (p *Peer) closeWithError(closeErr error) {
	p.closeOnce.Do(func() {
		close(p.closed)
		_ = p.conn.Close()
		p.pendingMu.Lock()
		for _, call := range p.pending {
			select {
			case call.result <- callResult{err: closeErr}:
			default:
			}
		}
		p.pending = make(map[string]*pendingCall)
		for _, terminal := range p.terminals {
			select {
			case terminal.result <- terminalResult{transportErr: closeErr}:
			default:
			}
		}
		p.terminals = make(map[string]*terminalCall)
		p.pendingMu.Unlock()
		if p.onClose != nil {
			p.onClose()
		}
	})
}
