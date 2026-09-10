package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadSelectionReturnsLine(t *testing.T) {
	value, err := readSelection(context.Background(), bufio.NewReader(strings.NewReader("2\n")))
	if err != nil {
		t.Fatal(err)
	}
	if value != "2" {
		t.Fatalf("value = %q, want %q", value, "2")
	}
}

func TestReadSelectionCancelsForRawCtrlC(t *testing.T) {
	_, err := readSelection(context.Background(), bufio.NewReader(strings.NewReader("\x03")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestExitCodeForCancellation(t *testing.T) {
	if code := exitCode(context.Canceled, io.Discard); code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
}

func TestReadSelectionCancellationDoesNotWaitForInput(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, err := readSelection(ctx, bufio.NewReader(reader))
		result <- err
	}()
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("selection did not stop after cancellation")
	}
}
