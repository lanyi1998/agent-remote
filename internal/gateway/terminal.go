package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"agent-remote/internal/protocol"
	"agent-remote/internal/tool"
	"agent-remote/internal/transport"
)

type terminalSocket struct {
	connection *websocket.Conn
	token      string
	writeMu    sync.Mutex
}

type terminalExecution struct {
	exitCode int
	err      *protocol.RPCError
}

func (s *Server) handleTerminal(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeMethodNotAllowed(response, http.MethodGet)
		return
	}
	if _, err := transport.VerifyRequest(request, s.config.Token, nil, s.authReplay, time.Now()); err != nil {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize:  32 * 1024,
		WriteBufferSize: 32 * 1024,
		CheckOrigin: func(request *http.Request) bool {
			return request.Header.Get("Origin") == ""
		},
	}
	connection, err := upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	socket := &terminalSocket{connection: connection, token: s.config.Token}
	defer connection.Close()
	if err := s.runTerminal(request.Context(), socket); err != nil {
		log.Printf("terminal session ended: %v", err)
		return
	}
}

func (s *Server) runTerminal(parent context.Context, socket *terminalSocket) error {
	open, err := socket.read()
	if err != nil {
		return err
	}
	if open.Type != protocol.MessageTerminal || open.Terminal == nil || open.ID == "" || open.Terminal.Operation != protocol.TerminalOpen {
		return errors.New("invalid terminal open message")
	}
	target := open.Terminal.Target
	if target == "" {
		target = "remote"
	}
	input, err := protocol.DecodePayload(open.Terminal.PayloadB64)
	if err != nil {
		return socket.writeTerminalExit(open.ID, -1, &protocol.RPCError{Code: "invalid_payload", Message: err.Error()})
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	inputReader, inputWriter := io.Pipe()
	defer inputWriter.Close()
	output := func(data []byte) {
		if err := socket.write(protocol.WireMessage{
			Type: protocol.MessageTerminal,
			ID:   open.ID,
			Terminal: &protocol.WireTerminal{
				Operation: protocol.TerminalOutput,
				DataB64:   protocol.EncodePayload(data),
			},
		}); err != nil {
			cancel()
		}
	}
	executionContext, finishExecution := context.WithCancel(ctx)
	ready := make(chan error, 1)
	done := make(chan terminalExecution, 1)
	go func() {
		result := s.executeTerminal(executionContext, target, open.ID, open.Terminal.Tool, json.RawMessage(input), inputReader, output, ready)
		_ = inputWriter.Close()
		finishExecution()
		done <- result
	}()
	select {
	case readyErr := <-ready:
		if readyErr != nil {
			result := <-done
			return socket.writeTerminalExit(open.ID, result.exitCode, result.err)
		}
	case result := <-done:
		return socket.writeTerminalExit(open.ID, result.exitCode, result.err)
	case <-ctx.Done():
		return ctx.Err()
	}

	messages := make(chan protocol.WireMessage, 8)
	readErrors := make(chan error, 1)
	go func() {
		for {
			message, readErr := socket.read()
			if readErr != nil {
				readErrors <- readErr
				return
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case result := <-done:
			return socket.writeTerminalExit(open.ID, result.exitCode, result.err)
		case readErr := <-readErrors:
			cancel()
			return readErr
		case message := <-messages:
			if message.Type != protocol.MessageTerminal || message.ID != open.ID || message.Terminal == nil {
				continue
			}
			switch message.Terminal.Operation {
			case protocol.TerminalInput:
				if executionContext.Err() != nil {
					continue
				}
				if err := s.forwardTerminalInput(executionContext, target, open.ID, message.Terminal.DataB64, inputWriter); err != nil {
					cancel()
					return err
				}
			case protocol.TerminalResize:
				if target != "remote" {
					if peer, ok := s.hub.Get(target); ok {
						_ = peer.ResizeTerminal(open.ID, message.Terminal.Width, message.Terminal.Height)
					}
				}
			case protocol.TerminalClose:
				cancel()
				_ = s.closeTerminal(target, open.ID)
				return nil
			}
		}
	}
}

func (s *Server) executeTerminal(ctx context.Context, target, id, toolName string, input json.RawMessage, inputReader io.Reader, output tool.ChunkWriter, ready chan<- error) terminalExecution {
	if target == "remote" {
		ready <- nil
		result, err := s.config.RemoteTools.ExecuteWithInput(ctx, toolName, input, inputReader, output)
		if err != nil {
			return terminalExecution{exitCode: -1, err: protocolError(err)}
		}
		var info struct {
			ExitCode int `json:"exit_code"`
		}
		if err := json.Unmarshal(result, &info); err != nil {
			return terminalExecution{exitCode: -1, err: &protocol.RPCError{Code: "invalid_result", Message: err.Error()}}
		}
		return terminalExecution{exitCode: info.ExitCode}
	}
	peer, ok := s.hub.Get(target)
	if !ok {
		rpcErr := &protocol.RPCError{Code: "target_offline", Message: fmt.Sprintf("worker %q is not connected", target)}
		ready <- errors.New(rpcErr.Message)
		return terminalExecution{exitCode: -1, err: rpcErr}
	}
	exitCode, err := peer.OpenTerminal(ctx, id, toolName, input, transport.TerminalHandler(output), ready)
	if err != nil {
		return terminalExecution{exitCode: -1, err: &protocol.RPCError{Code: "terminal_error", Message: err.Error()}}
	}
	return terminalExecution{exitCode: exitCode}
}

func (s *Server) forwardTerminalInput(ctx context.Context, target, id, encoded string, inputWriter *io.PipeWriter) error {
	data, err := protocol.DecodePayload(encoded)
	if err != nil {
		return err
	}
	if target == "remote" {
		writeDone := make(chan error, 1)
		go func() {
			_, writeErr := inputWriter.Write(data)
			writeDone <- writeErr
		}()
		select {
		case err = <-writeDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	peer, ok := s.hub.Get(target)
	if !ok {
		return fmt.Errorf("worker %q is not connected", target)
	}
	return peer.SendTerminalInput(id, data)
}

func (s *Server) closeTerminal(target, id string) error {
	if target == "remote" {
		return nil
	}
	peer, ok := s.hub.Get(target)
	if !ok {
		return nil
	}
	return peer.CloseTerminal(id)
}

func (s *terminalSocket) read() (protocol.WireMessage, error) {
	var envelope transport.Envelope
	if err := s.connection.ReadJSON(&envelope); err != nil {
		return protocol.WireMessage{}, err
	}
	plaintext, err := transport.DecryptEnvelope(s.token, transport.PurposeTerminalClient, transport.TerminalClientToGatewayAAD, envelope)
	if err != nil {
		return protocol.WireMessage{}, err
	}
	var message protocol.WireMessage
	if err := json.Unmarshal(plaintext, &message); err != nil {
		return protocol.WireMessage{}, err
	}
	return message, nil
}

func (s *terminalSocket) write(message protocol.WireMessage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	envelope, err := transport.EncryptEnvelope(s.token, transport.PurposeTerminalGateway, transport.TerminalGatewayToClientAAD, payload)
	if err != nil {
		return err
	}
	return s.connection.WriteJSON(envelope)
}

func (s *terminalSocket) writeTerminalExit(id string, exitCode int, rpcErr *protocol.RPCError) error {
	return s.write(protocol.WireMessage{
		Type: protocol.MessageTerminal,
		ID:   id,
		Terminal: &protocol.WireTerminal{
			Operation: protocol.TerminalExit,
			ExitCode:  exitCode,
			Error:     rpcErr,
		},
	})
}
