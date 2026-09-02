package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pi-remote/internal/protocol"
	"pi-remote/internal/tool"
	"pi-remote/internal/transport"
)

func TestSecureRPC(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from gateway\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, err := tool.NewService(tool.ServiceConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	const token = "a-strong-random-token-for-tests-only"
	server := New(Config{Token: token, LocalTools: tools})

	requestValue := protocol.RPCRequest{
		ID:     "request-1",
		Target: "local",
		Tool:   "read",
		Input:  json.RawMessage(`{"path":"hello.txt"}`),
	}
	plaintext, err := json.Marshal(requestValue)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	requestNonce, err := transport.NewRequestNonce()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := transport.EncryptEnvelope(token, transport.PurposeHTTPClient, httpAAD("request", http.MethodPost, "/v1/rpc", requestNonce), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	body, err := transport.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	header, err := transport.ClientAuthHeadersWithNonce(token, http.MethodPost, "/v1/rpc", body, requestNonce, now)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/rpc", bytes.NewReader(body))
	request.Header = header
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if response.Header().Get("X-Pi-Remote-Encrypted") != "1" {
		t.Fatal("response was not marked as encrypted")
	}
	responseEnvelope, err := transport.UnmarshalEnvelope(response.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := transport.DecryptEnvelope(token, transport.PurposeHTTPServer, httpAAD("response", http.MethodPost, "/v1/rpc", requestNonce), responseEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	var rpcResponse protocol.RPCResponse
	if err := json.Unmarshal(responseBody, &rpcResponse); err != nil {
		t.Fatal(err)
	}
	if !rpcResponse.OK || !bytes.Contains(rpcResponse.Result, []byte("hello from gateway")) {
		t.Fatalf("unexpected RPC response: %s", responseBody)
	}
}
