//go:build !windows

package security

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func prepareArgvCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		return killArgvGroup(cmd)
	}
}

func runArgvCommand(cmd *exec.Cmd) error {
	err := cmd.Run()
	_ = killArgvGroup(cmd)
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

func killArgvGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
