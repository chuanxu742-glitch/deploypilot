package tunnel

import (
	"context"
	"fmt"
	"time"
)

// Callbacks reports changes in tunnel availability to the local application.
// Ready must issue a fresh pairing token: quick-tunnel URLs change on restart.
// Unavailable must revoke old credentials and is also called before startup.
type Callbacks struct {
	Ready       func(publicURL string) error
	Unavailable func()
	Retry       func(cause error, delay time.Duration)
}

type retryPolicy struct {
	maxFailures  int
	initialDelay time.Duration
	stableAfter  time.Duration
}

// Maintain runs until cancellation or five consecutive failures. Short network
// interruptions stay with cloudflared's own reconnect logic; only process exit
// or failed startup creates a new URL. It never resumes interrupted commands.
func Maintain(ctx context.Context, binPath string, localPort int, cb Callbacks) error {
	return maintain(ctx, func(ctx context.Context) (*Tunnel, error) { return Start(ctx, binPath, localPort) }, cb,
		retryPolicy{maxFailures: 5, initialDelay: time.Second, stableAfter: time.Minute})
}

func maintain(ctx context.Context, start func(context.Context) (*Tunnel, error), cb Callbacks, policy retryPolicy) error {
	if cb.Unavailable != nil {
		cb.Unavailable()
	}
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		tun, err := start(ctx)
		if err == nil {
			started := time.Now()
			if ctx.Err() == nil && cb.Ready != nil {
				if err := cb.Ready(tun.URL); err != nil {
					tun.Close()
					if cb.Unavailable != nil {
						cb.Unavailable()
					}
					return fmt.Errorf("prepare tunnel pairing: %w", err)
				}
			}
			select {
			case <-ctx.Done():
			case <-tun.Done():
			}
			tun.Close()
			if cb.Unavailable != nil {
				cb.Unavailable()
			}
			if time.Since(started) >= policy.stableAfter {
				failures = 0
			}
			err = tun.exitError()
		}
		if ctx.Err() != nil {
			return nil
		}
		failures++
		if failures >= policy.maxFailures {
			return fmt.Errorf("tunnel unavailable after %d consecutive failures: %w", failures, err)
		}
		delay := policy.initialDelay * time.Duration(1<<(failures-1))
		if cb.Retry != nil {
			cb.Retry(err, delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
