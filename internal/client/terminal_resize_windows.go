//go:build windows

package client

import (
	"context"
	"io"
)

func startTerminalResizeForwarder(_ context.Context, _ *terminalClientSocket, _ string, _ io.Reader) {
}
