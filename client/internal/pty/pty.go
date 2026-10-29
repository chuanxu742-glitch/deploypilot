// Package pty starts an interactive shell attached to a pseudo-terminal.
//
// A real implementation is provided on Linux (build tag "linux"); other
// platforms get a stub that returns ErrUnsupported so the rest of the
// program still compiles and runs on a Windows dev host.
package pty

import (
	"errors"
	"io"
)

// ErrUnsupported is returned by Start on platforms without PTY support.
var ErrUnsupported = errors.New("pty is not supported on this platform")

// Session is an interactive shell bound to a pseudo-terminal.
type Session interface {
	io.ReadWriteCloser
	// Resize sets the terminal window size.
	Resize(rows, cols uint16) error
}

// Config controls how the shell session is started.
type Config struct {
	// Shell is the program to run; empty means auto-detect ($SHELL, then bash,
	// then sh).
	Shell string
	// Rows and Cols are the initial window size.
	Rows uint16
	Cols uint16
}
