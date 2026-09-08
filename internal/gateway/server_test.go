package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	remoteclient "agent-remote/internal/client"
	"agent-remote/internal/protocol"
	"agent-remote/internal/tool"
	"agent-remote/internal/transport"
	"agent-remote/internal/worker"
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
	server := New(Config{Token: token, RemoteTools: tools})

	requestValue := protocol.RPCRequest{
		ID:     "request-1",
		Target: "remote",
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
	if response.Header().Get("X-Agent-Remote-Encrypted") != "1" {
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

func TestSecureTerminalRunsRemotePTY(t *testing.T) {
	tools, err := tool.NewService(tool.ServiceConfig{Root: t.TempDir(), BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	const token = "a-strong-random-token-for-terminal"
	server := New(Config{Token: token, RemoteTools: tools})
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	client, err := remoteclient.New(remoteclient.Config{BaseURL: httpServer.URL, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	exitCode, err := client.OpenTerminal(context.Background(), remoteclient.TerminalOptions{
		Target:  "remote",
		Tool:    "bash",
		Command: "printf 'value: '; read value; printf 'received=%s\\n' \"$value\"",
		Input:   strings.NewReader("hello\n"),
		Output:  &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code = %d, output = %q", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "received=hello") {
		t.Fatalf("terminal output = %q", output.String())
	}
}

func TestSecureTerminalRunsWorkerPTY(t *testing.T) {
	gatewayTools, err := tool.NewService(tool.ServiceConfig{Root: t.TempDir(), BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	workerTools, err := tool.NewService(tool.ServiceConfig{Root: t.TempDir(), BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	const token = "a-strong-random-token-for-worker-terminal"
	server := New(Config{Token: token, RemoteTools: gatewayTools})
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	workerClient := worker.New(worker.Config{
		ServerURL:    httpServer.URL,
		Token:        token,
		WorkerID:     "terminal-worker",
		Hostname:     "terminal-worker",
		ShellProfile: "explicit-bash",
		Tools:        workerTools,
	})
	workerContext, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- workerClient.Run(workerContext) }()
	defer func() {
		cancelWorker()
		if workerErr := <-workerDone; workerErr != nil && !errors.Is(workerErr, context.Canceled) {
			t.Errorf("worker shutdown: %v", workerErr)
		}
	}()

	client, err := remoteclient.New(remoteclient.Config{BaseURL: httpServer.URL, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		targets, targetsErr := client.Targets(context.Background())
		if targetsErr == nil && len(targets.Workers) == 1 && targets.Workers[0].WorkerID == "terminal-worker" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not connect: %v", targetsErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var output bytes.Buffer
	exitCode, err := client.OpenTerminal(context.Background(), remoteclient.TerminalOptions{
		Target:  "terminal-worker",
		Tool:    "bash",
		Command: "printf 'value: '; read value; printf 'received=%s\\n' \"$value\"",
		Input:   strings.NewReader("hello\n"),
		Output:  &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 || !strings.Contains(output.String(), "received=hello") {
		t.Fatalf("exit code = %d, output = %q", exitCode, output.String())
	}
}
