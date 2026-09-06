//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashUsesWorkspaceAsPWD(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(ServiceConfig{Root: root, BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := service.Execute(context.Background(), "bash", json.RawMessage(`{"command":"printf '%s' \"$PWD\"","timeout":5}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	var result bashResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := filepath.EvalSymlinks(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("PWD = %q, want %q", result.Output, root)
	}
}

func TestBashTimeoutReturnsPartialOutput(t *testing.T) {
	service, err := NewService(ServiceConfig{Root: t.TempDir(), BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Execute(context.Background(), "bash", json.RawMessage(`{"command":"echo before; sleep 2","timeout":0.1}`), nil)
	var toolErr *Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected tool error, got %v", err)
	}
	if toolErr.Code != "timeout" {
		t.Fatalf("error code = %q", toolErr.Code)
	}
	details, ok := toolErr.Details.(bashResult)
	if !ok || details.Output != "before\n" {
		t.Fatalf("unexpected timeout details: %#v", toolErr.Details)
	}
}

func TestBashPTYReadsInteractiveInput(t *testing.T) {
	service, err := NewService(ServiceConfig{Root: t.TempDir(), BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := service.ExecuteWithInput(
		context.Background(),
		"bash",
		json.RawMessage(`{"command":"printf 'value: '; read value; printf 'received=%s\\n' \"$value\"","pty":true,"timeout":5}`),
		strings.NewReader("hello\n"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var result bashResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "received=hello") {
		t.Fatalf("PTY output = %q", result.Output)
	}
}

func TestBashDecodesConfiguredEncoding(t *testing.T) {
	service, err := NewService(ServiceConfig{Root: t.TempDir(), BashPath: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := service.Execute(
		context.Background(),
		"bash",
		json.RawMessage(`{"command":"printf '\\304\\343\\272\\303'","encoding":"gbk","timeout":5}`),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var result bashResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.Output != "你好" {
		t.Fatalf("decoded output = %q", result.Output)
	}
}
