//go:build !linux && !windows

package tunnel

import (
	"io"
	"os/exec"
)

// startOwned has no kernel-enforced parent binding on other platforms; the
// tunnel is still stopped by Close on a normal shutdown.
func startOwned(cmd *exec.Cmd) (io.Closer, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return nopCloser{}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
