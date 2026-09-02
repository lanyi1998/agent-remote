package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"pi-remote/internal/protocol"
	"pi-remote/internal/tool"
	"pi-remote/internal/transport"
)

type Config struct {
	ServerURL    string
	Token        string
	WorkerID     string
	Hostname     string
	ShellProfile string
	Tools        *tool.Service
}

type Client struct {
	config Config
}

func New(config Config) *Client {
	return &Client{config: config}
}

func (c *Client) Run(ctx context.Context) error {
	serverURL, err := NormalizeServerURL(c.config.ServerURL)
	if err != nil {
		return err
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := c.runSession(ctx, serverURL)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("worker connection ended: %v; reconnecting in %s", err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) runSession(ctx context.Context, serverURL string) error {
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Errorf("parse gateway URL: %w", err)
	}
	header, err := transport.ClientAuthHeaders(c.config.Token, http.MethodGet, parsedURL.EscapedPath(), nil, time.Now())
	if err != nil {
		return err
	}
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, serverURL, header)
	if err != nil {
		if response != nil {
			return fmt.Errorf("connect to gateway: HTTP %d: %w", response.StatusCode, err)
		}
		return fmt.Errorf("connect to gateway: %w", err)
	}
	session := newSession(connection, c.config.Token, c.config.Tools)
	defer session.close()
	go func() {
		select {
		case <-ctx.Done():
			session.close()
		case <-session.closed:
		}
	}()
	hello := protocol.WireMessage{
		Type: protocol.MessageHello,
		Hello: &protocol.Hello{
			ProtocolVersion: protocol.Version,
			WorkerID:        c.config.WorkerID,
			OS:              runtime.GOOS,
			Arch:            runtime.GOARCH,
			Hostname:        c.config.Hostname,
			Root:            c.config.Tools.Root(),
			Tools:           []string{"read", "bash", "edit", "write", "find"},
			ShellProfile:    c.config.ShellProfile,
		},
	}
	if err := session.writeJSON(hello); err != nil {
		return err
	}
	log.Printf("worker connected to %s as %s", serverURL, c.config.WorkerID)
	return session.readLoop(ctx)
}

func NormalizeServerURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("server URL scheme must be http, https, ws, or wss")
	}
	if parsed.Host == "" {
		return "", errors.New("server URL must include a host")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1/workers/connect"
	}
	return parsed.String(), nil
}

type session struct {
	connection *websocket.Conn
	token      string
	tools      *tool.Service
	writeMu    sync.Mutex
	cancelMu   sync.Mutex
	cancels    map[string]context.CancelFunc
	closed     chan struct{}
	closeOnce  sync.Once
}

func newSession(connection *websocket.Conn, token string, tools *tool.Service) *session {
	connection.SetReadLimit(72 * 1024 * 1024)
	return &session{
		connection: connection,
		token:      token,
		tools:      tools,
		cancels:    make(map[string]context.CancelFunc),
		closed:     make(chan struct{}),
	}
}

func (s *session) readLoop(ctx context.Context) error {
	for {
		var envelope transport.Envelope
		if err := s.connection.ReadJSON(&envelope); err != nil {
			return err
		}
		plaintext, err := transport.DecryptEnvelope(s.token, transport.PurposeGatewayToWorker, transport.GatewayToWorkerAAD, envelope)
		if err != nil {
			return err
		}
		var message protocol.WireMessage
		if err := json.Unmarshal(plaintext, &message); err != nil {
			return err
		}
		switch message.Type {
		case protocol.MessageRequest:
			if message.Request != nil && message.ID != "" {
				go s.execute(ctx, message.ID, *message.Request)
			}
		case protocol.MessageCancel:
			s.cancel(message.ID)
		}
	}
}

func (s *session) execute(parent context.Context, id string, request protocol.WireRequest) {
	input, err := protocol.DecodePayload(request.PayloadB64)
	if err != nil {
		s.sendError(id, &protocol.RPCError{Code: "invalid_payload", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithCancel(parent)
	if !s.registerCancel(id, cancel) {
		cancel()
		s.sendError(id, &protocol.RPCError{Code: "duplicate_request", Message: "request id is already running"})
		return
	}
	defer func() {
		cancel()
		s.unregisterCancel(id)
	}()
	onChunk := func(data []byte) {
		_ = s.writeJSON(protocol.WireMessage{
			Type:  protocol.MessageChunk,
			ID:    id,
			Chunk: &protocol.WireChunk{DataB64: protocol.EncodePayload(data)},
		})
	}
	result, executeErr := s.tools.Execute(ctx, request.Tool, json.RawMessage(input), onChunk)
	if executeErr != nil {
		s.sendError(id, toProtocolError(executeErr))
		return
	}
	_ = s.writeJSON(protocol.WireMessage{
		Type: protocol.MessageResponse,
		ID:   id,
		Result: &protocol.WireResponse{
			ResultB64: protocol.EncodePayload(result),
		},
	})
}

func (s *session) sendError(id string, rpcError *protocol.RPCError) {
	_ = s.writeJSON(protocol.WireMessage{
		Type:   protocol.MessageResponse,
		ID:     id,
		Result: &protocol.WireResponse{Error: rpcError},
	})
}

func toProtocolError(err error) *protocol.RPCError {
	var toolErr *tool.Error
	if errors.As(err, &toolErr) {
		return &protocol.RPCError{
			Code:    toolErr.Code,
			Message: toolErr.Message,
			Details: tool.ErrorDetails(toolErr.Details),
		}
	}
	return &protocol.RPCError{Code: "tool_error", Message: err.Error()}
}

func (s *session) registerCancel(id string, cancel context.CancelFunc) bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if _, exists := s.cancels[id]; exists {
		return false
	}
	s.cancels[id] = cancel
	return true
}

func (s *session) unregisterCancel(id string) {
	s.cancelMu.Lock()
	delete(s.cancels, id)
	s.cancelMu.Unlock()
}

func (s *session) cancel(id string) {
	s.cancelMu.Lock()
	cancel := s.cancels[id]
	s.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *session) writeJSON(message protocol.WireMessage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	envelope, err := transport.EncryptEnvelope(s.token, transport.PurposeWorkerToGateway, transport.WorkerToGatewayAAD, payload)
	if err != nil {
		return err
	}
	return s.connection.WriteJSON(envelope)
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.connection.Close()
		s.cancelMu.Lock()
		for _, cancel := range s.cancels {
			cancel()
		}
		s.cancels = make(map[string]context.CancelFunc)
		s.cancelMu.Unlock()
	})
}

func ShellProfile(bashPath, busyBoxPath string) string {
	if busyBoxPath != "" {
		return "busybox-sh"
	}
	if bashPath != "" {
		return "explicit-bash"
	}
	if runtime.GOOS == "windows" {
		return "busybox-sh"
	}
	return "system-bash"
}

func DefaultWorkerID(hostname string) string {
	value := strings.TrimSpace(hostname)
	if value == "" {
		return "worker"
	}
	return value
}
