//go:build !windows

package agent

import (
	"os"
	"syscall"
)

// Opening a FIFO read-only normally waits for a writer before f.Stat can
// reject it. O_NONBLOCK makes open bounded for pipes, including when a path
// or symlink changes from a regular file to a FIFO immediately before open.
// ReadFile validates the returned descriptor before reading any bytes.
// O_NOCTTY prevents a device path from becoming our controlling terminal.
func openReadFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
