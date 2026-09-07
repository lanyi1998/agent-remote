package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pi-remote/internal/protocol"
	"pi-remote/internal/transport"
)

const testToken = "client-test-token"

func TestTargetsUsesSecureProtocol(t *testing.T) {
	httpClient := localHTTPClient(secureHandler(t, func(request *http.Request, plaintext []byte) (int, interface{}) {
		if request.URL.Path != "/v1/targets" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if len(plaintext) != 0 {
			t.Fatalf("GET body = %q", plaintext)
		}
		return http.StatusOK, Targets{Remote: true, RemoteInfo: protocol.Hello{WorkerID: "remote"}}
	}))

	client, err := New(Config{BaseURL: "http://gateway.test", Token: testToken, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := client.Targets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !targets.Remote || targets.RemoteInfo.WorkerID != "remote" {
		t.Fatalf("unexpected targets: %+v", targets)
	}
}

func TestCallReturnsResult(t *testing.T) {
	httpClient := localHTTPClient(secureHandler(t, func(_ *http.Request, plaintext []byte) (int, interface{}) {
		var request protocol.RPCRequest
		if err := json.Unmarshal(plaintext, &request); err != nil {
			t.Fatal(err)
		}
		if request.Target != "worker-1" || request.Tool != "read" || !bytes.Contains(request.Input, []byte("hello.txt")) {
			t.Fatalf("unexpected RPC request: %+v", request)
		}
		return http.StatusOK, protocol.RPCResponse{ID: "request-1", OK: true, Result: json.RawMessage(`{"content":"hello"}`)}
	}))

	client, err := New(Config{BaseURL: "http://gateway.test", Token: testToken, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(context.Background(), "worker-1", "read", map[string]string{"path": "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"content":"hello"}` {
		t.Fatalf("result = %s", result)
	}
}

func TestCallReturnsStructuredRPCError(t *testing.T) {
	httpClient := localHTTPClient(secureHandler(t, func(_ *http.Request, _ []byte) (int, interface{}) {
		return http.StatusUnprocessableEntity, protocol.RPCResponse{
			ID:    "request-1",
			OK:    false,
			Error: &protocol.RPCError{Code: "path_not_found", Message: "missing file", Details: json.RawMessage(`{"path":"missing"}`)},
		}
	}))

	client, err := New(Config{BaseURL: "http://gateway.test", Token: testToken, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Call(context.Background(), "remote", "read", map[string]string{"path": "missing"})
	rpcError, ok := err.(*RPCError)
	if !ok || rpcError.Code != "path_not_found" || rpcError.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected error: %#v", err)
	}
}

func TestNewRejectsURLPath(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://example.test/prefix", Token: testToken}); err == nil {
		t.Fatal("expected URL path error")
	}
}

type responseFactory func(*http.Request, []byte) (int, interface{})

type handlerRoundTripper struct {
	handler http.Handler
}

func (r handlerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	r.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func localHTTPClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: handlerRoundTripper{handler: handler}}
}

func secureHandler(t *testing.T, response responseFactory) http.Handler {
	t.Helper()
	replay := transport.NewReplayCache()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		wireBody, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		auth, err := transport.VerifyRequest(request, testToken, wireBody, replay, time.Now())
		if err != nil {
			t.Errorf("verify request: %v", err)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		plaintext := wireBody
		if len(wireBody) > 0 {
			envelope, err := transport.UnmarshalEnvelope(wireBody)
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err = transport.DecryptEnvelope(testToken, transport.PurposeHTTPClient, httpAAD("request", request.Method, request.URL.EscapedPath(), auth.Nonce), envelope)
			if err != nil {
				t.Fatal(err)
			}
		}
		status, value := response(request, plaintext)
		responseBody, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := transport.EncryptEnvelope(testToken, transport.PurposeHTTPServer, httpAAD("response", request.Method, request.URL.EscapedPath(), auth.Nonce), responseBody)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := transport.MarshalEnvelope(envelope)
		if err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("X-Pi-Remote-Encrypted", "1")
		writer.WriteHeader(status)
		_, _ = writer.Write(encoded)
	})
}
