//go:build !windows

package worker

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// Each runtime owns a process group, so context cancellation and normal exit
// both clean up subprocesses the runtime leaves behind.
func prepareRuntimeCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		return killRuntimeGroup(cmd)
	}
}

func runRuntimeCommand(cmd *exec.Cmd) error {
	err := cmd.Run()
	// A runtime can exit while its agents are still running. Clean them up even
	// when the command itself returned successfully.
	_ = killRuntimeGroup(cmd)
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

func killRuntimeGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
