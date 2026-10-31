package llm

import (
	"io"
	"sync/atomic"
	"time"
)

// idleTimeoutBody cancels the owning HTTP request if the response body stops
// producing bytes. Its timer callback is not a reader goroutine, so shutdown
// only needs to stop the timer and close the underlying body.
type idleTimeoutBody struct {
	body     io.ReadCloser
	timer    *time.Timer
	timeout  time.Duration
	timedOut atomic.Bool
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration, cancel func()) *idleTimeoutBody {
	b := &idleTimeoutBody{body: body, timeout: timeout}
	b.timer = time.AfterFunc(timeout, func() {
		b.timedOut.Store(true)
		cancel()
	})
	return b
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 && !b.timedOut.Load() {
		b.timer.Reset(b.timeout)
	}
	return n, err
}

func (b *idleTimeoutBody) Close() error {
	b.timer.Stop()
	return b.body.Close()
}

func (b *idleTimeoutBody) TimedOut() bool { return b.timedOut.Load() }

var _ io.ReadCloser = (*idleTimeoutBody)(nil)
