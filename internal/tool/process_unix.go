//go:build !windows

package tool

import (
	"os/exec"
	"syscall"
)

type processGuard struct {
	processGroup int
}

func startManagedCommand(command *exec.Cmd) (*processGuard, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &processGuard{processGroup: command.Process.Pid}, nil
}

func (g *processGuard) Kill() {
	_ = syscall.Kill(-g.processGroup, syscall.SIGKILL)
}

func (g *processGuard) Close() {}
