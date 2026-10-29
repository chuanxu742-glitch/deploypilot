//go:build !linux && !windows

package pty

// Start is unsupported on platforms without a PTY/ConPTY implementation.
func Start(_ Config) (Session, error) {
	return nil, ErrUnsupported
}
