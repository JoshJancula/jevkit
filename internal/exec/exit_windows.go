//go:build windows

package exec

import osexec "os/exec"

func processExitCode(cmd *osexec.Cmd) int {
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
