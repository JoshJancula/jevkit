//go:build windows

package worker

import (
	"errors"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepareRuntimeCommand(cmd *exec.Cmd) { cmd.WaitDelay = 100 * time.Millisecond }

func runRuntimeCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(job)
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return err
	}
	attached := make(chan struct{})
	assigned := false
	cmd.Cancel = func() error {
		<-attached
		if assigned {
			return windows.TerminateJobObject(job, 1)
		}
		return cmd.Process.Kill()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	assigned = err == nil
	close(attached)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}
