//go:build windows

package tool

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

func runManagedPTY(context.Context, *exec.Cmd, io.Reader, int, int, io.Writer) (int, error) {
	return -1, errors.New("interactive PTY sessions are not supported on Windows")
}
