package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// urlRe matches the public quick-tunnel address cloudflared prints.
var urlRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Tunnel represents a running cloudflared quick tunnel.
type Tunnel struct {
	URL string

	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	owner  io.Closer
	mu     sync.Mutex
	err    error
}

// Start launches cloudflared to expose http://127.0.0.1:<localPort> and
// waits for both a URL and cloudflared's first successful edge connection.
// This does not guarantee that public DNS/routing has propagated everywhere.
func Start(ctx context.Context, binPath string, localPort int) (*Tunnel, error) {
	dir, err := os.MkdirTemp("", "deploypilot-tunnel-*")
	if err != nil {
		return nil, err
	}
	pidfile := filepath.Join(dir, "connected.pid")
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, binPath,
		"tunnel",
		"--no-autoupdate",
		"--metrics", "127.0.0.1:0",
		"--pidfile", pidfile,
		"--loglevel", "info",
		"--url", fmt.Sprintf("http://127.0.0.1:%d", localPort),
	)

	// The same writer lets os/exec drain both streams and join its copy goroutines
	// before Wait returns. Reading stderr then stdout can deadlock the child.
	urlCh := make(chan string, 1)
	logs := &urlCapture{out: urlCh}
	cmd.Stdout, cmd.Stderr = logs, logs
	cmd.WaitDelay = 2 * time.Second

	// Bind cloudflared's lifetime to this process: if DeployPilot is killed
	// without a clean shutdown, the tunnel must not keep exposing the machine.
	owner, err := startOwned(cmd)
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, err
	}

	t := &Tunnel{cmd: cmd, cancel: cancel, done: make(chan struct{}), owner: owner}

	go func() {
		err := cmd.Wait()
		cancel()
		_ = owner.Close()
		_ = os.RemoveAll(dir)
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
		close(t.done)
	}()

	deadline := time.NewTimer(120 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case u := <-urlCh:
			t.URL = u
		case <-poll.C:
			if t.URL == "" {
				continue
			}
			pid, err := os.ReadFile(pidfile)
			if err != nil || strings.TrimSpace(string(pid)) != strconv.Itoa(cmd.Process.Pid) {
				continue
			}
			select {
			case <-t.done:
				return nil, t.exitError()
			case <-ctx.Done():
				t.Close()
				return nil, ctx.Err()
			default:
				return t, nil
			}
		case <-t.done:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, t.exitError()
		case <-deadline.C:
			t.Close()
			return nil, errors.New("timed out waiting for tunnel URL and first edge connection (120s)")
		case <-ctx.Done():
			t.Close()
			return nil, ctx.Err()
		}
	}
}

// urlCapture retains only a small suffix, including across write boundaries.
// Overlong log lines must neither stop draining nor grow memory without bound.
type urlCapture struct {
	mu    sync.Mutex
	out   chan<- string
	tail  string
	found bool
}

func (w *urlCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 && !w.found {
		size := min(len(p), 4096)
		chunk := w.tail + string(p[:size])
		p = p[size:]
		if u := urlRe.FindString(chunk); u != "" {
			w.found = true
			select {
			case w.out <- u:
			default:
			}
		}
		if len(chunk) > 256 {
			chunk = chunk[len(chunk)-256:]
		}
		w.tail = chunk
	}
	return n, nil
}

func scanForURL(r io.Reader, out chan<- string) {
	_, _ = io.Copy(&urlCapture{out: out}, r)
}

func (t *Tunnel) exitError() error {
	if err := t.Err(); err != nil {
		return fmt.Errorf("cloudflared exited: %w", err)
	}
	return errors.New("cloudflared exited unexpectedly (exit status 0)")
}

// Done returns a channel closed when the tunnel process exits.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Err returns the process exit error, if any.
func (t *Tunnel) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Close terminates the cloudflared process and waits for it to exit.
func (t *Tunnel) Close() {
	if t.cancel != nil {
		t.cancel()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
}
