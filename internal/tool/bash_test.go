//go:build !windows

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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
