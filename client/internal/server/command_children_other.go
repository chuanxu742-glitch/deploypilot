//go:build !windows

package server

import "github.com/deploypilot/deploypilot/internal/pty"

func trackCommandChildren(pty.Session) (commandChildren, error) {
	return untrackedCommandChildren{}, nil
}
