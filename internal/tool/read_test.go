package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteReadUsesRequestedLineRange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "records.csv"), []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Root: root, MaxReadBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	result := executeReadForTest(t, service, `{"path":"records.csv","offset":2,"limit":2}`)
	if result.Content != "two\nthree\n\n[Showing lines 2-3 of 5. Use offset=4 to continue.]" {
		t.Fatalf("content = %q", result.Content)
	}
	if result.StartLine != 2 || result.EndLine != 3 || result.TotalLines != 5 || result.NextOffset != 4 || !result.Truncated {
		t.Fatalf("unexpected range result: %+v", result)
	}
}

func TestReadLineWithinBytesBoundsLongLineMemory(t *testing.T) {
	reader := strings.NewReader(strings.Repeat("x", 1024*1024) + "\nnext\n")
	line, ended, reachedEOF, err := readLineWithinBytes(bufio.NewReader(reader), 64)
	if err != nil || !ended || reachedEOF {
		t.Fatalf("unexpected line read: ended=%t eof=%t err=%v", ended, reachedEOF, err)
	}
	if line.totalBytes != 1024*1024 || len(line.prefix) != 64 {
		t.Fatalf("line was not bounded: bytes=%d prefix=%d", line.totalBytes, len(line.prefix))
	}
}

func TestExecuteReadBoundsBinaryResponse(t *testing.T) {
	root := t.TempDir()
	content := append([]byte{0}, bytes.Repeat([]byte("x"), 1024*1024)...)
	if err := os.WriteFile(filepath.Join(root, "large.bin"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Root: root, MaxReadBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	result := executeReadForTest(t, service, `{"path":"large.bin"}`)
	decoded, err := base64.StdEncoding.DecodeString(result.ContentBase64)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) > 48 || !result.Truncated || result.Encoding != "base64" {
		t.Fatalf("binary result was not bounded: decoded=%d result=%+v", len(decoded), result)
	}
}

func executeReadForTest(t *testing.T, service *Service, input string) readResult {
	t.Helper()
	raw, err := service.Execute(context.Background(), "read", json.RawMessage(input), nil)
	if err != nil {
		t.Fatal(err)
	}
	var result readResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
