//go:build !linux && !windows

package agent

import (
	"os/exec"
	"time"
)

// runCommand bounds pipe cleanup on unsupported development platforms.
func runCommand(cmd *exec.Cmd) error {
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}
