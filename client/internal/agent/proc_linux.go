//go:build linux

package agent

import (
	"os/exec"
	"syscall"
	"time"
)

// configureCmd runs the command in its own process group so cancellation
// kills the whole tree (e.g. a model server started by the shell), not just
// the bash process.
func configureCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Don't hang forever if a grandchild keeps stdout open.
	cmd.WaitDelay = 5 * time.Second
}
