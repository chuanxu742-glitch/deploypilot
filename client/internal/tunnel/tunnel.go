package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
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
	mu     sync.Mutex
	err    error
}

// Start launches cloudflared to expose http://127.0.0.1:<localPort> and
// blocks until the public URL is parsed or the timeout elapses.
func Start(ctx context.Context, binPath string, localPort int) (*Tunnel, error) {
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, binPath,
		"tunnel",
		"--no-autoupdate",
		"--url", fmt.Sprintf("http://127.0.0.1:%d", localPort),
	)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	t := &Tunnel{cmd: cmd, cancel: cancel, done: make(chan struct{})}

	urlCh := make(chan string, 1)
	go scanForURL(io.MultiReader(stderr, stdout), urlCh)

	go func() {
		err := cmd.Wait()
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
		close(t.done)
	}()

	select {
	case u := <-urlCh:
		t.URL = u
		return t, nil
	case <-t.done:
		return nil, fmt.Errorf("cloudflared exited before announcing a URL: %w", t.Err())
	case <-time.After(45 * time.Second):
		t.Close()
		return nil, errors.New("timed out waiting for tunnel URL")
	case <-ctx.Done():
		t.Close()
		return nil, ctx.Err()
	}
}

// scanForURL reads lines until it finds the tunnel URL, then keeps draining
// so the subprocess never blocks on a full pipe.
func scanForURL(r io.Reader, out chan<- string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	found := false
	for sc.Scan() {
		if !found {
			if m := urlRe.FindString(sc.Text()); m != "" {
				found = true
				out <- m
			}
		}
	}
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
