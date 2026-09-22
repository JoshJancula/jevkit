//go:build !windows

package exec

import (
	osexec "os/exec"
	"syscall"
)

func processExitCode(cmd *osexec.Cmd) int {
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}
