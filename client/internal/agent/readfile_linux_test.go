//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadFileRejectsFIFOWithoutWriter(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		name := "direct"
		if symlink {
			name = "symlink"
		}
		t.Run(name, func(t *testing.T) {
			fifo := filepath.Join(t.TempDir(), "service.log")
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			path := fifo
			if symlink {
				path = fifo + ".link"
				if err := os.Symlink(fifo, path); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { _, err := NewShellExecutor().ReadFile(path, 0, 0); done <- err }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "regular file") {
					t.Fatalf("want special-file rejection, got %v", err)
				}
			case <-time.After(time.Second):
				// Release the old implementation's blocked open/read before failing,
				// so the regression test itself never leaks a goroutine.
				fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					syscall.Close(fd)
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("reader did not drain after FIFO release")
				}
				t.Fatal("read_file blocked opening a FIFO without a writer")
			}
		})
	}
}

func TestReadFileRejectsDevice(t *testing.T) {
	if _, err := NewShellExecutor().ReadFile("/dev/null", 0, 0); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("want device rejection, got %v", err)
	}
}

func TestReadFileFollowsRegularSymlink(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "config"), filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("abc\ndef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r, err := NewShellExecutor().ReadFile(link, 4, 4)
	if err != nil || r.Content != "def\n" || !r.EOF {
		t.Fatalf("regular symlink paging changed: %+v, %v", r, err)
	}
}
