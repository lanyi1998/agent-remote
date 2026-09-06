//go:build !windows

package tool

import (
	"context"
	"io"
	"os/exec"

	"github.com/creack/pty"
)

func runManagedPTY(ctx context.Context, command *exec.Cmd, input io.Reader, width, height int, output io.Writer) (int, error) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	master, err := pty.StartWithSize(command, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	if err != nil {
		return -1, err
	}
	defer master.Close()

	inputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(master, input)
		close(inputDone)
	}()

	outputDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(output, master)
		outputDone <- copyErr
	}()

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case waitErr := <-wait:
		_ = master.Close()
		<-outputDone
		return commandExitCode(command, waitErr), waitErr
	case <-ctx.Done():
		_ = command.Process.Kill()
		waitErr := <-wait
		_ = master.Close()
		<-outputDone
		return -1, waitErr
	}
}
