package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// shellCommand returns the shell invocation for the current OS.
func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		// PowerShell writes pipe output in the console code page (e.g. GBK on
		// Chinese Windows); force UTF-8 so non-ASCII text is not garbled.
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding=[Text.Encoding]::UTF8; $OutputEncoding=[Text.Encoding]::UTF8; " + command}
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

// shellExecutor uses the platform shell selected by shellCommand.
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

	var buf cappedBuffer
	buf.limit = maxCapturedOutput
	w := &streamWriter{onOutput: onOutput, sink: &buf}
	cmd.Stdout = w
	cmd.Stderr = w

	err := runCommand(cmd)
	// A successful launcher may leave its output pipe inherited by a service.
	// WaitDelay closes that pipe; it does not turn exit status 0 into a failure.
	detached := errors.Is(err, exec.ErrWaitDelay) && runCtx.Err() == nil && cmd.ProcessState != nil && cmd.ProcessState.Success()
	if detached {
		err = nil
	}
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
	output := buf.String()
	if detached {
		output += "\n[command exited successfully; background output detached; inspect service logs for subsequent output]"
	}
	return exit, output, err
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
	// NUL bytes do not occur in text; returning a binary file would only fill
	// the model's context with garbage.
	if bytes.IndexByte(buf[:min(n, 8192)], 0) >= 0 {
		return ReadResult{}, fmt.Errorf("%s looks like a binary file (%d bytes); inspect it with run_command (file, ls -lh, head -c, xxd | head) instead", path, size)
	}
	// End a partial page on a whole character so the next page (which starts
	// at NextOffset) does not split a multi-byte rune across two reads.
	if offset+int64(n) < size {
		for back := 1; back <= utf8.UTFMax && back <= n; back++ {
			if utf8.RuneStart(buf[n-back]) {
				if !utf8.FullRune(buf[n-back : n]) {
					n -= back
				}
				break
			}
		}
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
	// Edit the real file behind a symlink (e.g. nginx sites-enabled/default):
	// renaming over the link would replace the link with a copy and leave the
	// actual configuration untouched.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return 0, err
	}
	if info.Size() > maxEditFileBytes {
		return 0, fmt.Errorf("file is %d bytes (> %d); edit it with run_command using sed/awk instead", info.Size(), maxEditFileBytes)
	}
	b, err := os.ReadFile(real)
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
	// Atomic replace: write a uniquely named temp file beside the real file,
	// copy its mode and owner, then rename over it.
	f, err := os.CreateTemp(filepath.Dir(real), "."+filepath.Base(real)+".dp-edit-*")
	if err != nil {
		return 0, err
	}
	tmp := f.Name()
	_, err = f.WriteString(content)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp, info.Mode().Perm())
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	// Editing as root must not silently hand a service's file to root.
	preserveOwner(tmp, info)
	if err := os.Rename(tmp, real); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return count, nil
}

type machineProbe struct{ label, cmd string }

// machineProbes returns read-only environment checks for the host OS. Each
// prints a fallback instead of failing so the summary is always readable.
func machineProbes() []machineProbe {
	if runtime.GOOS == "windows" {
		have := func(name, cmd, missing string) string {
			return "if (Get-Command " + name + " -ErrorAction SilentlyContinue) { " + cmd + " } else { '" + missing + "' }"
		}
		return []machineProbe{
			{"OS", "(Get-CimInstance Win32_OperatingSystem | ForEach-Object { $_.Caption + ' ' + $_.Version + ' ' + $_.OSArchitecture })"},
			{"User", "$id=[Security.Principal.WindowsIdentity]::GetCurrent(); $admin=([Security.Principal.WindowsPrincipal]$id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator); $id.Name + '; administrator: ' + $admin"},
			{"CPU/Mem", "$env:NUMBER_OF_PROCESSORS + ' logical CPUs; ' + [math]::Round((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory/1GB,1) + ' GB RAM'"},
			{"Disk", "Get-PSDrive -PSProvider FileSystem | Where-Object { $_.Used -ne $null } | ForEach-Object { $_.Name + ': free ' + [math]::Round($_.Free/1GB,1) + ' GB' }"},
			{"GPU", have("nvidia-smi", "nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader", "no nvidia gpu")},
			{"Python", have("python", "python --version 2>&1", "no python")},
			{"Docker", have("docker", "docker --version", "no docker")},
			{"Network", "try { (Invoke-WebRequest -Uri https://modelscope.cn -Method Head -UseBasicParsing -TimeoutSec 5).StatusCode } catch { 'modelscope.cn unreachable' }"},
		}
	}
	return []machineProbe{
		{"OS", "uname -srm; (. /etc/os-release 2>/dev/null && echo \"$PRETTY_NAME\")"},
		{"User", "echo \"$(id -un) uid=$(id -u)\"; if [ \"$(id -u)\" = 0 ]; then echo 'running as root'; elif sudo -n true 2>/dev/null; then echo 'passwordless sudo: yes'; else echo 'passwordless sudo: no (sudo would prompt; it cannot be answered)'; fi"},
		{"CPU/Mem", "echo \"$(nproc) CPUs\"; free -h 2>/dev/null | head -n2"},
		{"Disk", "df -h / \"$HOME\" 2>/dev/null | tail -n +2 | sort -u"},
		{"GPU", "if command -v nvidia-smi >/dev/null 2>&1; then nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader; else echo 'no nvidia gpu'; fi"},
		{"CUDA", "if command -v nvcc >/dev/null 2>&1; then nvcc --version | tail -n1; else echo 'no nvcc in PATH'; fi"},
		{"Python", "if command -v python3 >/dev/null 2>&1; then python3 --version; python3 -m pip --version 2>/dev/null || echo 'no pip'; else echo 'no python3'; fi"},
		{"Docker", "if command -v docker >/dev/null 2>&1; then docker --version; docker info >/dev/null 2>&1 && echo 'docker daemon reachable' || echo 'docker daemon not reachable by this user'; else echo 'no docker'; fi"},
		{"Init", "if [ -d /run/systemd/system ]; then echo systemd; else echo 'no systemd (use nohup/background processes)'; fi"},
		{"Network", "curl -s -m 5 -o /dev/null -w 'modelscope.cn HTTP %{http_code}' https://modelscope.cn 2>/dev/null || echo 'modelscope.cn unreachable'"},
	}
}

// maxProbeOutput bounds each probe line in the agent's context.
const maxProbeOutput = 600

func (shellExecutor) MachineInfo(ctx context.Context) string {
	probes := machineProbes()
	results := make([]string, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out, err := shellExecutor{}.Run(ctx, p.cmd, 10*time.Second, nil)
			out = strings.TrimSpace(out)
			if out == "" && err != nil {
				out = "unavailable (" + err.Error() + ")"
			}
			if len(out) > maxProbeOutput {
				out = strings.ToValidUTF8(out[:maxProbeOutput], "") + " …"
			}
			results[i] = p.label + ": " + strings.ReplaceAll(out, "\n", "; ")
		}()
	}
	wg.Wait()
	return strings.Join(results, "\n") + "\n"
}

func parentDir(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if i <= 0 {
		return ""
	}
	return path[:i]
}

// cappedBuffer keeps a fixed prefix and a rolling suffix. Install failures
// usually appear at the end; retaining only the prefix hides them from the model.
type cappedBuffer struct {
	buf       bytes.Buffer
	tail      []byte
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if c.limit <= 0 {
		c.truncated = c.truncated || n > 0
		return n, nil
	}
	if !c.truncated {
		if c.buf.Len()+n <= c.limit {
			return c.buf.Write(p)
		}
		// Split the old buffer only when truncation first becomes necessary.
		old := append([]byte(nil), c.buf.Bytes()...)
		c.buf.Reset()
		c.truncated = true
		c.retain(old)
	}
	c.retain(p)
	return n, nil
}

func (c *cappedBuffer) retain(p []byte) {
	headSize := c.limit / 2
	if room := headSize - c.buf.Len(); room > 0 {
		n := min(room, len(p))
		c.buf.Write(p[:n])
		p = p[n:]
	}
	tailSize := c.limit - headSize
	if len(p) >= tailSize {
		c.tail = append(c.tail[:0], p[len(p)-tailSize:]...)
		return
	}
	if excess := len(c.tail) + len(p) - tailSize; excess > 0 {
		copy(c.tail, c.tail[excess:])
		c.tail = c.tail[:len(c.tail)-excess]
	}
	c.tail = append(c.tail, p...)
}

func (c *cappedBuffer) String() string {
	s := c.buf.String()
	if c.truncated {
		// A byte limit can split a multibyte rune at either retained boundary.
		for len(s) > 0 {
			_, size := utf8.DecodeLastRuneInString(s)
			if size != 1 || s[len(s)-1] < utf8.RuneSelf {
				break
			}
			s = s[:len(s)-1]
		}
		tail := c.tail
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
		return strings.ToValidUTF8(s, "?") + "\n...[output truncated; middle omitted]...\n" + strings.ToValidUTF8(string(tail), "?")
	}
	return strings.ToValidUTF8(s, "?")
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
