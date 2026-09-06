package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"

	"pi-remote/internal/protocol"
	"pi-remote/internal/transport"
)

type TerminalOptions struct {
	Target   string
	Tool     string
	Command  string
	Timeout  *float64
	Encoding string
	Input    io.Reader
	Output   io.Writer
}

func (c *Client) OpenTerminal(ctx context.Context, options TerminalOptions) (int, error) {
	if options.Tool == "" {
		options.Tool = "bash"
	}
	if options.Input == nil || options.Output == nil {
		return -1, errors.New("terminal input and output are required")
	}
	terminalID, err := newTerminalID()
	if err != nil {
		return -1, err
	}
	requestURL := *c.baseURL
	if requestURL.Scheme == "http" {
		requestURL.Scheme = "ws"
	} else {
		requestURL.Scheme = "wss"
	}
	requestURL.Path = "/v1/terminal"
	requestURL.RawPath = ""
	header, err := transport.ClientAuthHeaders(c.token, http.MethodGet, requestURL.EscapedPath(), nil, time.Now())
	if err != nil {
		return -1, err
	}
	connection, response, err := websocket.DefaultDialer.DialContext(ctx, requestURL.String(), header)
	if err != nil {
		if response != nil {
			return -1, fmt.Errorf("connect to terminal: HTTP %d: %w", response.StatusCode, err)
		}
		return -1, fmt.Errorf("connect to terminal: %w", err)
	}
	defer connection.Close()
	socket := &terminalClientSocket{connection: connection, token: c.token}
	terminalCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-terminalCtx.Done()
		_ = connection.Close()
	}()

	width, height := terminalSize(options.Input)
	input := map[string]interface{}{
		"command": options.Command,
		"pty":     true,
		"width":   width,
		"height":  height,
	}
	if options.Timeout != nil {
		input["timeout"] = *options.Timeout
	}
	if options.Encoding != "" {
		input["encoding"] = options.Encoding
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return -1, fmt.Errorf("encode terminal input: %w", err)
	}
	if err := socket.write(protocol.WireMessage{
		Type: protocol.MessageTerminal,
		ID:   terminalID,
		Terminal: &protocol.WireTerminal{
			Operation:  protocol.TerminalOpen,
			Target:     options.Target,
			Tool:       options.Tool,
			PayloadB64: protocol.EncodePayload(payload),
		},
	}); err != nil {
		return -1, err
	}
	startTerminalResizeForwarder(terminalCtx, socket, terminalID, options.Input)

	inputDone := make(chan error, 1)
	go func() {
		inputDone <- socket.forwardInput(terminalCtx, terminalID, options.Input)
	}()
	for {
		message, err := socket.read()
		if err != nil {
			select {
			case inputErr := <-inputDone:
				if inputErr != nil && !errors.Is(inputErr, context.Canceled) {
					return -1, inputErr
				}
			default:
			}
			if terminalCtx.Err() != nil {
				return -1, terminalCtx.Err()
			}
			return -1, fmt.Errorf("read terminal: %w", err)
		}
		if message.Type != protocol.MessageTerminal || message.ID != terminalID || message.Terminal == nil {
			continue
		}
		switch message.Terminal.Operation {
		case protocol.TerminalOutput:
			data, err := protocol.DecodePayload(message.Terminal.DataB64)
			if err != nil {
				return -1, err
			}
			if _, err := options.Output.Write(data); err != nil {
				return -1, err
			}
		case protocol.TerminalExit:
			if message.Terminal.Error != nil {
				return -1, &RPCError{Code: message.Terminal.Error.Code, Message: message.Terminal.Error.Message, Details: message.Terminal.Error.Details}
			}
			return message.Terminal.ExitCode, nil
		}
	}
}

type terminalClientSocket struct {
	connection *websocket.Conn
	token      string
	writeMu    sync.Mutex
}

func (s *terminalClientSocket) write(message protocol.WireMessage) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.connection.SetWriteDeadline(time.Now().Add(15 * time.Second))
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	envelope, err := transport.EncryptEnvelope(s.token, transport.PurposeTerminalClient, transport.TerminalClientToGatewayAAD, payload)
	if err != nil {
		return err
	}
	return s.connection.WriteJSON(envelope)
}

func (s *terminalClientSocket) read() (protocol.WireMessage, error) {
	var envelope transport.Envelope
	if err := s.connection.ReadJSON(&envelope); err != nil {
		return protocol.WireMessage{}, err
	}
	plaintext, err := transport.DecryptEnvelope(s.token, transport.PurposeTerminalGateway, transport.TerminalGatewayToClientAAD, envelope)
	if err != nil {
		return protocol.WireMessage{}, err
	}
	var message protocol.WireMessage
	if err := json.Unmarshal(plaintext, &message); err != nil {
		return protocol.WireMessage{}, err
	}
	return message, nil
}

func (s *terminalClientSocket) forwardInput(ctx context.Context, id string, input io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		read, err := input.Read(buffer)
		if read > 0 {
			if writeErr := s.write(protocol.WireMessage{
				Type: protocol.MessageTerminal,
				ID:   id,
				Terminal: &protocol.WireTerminal{
					Operation: protocol.TerminalInput,
					DataB64:   protocol.EncodePayload(buffer[:read]),
				},
			}); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func terminalSize(input io.Reader) (int, int) {
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return 80, 24
	}
	width, height, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24
	}
	return width, height
}

func newTerminalID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate terminal id: %w", err)
	}
	return hex.EncodeToString(data), nil
}
