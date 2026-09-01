//go:build windows

package tool

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processGuard struct {
	job     windows.Handle
	process *exec.Cmd
}

func startManagedCommand(command *exec.Cmd) (*processGuard, error) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := command.Start(); err != nil {
		return nil, err
	}
	guard := &processGuard{process: command}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return guard, nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		windows.CloseHandle(job)
		return guard, nil
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		windows.CloseHandle(job)
		return guard, nil
	}
	defer windows.CloseHandle(processHandle)
	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		windows.CloseHandle(job)
		return guard, nil
	}
	guard.job = job
	return guard, nil
}

func (g *processGuard) Kill() {
	if g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
		return
	}
	if g.process.Process != nil {
		_ = g.process.Process.Kill()
	}
}

func (g *processGuard) Close() {
	if g.job != 0 {
		windows.CloseHandle(g.job)
		g.job = 0
	}
}
