package tunnel

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func fakeTunnel() *Tunnel {
	done := make(chan struct{})
	var once sync.Once
	return &Tunnel{URL: "https://fake.trycloudflare.com", done: done, cancel: func() { once.Do(func() { close(done) }) }}
}

func TestMaintainRetriesAndRevokesBeforeNewPairing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	attempts := 0
	start := func(context.Context) (*Tunnel, error) {
		attempts++
		events = append(events, "start")
		if attempts == 1 {
			return nil, errors.New("startup failed")
		}
		return fakeTunnel(), nil
	}
	var active *Tunnel
	wrapped := func(ctx context.Context) (*Tunnel, error) { tun, err := start(ctx); active = tun; return tun, err }
	cb := Callbacks{
		Unavailable: func() { events = append(events, "revoke") },
		Ready: func(string) error {
			events = append(events, "pair")
			if attempts == 2 {
				active.cancel()
			} else {
				cancel()
			}
			return nil
		},
		Retry: func(error, time.Duration) { events = append(events, "retry") },
	}
	if err := maintain(ctx, wrapped, cb, retryPolicy{5, time.Millisecond, time.Minute}); err != nil {
		t.Fatal(err)
	}
	want := []string{"revoke", "start", "retry", "start", "pair", "revoke", "retry", "start", "pair", "revoke"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events %v", events)
	}
}

func TestMaintainBoundsFailuresAndBackoff(t *testing.T) {
	var waits []time.Duration
	attempts := 0
	err := maintain(context.Background(), func(context.Context) (*Tunnel, error) { attempts++; return nil, errors.New("offline") },
		Callbacks{Retry: func(_ error, d time.Duration) { waits = append(waits, d) }}, retryPolicy{5, time.Millisecond, time.Minute})
	if err == nil || attempts != 5 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if !reflect.DeepEqual(waits, []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond}) {
		t.Fatal(waits)
	}
}

func TestMaintainCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	attempts := 0
	err := maintain(ctx, func(context.Context) (*Tunnel, error) { attempts++; return nil, errors.New("offline") },
		Callbacks{Retry: func(error, time.Duration) { cancel() }}, retryPolicy{5, time.Minute, time.Minute})
	if err != nil || attempts != 1 || time.Since(start) > time.Second {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestMaintainStopsOnPairingFailure(t *testing.T) {
	tun := fakeTunnel()
	revoked := 0
	err := maintain(context.Background(), func(context.Context) (*Tunnel, error) { return tun, nil }, Callbacks{
		Ready:       func(string) error { return errors.New("random source failure") },
		Unavailable: func() { revoked++ },
	}, retryPolicy{5, time.Millisecond, time.Minute})
	if err == nil || revoked != 2 {
		t.Fatalf("revocations=%d err=%v", revoked, err)
	}
	select {
	case <-tun.Done():
	default:
		t.Fatal("tunnel left running")
	}
}

func TestMaintainStableRunResetsFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	var tun *Tunnel
	var waits []time.Duration
	err := maintain(ctx, func(context.Context) (*Tunnel, error) {
		attempts++
		if attempts == 2 {
			tun = fakeTunnel()
			return tun, nil
		}
		return nil, errors.New("offline")
	}, Callbacks{
		Ready: func(string) error { time.Sleep(20 * time.Millisecond); tun.cancel(); return nil },
		Retry: func(_ error, d time.Duration) {
			waits = append(waits, d)
			if len(waits) == 3 {
				cancel()
			}
		},
	}, retryPolicy{5, time.Millisecond, 10 * time.Millisecond})
	if err != nil || !reflect.DeepEqual(waits, []time.Duration{time.Millisecond, time.Millisecond, 2 * time.Millisecond}) {
		t.Fatalf("waits=%v err=%v", waits, err)
	}
}
