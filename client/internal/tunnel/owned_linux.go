//go:build linux

package tunnel

import (
	"io"
	"os/exec"
	"syscall"
)

// startOwned asks the kernel to SIGKILL cloudflared when DeployPilot dies, so
// a crash or kill -9 cannot leave the tunnel running and exposing the machine.
func startOwned(cmd *exec.Cmd) (io.Closer, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return nopCloser{}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
