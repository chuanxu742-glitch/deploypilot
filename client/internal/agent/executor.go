package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// shellCommand returns the shell invocation for the current OS.
func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", command}
	}
	return "bash", []string{"-lc", command}
}

// maxCapturedOutput bounds how much command output is kept/sent to the model.
const maxCapturedOutput = 16 * 1024

// defaultReadBytes is how much read_file returns when the model does not ask
// for a specific amount; it can request more via maxBytes.
const defaultReadBytes = 16 * 1024

// maxReadBytes is the hard cap on a single read_file response.
const maxReadBytes = 256 * 1024

// maxEditFileBytes caps the size of a file edit_file will load wholesale.
// Larger files should be edited with run_command (sed/awk).
const maxEditFileBytes = 8 * 1024 * 1024

// ReadResult describes a slice of a file plus the whole-file size.
type ReadResult struct {
	Content    string
	Offset     int64
	Size       int64
	NextOffset int64
	EOF        bool
}

// Executor runs the side-effecting operations the agent is allowed to perform.
// It is an interface so tests can substitute a fake.
type Executor interface {
	// Run executes a shell command, streaming output chunks to onOutput, and
	// returns the exit code and the (possibly truncated) combined output.
	Run(ctx context.Context, command string, timeout time.Duration, onOutput func(string)) (exitCode int, output string, err error)
	// ReadFile returns up to maxBytes of a file starting at offset. When
	// maxBytes <= 0 a default is used; it is capped at maxReadBytes.
	ReadFile(path string, offset, maxBytes int64) (ReadResult, error)
	// WriteFile writes content to a file, creating parent dirs.
	WriteFile(path, content string) error
	// AppendFile appends content to a file, creating it if needed.
	AppendFile(path, content string) error
	// EditFile replaces oldStr with newStr. When replaceAll is false exactly
	// one occurrence must exist. Returns the number of replacements.
	EditFile(path, oldStr, newStr string, replaceAll bool) (int, error)
	// MachineInfo returns a short environment summary used as agent context.
	MachineInfo(ctx context.Context) string
}

// shellExecutor is the real executor using bash on the host.
type shellExecutor struct{}

// NewShellExecutor returns the default host executor.
func NewShellExecutor() Executor { return shellExecutor{} }

func (shellExecutor) Run(ctx context.Context, command string, timeout time.Duration, onOutput func(string)) (int, string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := shellCommand(command)
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Env = os.Environ()
	configureCmd(cmd)

	var buf cappedBuffer
	buf.limit = maxCapturedOutput
	w := &streamWriter{onOutput: onOutput, sink: &buf}
	cmd.Stdout = w
	cmd.Stderr = w

	err := cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
			err = nil
		} else {
			exit = -1
		}
	}
	if runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return -1, buf.String(), fmt.Errorf("command timed out after %s", timeout)
	}
	if ctx.Err() != nil {
		return -1, buf.String(), fmt.Errorf("command cancelled: %w", ctx.Err())
	}
	return exit, buf.String(), err
}

func (shellExecutor) ReadFile(path string, offset, maxBytes int64) (ReadResult, error) {
	if maxBytes <= 0 {
		maxBytes = defaultReadBytes
	}
	if maxBytes > maxReadBytes {
		maxBytes = maxReadBytes
	}
	if offset < 0 {
		offset = 0
	}
	f, err := os.Open(path)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ReadResult{}, err
	}
	if info.IsDir() {
		return ReadResult{}, fmt.Errorf("%s is a directory; use run_command (ls) instead", path)
	}
	size := info.Size()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return ReadResult{}, err
		}
	}
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return ReadResult{}, err
	}
	next := offset + int64(n)
	return ReadResult{
		Content:    string(buf[:n]),
		Offset:     offset,
		Size:       size,
		NextOffset: next,
		EOF:        next >= size,
	}, nil
}

func (shellExecutor) WriteFile(path, content string) error {
	if dir := parentDir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (shellExecutor) AppendFile(path, content string) error {
	if dir := parentDir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func (shellExecutor) EditFile(path, oldStr, newStr string, replaceAll bool) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.Size() > maxEditFileBytes {
		return 0, fmt.Errorf("file is %d bytes (> %d); edit it with run_command using sed/awk instead", info.Size(), maxEditFileBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	content := string(b)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return 0, fmt.Errorf("old_string not found in %s", path)
	}
	if !replaceAll && count > 1 {
		return 0, fmt.Errorf("old_string appears %d times in %s; make it unique or set replace_all=true", count, path)
	}
	if replaceAll {
		content = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		content = strings.Replace(content, oldStr, newStr, 1)
		count = 1
	}
	// Atomic replace: write to a temp file in the same dir, then rename.
	tmp := path + ".dp-edit.tmp"
	if err := os.WriteFile(tmp, []byte(content), info.Mode().Perm()); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return count, nil
}

func (shellExecutor) MachineInfo(ctx context.Context) string {
	probes := []struct {
		label string
		cmd   string
	}{
		{"OS", "uname -a; (. /etc/os-release 2>/dev/null && echo $PRETTY_NAME)"},
		{"CPU/Mem", "nproc; free -h 2>/dev/null | head -n2"},
		{"Disk", "df -h / 2>/dev/null | tail -n1"},
		{"GPU", "nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader 2>/dev/null || echo 'no nvidia gpu'"},
		{"CUDA", "nvcc --version 2>/dev/null | tail -n1 || echo 'no nvcc'"},
		{"Python", "python3 --version 2>/dev/null || echo 'no python3'"},
		{"Docker", "docker --version 2>/dev/null || echo 'no docker'"},
		{"Network", "curl -s -m 5 -o /dev/null -w '%{http_code}' https://modelscope.cn 2>/dev/null || echo 'unreachable'"},
	}
	var b strings.Builder
	for _, p := range probes {
		_, out, _ := shellExecutor{}.Run(ctx, p.cmd, 15*time.Second, nil)
		b.WriteString(p.label)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(out))
		b.WriteString("\n")
	}
	return b.String()
}

func parentDir(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if i <= 0 {
		return ""
	}
	return path[:i]
}

// cappedBuffer stores up to limit bytes, discarding the rest.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len() < c.limit {
		room := c.limit - c.buf.Len()
		if room >= len(p) {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.truncated = true
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	s := c.buf.String()
	if c.truncated {
		s += "\n...[output truncated]"
	}
	return s
}

// streamWriter forwards writes to a live callback and a capped sink.
type streamWriter struct {
	onOutput func(string)
	sink     *cappedBuffer
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if w.onOutput != nil {
		w.onOutput(string(p))
	}
	return w.sink.Write(p)
}
