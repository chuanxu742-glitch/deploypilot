//go:build windows

package tunnel

import (
	"fmt"
	"io"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startOwned starts cmd inside a job object that kills it when the job handle
// closes. The handle is closed by Close, or by Windows when this process dies
// for any reason (crash, Stop-Process, closed console), so a tunnel can never
// outlive DeployPilot and keep exposing the machine.
func startOwned(cmd *exec.Cmd) (io.Closer, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create tunnel job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("configure tunnel job: %w", err)
	}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		windows.CloseHandle(job)
		return nil, fmt.Errorf("attach tunnel to job: %w", err)
	}
	return jobHandle(job), nil
}

type jobHandle windows.Handle

func (j jobHandle) Close() error { return windows.CloseHandle(windows.Handle(j)) }
