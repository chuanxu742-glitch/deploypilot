//go:build !linux

package agent

import (
	"os/exec"
	"time"
)

// configureCmd is a no-op beyond a wait delay on non-Linux dev hosts.
func configureCmd(cmd *exec.Cmd) {
	cmd.WaitDelay = 5 * time.Second
}
