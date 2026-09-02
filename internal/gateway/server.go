package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"pi-remote/internal/protocol"
	"pi-remote/internal/tool"
	"pi-remote/internal/transport"
)

const maxRequestBytes = 64 * 1024 * 1024

type Config struct {
	ListenAddress string
	Token         string
	TLSCert       string
	TLSKey        string
	LocalTools    *tool.Service
	LocalInfo     protocol.Hello
}

type Server struct {
	config     Config
	hub        *transport.Hub
	authReplay *transport.ReplayCache
	http       *http.Server
}

func New(config Config) *Server {
	server := &Server{config: config, hub: transport.NewHub(), authReplay: transport.NewReplayCache()}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.handleHealth)
	mux.HandleFunc("/v1/workers/connect", server.handleWorkerConnect)
	mux.HandleFunc("/v1/targets", server.requireAuth(server.handleTargets))
	mux.HandleFunc("/v1/rpc", server.requireAuth(server.handleRPC))
	server.http = &http.Server{
		Addr:              config.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	return server
}

func (s *Server) ListenAndServe() error {
	if s.config.TLSCert != "" || s.config.TLSKey != "" {
		if s.config.TLSCert == "" || s.config.TLSKey == "" {
			return errors.New("both --tls-cert and --tls-key are required")
		}
		return s.http.ListenAndServeTLS(s.config.TLSCert, s.config.TLSKey)
	}
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.hub.Close()
	return s.http.Shutdown(ctx)
}

func (s *Server) handleHealth(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"ok":               true,
		"protocol_version": protocol.Version,
		"remote_workers":   len(s.hub.List()),
	})
}

func (s *Server) handleTargets(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]interface{}{
		"local":      true,
		"local_info": s.config.LocalInfo,
		"workers":    s.hub.List(),
	})
}

func (s *Server) handleRPC(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeMethodNotAllowed(response, http.MethodPost)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBytes)
	var rpcRequest protocol.RPCRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rpcRequest); err != nil {
		writeRPCError(response, http.StatusBadRequest, rpcRequest.ID, &protocol.RPCError{Code: "invalid_request", Message: err.Error()})
		return
	}
	if rpcRequest.ID == "" {
		rpcRequest.ID = newRequestID()
	}
	if rpcRequest.Target == "" {
		rpcRequest.Target = "local"
	}
	if rpcRequest.Tool == "" || len(rpcRequest.Input) == 0 {
		writeRPCError(response, http.StatusBadRequest, rpcRequest.ID, &protocol.RPCError{Code: "invalid_request", Message: "tool and input are required"})
		return
	}
	result, rpcErr, err := s.execute(request.Context(), rpcRequest)
	if err != nil {
		writeRPCError(response, http.StatusBadGateway, rpcRequest.ID, &protocol.RPCError{Code: "transport_error", Message: err.Error()})
		return
	}
	if rpcErr != nil {
		writeRPCError(response, http.StatusUnprocessableEntity, rpcRequest.ID, rpcErr)
		return
	}
	writeJSON(response, http.StatusOK, protocol.RPCResponse{ID: rpcRequest.ID, OK: true, Result: result})
}

func (s *Server) execute(ctx context.Context, request protocol.RPCRequest) (json.RawMessage, *protocol.RPCError, error) {
	if request.Target == "local" {
		result, err := s.config.LocalTools.Execute(ctx, request.Tool, request.Input, nil)
		if err != nil {
			return nil, protocolError(err), nil
		}
		return result, nil, nil
	}
	peer, ok := s.hub.Get(request.Target)
	if !ok {
		return nil, &protocol.RPCError{Code: "target_offline", Message: fmt.Sprintf("worker %q is not connected", request.Target)}, nil
	}
	return peer.Call(ctx, request.ID, request.Tool, request.Input, nil)
}

func (s *Server) handleWorkerConnect(response http.ResponseWriter, request *http.Request) {
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
	_ = connection.SetReadDeadline(time.Now().Add(15 * time.Second))
	var envelope transport.Envelope
	if err := connection.ReadJSON(&envelope); err != nil {
		connection.Close()
		return
	}
	plaintext, err := transport.DecryptEnvelope(s.config.Token, transport.PurposeWorkerToGateway, transport.WorkerToGatewayAAD, envelope)
	if err != nil {
		connection.Close()
		return
	}
	var message protocol.WireMessage
	if err := json.Unmarshal(plaintext, &message); err != nil {
		connection.Close()
		return
	}
	if message.Type != protocol.MessageHello || message.Hello == nil || !validHello(*message.Hello) {
		connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid hello"), time.Now().Add(time.Second))
		connection.Close()
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	var peer *transport.Peer
	peer = transport.NewPeer(connection, *message.Hello, s.config.Token, func() {
		s.hub.Remove(message.Hello.WorkerID, peer)
		log.Printf("worker disconnected: %s", message.Hello.WorkerID)
	})
	s.hub.Register(peer)
	peer.Start()
	log.Printf("worker connected: %s (%s/%s)", message.Hello.WorkerID, message.Hello.OS, message.Hello.Arch)
}

func validHello(hello protocol.Hello) bool {
	return hello.ProtocolVersion == protocol.Version && strings.TrimSpace(hello.WorkerID) != ""
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		body, err := readRequestBody(response, request)
		if err != nil {
			writeJSON(response, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body is too large"})
			return
		}
		auth, err := transport.VerifyRequest(request, s.config.Token, body, s.authReplay, time.Now())
		if err != nil {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		plaintext, err := decryptRequestBody(s.config.Token, request, auth.Nonce, body)
		if err != nil {
			writeSecureJSON(response, request, auth.Nonce, http.StatusBadRequest, map[string]string{"error": "invalid secure request"}, s.config.Token)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(plaintext))
		buffered := newBufferedResponseWriter()
		next(buffered, request)
		if err := writeSecureResponse(response, request, auth.Nonce, buffered, s.config.Token); err != nil {
			log.Printf("write secure HTTP response: %v", err)
		}
	}
}

func readRequestBody(response http.ResponseWriter, request *http.Request) ([]byte, error) {
	request.Body = http.MaxBytesReader(response, request.Body, maxRequestBytes+4096)
	return io.ReadAll(request.Body)
}

func decryptRequestBody(token string, request *http.Request, requestNonce string, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}
	envelope, err := transport.UnmarshalEnvelope(body)
	if err != nil {
		return nil, err
	}
	return transport.DecryptEnvelope(token, transport.PurposeHTTPClient, httpAAD("request", request.Method, request.URL.EscapedPath(), requestNonce), envelope)
}

func writeSecureJSON(response http.ResponseWriter, request *http.Request, requestNonce string, status int, value interface{}, token string) {
	buffered := newBufferedResponseWriter()
	writeJSON(buffered, status, value)
	if err := writeSecureResponse(response, request, requestNonce, buffered, token); err != nil {
		log.Printf("write secure JSON response: %v", err)
	}
}

func writeSecureResponse(response http.ResponseWriter, request *http.Request, requestNonce string, buffered *bufferedResponseWriter, token string) error {
	envelope, err := transport.EncryptEnvelope(token, transport.PurposeHTTPServer, httpAAD("response", request.Method, request.URL.EscapedPath(), requestNonce), buffered.body.Bytes())
	if err != nil {
		return err
	}
	payload, err := transport.MarshalEnvelope(envelope)
	if err != nil {
		return err
	}
	for key, values := range buffered.header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Content-Type") {
			continue
		}
		for _, value := range values {
			response.Header().Add(key, value)
		}
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("X-Pi-Remote-Encrypted", "1")
	response.WriteHeader(buffered.statusCode())
	_, err = response.Write(payload)
	return err
}

func httpAAD(direction, method, path, requestNonce string) string {
	return "http/v1\n" + direction + "\n" + method + "\n" + path + "\n" + requestNonce
}

type bufferedResponseWriter struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(data)
}

func (w *bufferedResponseWriter) statusCode() int {
	if !w.wroteHeader {
		return http.StatusOK
	}
	return w.status
}

func protocolError(err error) *protocol.RPCError {
	var toolErr *tool.Error
	if errors.As(err, &toolErr) {
		return &protocol.RPCError{Code: toolErr.Code, Message: toolErr.Message, Details: tool.ErrorDetails(toolErr.Details)}
	}
	return &protocol.RPCError{Code: "tool_error", Message: err.Error()}
}

func newRequestID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data)
}

func writeRPCError(response http.ResponseWriter, status int, id string, rpcErr *protocol.RPCError) {
	writeJSON(response, status, protocol.RPCResponse{ID: id, OK: false, Error: rpcErr})
}

func writeMethodNotAllowed(response http.ResponseWriter, allowed string) {
	response.Header().Set("Allow", allowed)
	writeJSON(response, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

func writeJSON(response http.ResponseWriter, status int, value interface{}) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Printf("write HTTP response: %v", err)
	}
}
