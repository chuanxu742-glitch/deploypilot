package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CommandLog writes unbounded command output directly to a private file.
type CommandLog struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	err    error
	closed bool
}

// NewCommandLog creates a private log under ~/.deploypilot/logs.
func NewCommandLog() (*CommandLog, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory for command log: %w", err)
	}
	dir := filepath.Join(home, ".deploypilot", "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create command log directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("secure command log directory %s: %w", dir, err)
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("create unique command log name: %w", err)
	}
	name := "command-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random) + ".log"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("create command log %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("resolve command log path: %w", err)
	}
	return &CommandLog{file: f, path: abs}, nil
}

// Path returns the absolute path of the log file.
func (l *CommandLog) Path() string { return l.path }

// Write persists output without retaining it in memory.
func (l *CommandLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, os.ErrClosed
	}
	if l.err != nil {
		return 0, l.err
	}
	n, err := l.file.Write(p)
	if err != nil {
		l.err = err
	}
	return n, err
}

// Close flushes and closes the log. It is safe to call more than once.
func (l *CommandLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return l.err
	}
	l.closed = true
	syncErr := l.file.Sync()
	closeErr := l.file.Close()
	l.err = errors.Join(l.err, syncErr, closeErr)
	return l.err
}

// CommandLogReference formats the path returned with truncated command output.
func CommandLogReference(path string) string {
	return "[full output: " + path + "]"
}
