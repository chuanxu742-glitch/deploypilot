package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func backupExisting(path string, info os.FileInfo) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	dest, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".bak-"+stamp+"-*")
	if err != nil {
		return "", fmt.Errorf("create backup for %s: %w", path, err)
	}
	backup := dest.Name()
	src, err := os.Open(path)
	if err != nil {
		_ = dest.Close()
		_ = os.Remove(backup)
		return "", fmt.Errorf("read %s for backup: %w", path, err)
	}
	_, copyErr := io.Copy(dest, src)
	srcErr := src.Close()
	if copyErr == nil {
		copyErr = srcErr
	}
	if copyErr == nil {
		copyErr = dest.Chmod(info.Mode().Perm())
	}
	if copyErr == nil {
		copyErr = dest.Sync()
	}
	closeErr := dest.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(backup)
		return "", fmt.Errorf("write backup for %s: %w", path, copyErr)
	}
	return backup, nil
}
