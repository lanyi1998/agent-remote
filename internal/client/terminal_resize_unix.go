//go:build !windows

package client

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"agent-remote/internal/protocol"
)

func startTerminalResizeForwarder(ctx context.Context, socket *terminalClientSocket, id string, input io.Reader) {
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}

	resizeSignals := make(chan os.Signal, 1)
	signal.Notify(resizeSignals, syscall.SIGWINCH)
	go forwardTerminalResizes(ctx, socket, id, file, resizeSignals)
}

func forwardTerminalResizes(ctx context.Context, socket *terminalClientSocket, id string, input io.Reader, resizeSignals chan os.Signal) {
	defer signal.Stop(resizeSignals)
	for {
		select {
		case <-ctx.Done():
			return
		case <-resizeSignals:
			width, height := terminalSize(input)
			if err := socket.write(protocol.WireMessage{
				Type: protocol.MessageTerminal,
				ID:   id,
				Terminal: &protocol.WireTerminal{
					Operation: protocol.TerminalResize,
					Width:     width,
					Height:    height,
				},
			}); err != nil {
				return
			}
		}
	}
}
