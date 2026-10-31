package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func replaceWaitLog(t *testing.T, path, content string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	// Equal timestamps and equal/larger lengths must not conceal a new file.
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForLogRecognizesRotatedFileIdentity(t *testing.T) {
	for _, size := range []int{160, 326} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "service.log")
			if err := os.WriteFile(path, []byte(strings.Repeat("x", 160)), 0600); err != nil {
				t.Fatal(err)
			}
			rotated := false
			result, err := waitFor(context.Background(), NewShellExecutor(), waitForArgs{
				Kind: "log", Target: path, Contains: "READY", TimeoutSeconds: 3, IntervalSeconds: 1,
			}, func(_ time.Duration, observation string) {
				if !rotated && observation == "log does not contain requested text yet" {
					rotated = true
					replaceWaitLog(t, path, "READY"+strings.Repeat("n", size-len("READY")))
				}
			})
			if !rotated || err != nil || !strings.Contains(result, `contains "READY"`) {
				t.Fatalf("replacement size=%d was missed: rotated=%v result=%q err=%v", size, rotated, result, err)
			}
		})
	}
}

func TestWaitForLogDoesNotJoinTextAcrossRotatedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("prefixREA"), 0600); err != nil {
		t.Fatal(err)
	}
	rotated := false
	result, err := waitFor(context.Background(), NewShellExecutor(), waitForArgs{
		Kind: "log", Target: path, Contains: "READY", TimeoutSeconds: 2, IntervalSeconds: 1,
	}, func(_ time.Duration, observation string) {
		if !rotated && observation == "log does not contain requested text yet" {
			rotated = true
			replaceWaitLog(t, path, "newfile!!DY")
		}
	})
	if !rotated || !errors.Is(err, context.DeadlineExceeded) || result != "" {
		t.Fatalf("text from two files became false evidence: rotated=%v result=%q err=%v", rotated, result, err)
	}
}

type rotateOnReadExecutor struct {
	Executor
	reads int
	after func()
}

func (e *rotateOnReadExecutor) ReadFile(path string, offset, limit int64) (ReadResult, error) {
	result, err := e.Executor.ReadFile(path, offset, limit)
	e.reads++
	if e.reads == 3 {
		e.after()
	}
	return result, err
}

func TestWaitForLogRecognizesRotationBetweenProbeAndPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 160)), 0600); err != nil {
		t.Fatal(err)
	}
	executor := &rotateOnReadExecutor{Executor: NewShellExecutor(), after: func() {
		replaceWaitLog(t, path, "READY"+strings.Repeat("n", 321))
	}}
	result, err := waitFor(context.Background(), executor, waitForArgs{
		Kind: "log", Target: path, Contains: "READY", TimeoutSeconds: 4, IntervalSeconds: 1,
	})
	if err != nil || !strings.Contains(result, `contains "READY"`) {
		t.Fatalf("replacement between probe and page was missed: reads=%d result=%q err=%v", executor.reads, result, err)
	}
}

func TestWaitForLogPreservesAppendAndTruncation(t *testing.T) {
	for _, appendOnly := range []bool{true, false} {
		t.Run(strconv.FormatBool(appendOnly), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "service.log")
			if err := os.WriteFile(path, []byte(strings.Repeat("x", 160)), 0600); err != nil {
				t.Fatal(err)
			}
			changed := false
			result, err := waitFor(context.Background(), NewShellExecutor(), waitForArgs{
				Kind: "log", Target: path, Contains: "READY", TimeoutSeconds: 3, IntervalSeconds: 1,
			}, func(_ time.Duration, observation string) {
				if changed || observation != "log does not contain requested text yet" {
					return
				}
				changed = true
				flags := os.O_WRONLY | os.O_TRUNC
				if appendOnly {
					flags = os.O_WRONLY | os.O_APPEND
				}
				f, err := os.OpenFile(path, flags, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("READY")
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("change log: write=%v close=%v", err, closeErr)
				}
			})
			if err != nil || !strings.Contains(result, `contains "READY"`) {
				t.Fatalf("same-file change was missed: append=%v result=%q err=%v", appendOnly, result, err)
			}
		})
	}
}

func TestWaitForLogAcceptsExecutorWithoutFileIdentity(t *testing.T) {
	result, err := waitFor(context.Background(), newFakeExec(), waitForArgs{
		Kind: "log", Target: "service.log", Contains: "contents of service.log", TimeoutSeconds: 1,
	})
	if err != nil || !strings.Contains(result, `contains "contents of service.log"`) {
		t.Fatalf("executor without optional metadata broke: result=%q err=%v", result, err)
	}
}
