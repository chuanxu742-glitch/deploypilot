//go:build windows

package server

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// replaceFile renames over path. On Windows the rename fails while any other
// process (an antivirus scan, backup or search indexer) has the target open
// without delete sharing. Those holds last milliseconds, so retry briefly
// instead of turning one scan into a permanent history failure.
func replaceFile(from, to string) error {
	deadline := time.Now().Add(2 * time.Second)
	delay := 5 * time.Millisecond
	for {
		err := os.Rename(from, to)
		if err == nil || !transientRenameError(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay *= 2; delay > 200*time.Millisecond {
			delay = 200 * time.Millisecond
		}
	}
}

func transientRenameError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
