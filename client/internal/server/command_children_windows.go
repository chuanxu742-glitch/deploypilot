//go:build windows

package server

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/deploypilot/deploypilot/internal/pty"
	"golang.org/x/sys/windows"
)

type trackedChild struct {
	pid       uint32
	process   windows.Handle
	created   windows.Filetime
	protected bool
}

type windowsCommandChildren struct {
	mu        sync.Mutex
	root      uint32
	processes map[uint32]*trackedChild
	cancelled bool
	err       error
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

func trackCommandChildren(session pty.Session) (commandChildren, error) {
	owner, ok := session.(interface{ Pid() int })
	if !ok || owner.Pid() <= 0 {
		return untrackedCommandChildren{}, nil
	}
	root := uint32(owner.Pid())
	process, err := openTrackedChild(root, true)
	if err != nil {
		return nil, fmt.Errorf("inspect terminal process: %w", err)
	}
	tracker := &windowsCommandChildren{
		root: root, processes: map[uint32]*trackedChild{root: process},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if err := tracker.scan(true); err != nil {
		for _, process := range tracker.processes {
			_ = windows.CloseHandle(process.process)
		}
		return nil, fmt.Errorf("inspect existing terminal descendants: %w", err)
	}
	go func() {
		defer close(tracker.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-tracker.stop:
				return
			case <-ticker.C:
				tracker.mu.Lock()
				tracker.rememberError(tracker.scan(false))
				if tracker.cancelled {
					_, err := tracker.terminateNew()
					tracker.rememberError(err)
				}
				tracker.mu.Unlock()
			}
		}
	}()
	return tracker, nil
}

func openTrackedChild(pid uint32, protected bool) (*trackedChild, error) {
	rights := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE)
	if !protected {
		rights |= windows.PROCESS_TERMINATE
	}
	process, err := windows.OpenProcess(rights, false, pid)
	if err != nil {
		return nil, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		_ = windows.CloseHandle(process)
		return nil, err
	}
	return &trackedChild{pid: pid, process: process, created: created, protected: protected}, nil
}

// scan retains handles even after exit, preventing PID reuse from turning an
// old ancestry edge into permission to kill an unrelated process. Existing
// descendants and children subsequently spawned by those services are protected.
// Polling can miss an intermediary that both starts and exits between scans;
// this is cancellation cleanup, not a process-creation sandbox.
func (t *windowsCommandChildren) scan(baseline bool) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := make(map[uint32]uint32)
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	var scanErr error
	for changed := true; changed; {
		changed = false
		for pid, parentPID := range parents {
			if t.processes[pid] != nil {
				continue
			}
			parent := t.processes[parentPID]
			if parent == nil {
				continue
			}
			protected := baseline || (parent.protected && parentPID != t.root)
			process, err := openTrackedChild(pid, protected)
			if err != nil {
				// A process that exited since the snapshot needs no termination.
				if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) && scanErr == nil {
					scanErr = fmt.Errorf("inspect descendant %d: %w", pid, err)
				}
				continue
			}
			if process.created.Nanoseconds() < parent.created.Nanoseconds() {
				_ = windows.CloseHandle(process.process)
				continue // Stale parent PID from before this parent's lifetime.
			}
			t.processes[pid] = process
			changed = true
		}
	}
	return scanErr
}

func (t *windowsCommandChildren) rememberError(err error) {
	if t.err == nil && err != nil {
		t.err = err
	}
}

func (t *windowsCommandChildren) terminateNew() (bool, error) {
	active := false
	var firstErr error
	for _, child := range t.processes {
		if child.protected {
			continue
		}
		status, err := windows.WaitForSingleObject(child.process, 0)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("check descendant %d: %w", child.pid, err)
			}
			active = true
			continue
		}
		if status == windows.WAIT_OBJECT_0 {
			continue
		}
		active = true
		if err := windows.TerminateProcess(child.process, 1); err != nil && firstErr == nil {
			// The handle refers to this exact process even if it exits here.
			if status, _ := windows.WaitForSingleObject(child.process, 0); status != windows.WAIT_OBJECT_0 {
				firstErr = fmt.Errorf("terminate descendant %d: %w", child.pid, err)
			}
		}
	}
	return active, firstErr
}

func (t *windowsCommandChildren) waitNewExited() error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		t.rememberError(t.scan(false))
		active, err := t.terminateNew()
		t.rememberError(err)
		if !active {
			return t.err
		}
		if time.Now().After(deadline) {
			return errors.Join(t.err, errors.New("command descendants did not terminate within 5 seconds"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (t *windowsCommandChildren) Cancel(interrupt func()) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Capture before Ctrl+C can make a short-lived parent disappear.
	t.rememberError(t.scan(false))
	t.cancelled = true
	interrupt()
	return t.waitNewExited()
}

func (t *windowsCommandChildren) Close() error {
	t.stopOnce.Do(func() { close(t.stop) })
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancelled {
		t.rememberError(t.waitNewExited())
	}
	for _, child := range t.processes {
		_ = windows.CloseHandle(child.process)
	}
	return t.err
}
