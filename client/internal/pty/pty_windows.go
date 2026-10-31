//go:build windows

package pty

import (
	"sync"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

type windowsSession struct {
	cpty      *conpty.ConPty
	closeOnce sync.Once
	closeErr  error
}

// Start launches a shell attached to a Windows pseudo console (ConPTY).
func Start(cfg Config) (Session, error) {
	shell := cfg.Shell
	if shell == "" {
		shell = "powershell.exe -NoLogo -NoProfile"
	}
	rows := int(cfg.Rows)
	cols := int(cfg.Cols)
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}
	cpty, err := conpty.Start(shell, conpty.ConPtyDimensions(cols, rows))
	if err != nil {
		return nil, err
	}
	s := &windowsSession{cpty: cpty}
	// ConPTY keeps its output pipe open after the shell exits (for example
	// after `exit`), so Read would block forever and the session would look
	// alive but never return to a prompt. Watch the process through our own
	// handle and close the pseudo console when it ends; Read then fails and
	// the owner can start a new shell.
	if process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cpty.Pid())); err == nil {
		go func() {
			defer windows.CloseHandle(process)
			_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
			_ = s.Close()
		}()
	}
	return s, nil
}

func (s *windowsSession) Read(p []byte) (int, error)  { return s.cpty.Read(p) }
func (s *windowsSession) Write(p []byte) (int, error) { return s.cpty.Write(p) }

// Pid is optional: the server uses it to track one command's descendants
// without adding process ownership to the cross-platform Session contract.
func (s *windowsSession) Pid() int { return s.cpty.Pid() }

// Resize maps the terminal (rows, cols) to ConPTY's (width, height).
func (s *windowsSession) Resize(rows, cols uint16) error {
	return s.cpty.Resize(int(cols), int(rows))
}

// Close is safe to call more than once and from the exit watcher: the
// underlying handles must be closed exactly once.
func (s *windowsSession) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.cpty.Close() })
	return s.closeErr
}
