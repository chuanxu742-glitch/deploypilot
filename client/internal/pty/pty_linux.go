//go:build linux

package pty

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

type linuxSession struct {
	cmd  *exec.Cmd
	file *os.File
}

// Start launches an interactive login shell attached to a new PTY.
func Start(cfg Config) (Session, error) {
	shell := cfg.Shell
	if shell == "" {
		shell = detectShell()
	}

	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	rows := cfg.Rows
	cols := cfg.Cols
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}
	return &linuxSession{cmd: cmd, file: f}, nil
}

func detectShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	for _, c := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "/bin/sh"
}

func (s *linuxSession) Read(p []byte) (int, error)  { return s.file.Read(p) }
func (s *linuxSession) Write(p []byte) (int, error) { return s.file.Write(p) }

func (s *linuxSession) Resize(rows, cols uint16) error {
	return pty.Setsize(s.file, &pty.Winsize{Rows: rows, Cols: cols})
}

func (s *linuxSession) Close() error {
	// Closing the PTY sends EOF/HUP to the shell; then reap the process.
	err := s.file.Close()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	return err
}
