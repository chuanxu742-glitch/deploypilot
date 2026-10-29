//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// runCommand assigns the suspended shell to a job before any user command can
// spawn children. Cancellation terminates that job. Normal completion leaves
// intentionally launched background services running, so KILL_ON_JOB_CLOSE is
// deliberately not enabled.
func runCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create command job: %w", err)
	}
	defer windows.CloseHandle(job)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_SUSPENDED}
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error {
		jobErr := windows.TerminateJobObject(job, 1)
		// Cancellation can race with job assignment while the shell is suspended.
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(jobErr, killErr, waitJobEmpty(job))
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := assignAndResume(job, uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return fmt.Errorf("prepare command job: %w", err)
	}
	return cmd.Wait()
}

func assignAndResume(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return resumeErr // A newly created suspended process has one initial thread.
	}
	return fmt.Errorf("find suspended command thread: %w", err)
}

func waitJobEmpty(job windows.Handle) error {
	// Layout of JOBOBJECT_BASIC_ACCOUNTING_INFORMATION from winnt.h.
	var accounting struct {
		Times                                 [4]int64
		PageFaults, Total, Active, Terminated uint32
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
		if err != nil {
			return err
		}
		if accounting.Active == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("command job still has %d processes after cancellation", accounting.Active)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
