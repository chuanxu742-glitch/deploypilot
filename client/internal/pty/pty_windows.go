//go:build windows

package pty

import (
	"github.com/UserExistsError/conpty"
)

type windowsSession struct {
	cpty *conpty.ConPty
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
	return &windowsSession{cpty: cpty}, nil
}

func (s *windowsSession) Read(p []byte) (int, error)  { return s.cpty.Read(p) }
func (s *windowsSession) Write(p []byte) (int, error) { return s.cpty.Write(p) }

// Resize maps the terminal (rows, cols) to ConPTY's (width, height).
func (s *windowsSession) Resize(rows, cols uint16) error {
	return s.cpty.Resize(int(cols), int(rows))
}

func (s *windowsSession) Close() error {
	return s.cpty.Close()
}
