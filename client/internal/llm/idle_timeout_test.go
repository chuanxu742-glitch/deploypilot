package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamIdleTimeoutInterruptsAndCancelsRequest(t *testing.T) {
	canceled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer srv.Close()

	_, err := New(Config{BaseURL: srv.URL, APIKey: "k", StreamIdleTimeout: 40 * time.Millisecond}).StreamChat(context.Background(), ChatRequest{}, nil)
	if !IsStreamInterrupted(err) {
		t.Fatalf("idle stream should be interrupted, got %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not cancel the HTTP request")
	}
}

func TestStreamWithContinuousDataDoesNotIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"c\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"d\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"e\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"f\"},\"finish_reason\":\"stop\"}]}\n\n",
			"data: [DONE]\n\n",
		} {
			if _, err := fmt.Fprint(w, part); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()

	result, err := New(Config{BaseURL: srv.URL, APIKey: "k", StreamIdleTimeout: 200 * time.Millisecond}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err != nil {
		t.Fatalf("continuous stream was interrupted: %v", err)
	}
	if result.Message.Content != "abcdef" {
		t.Fatalf("content = %q", result.Message.Content)
	}
}

func TestStreamRetryWaitCallbackAndAttemptLimit(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	var calls atomic.Int32
	var waits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", OnRetryWait: func(attempt int, delay time.Duration) {
		if attempt < 1 || delay <= 0 {
			t.Errorf("invalid retry callback: attempt=%d delay=%s", attempt, delay)
		}
		waits.Add(1)
	}})
	_, err := c.StreamChat(context.Background(), ChatRequest{}, nil)
	if err == nil || calls.Load() != 8 || waits.Load() != 7 {
		t.Fatalf("err=%v calls=%d waits=%d", err, calls.Load(), waits.Load())
	}
}

func TestStreamRetryWaitCanBeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer srv.Close()
	ctx = WithRetryWait(ctx, func(int, time.Duration) { cancel() })
	c := New(Config{BaseURL: srv.URL, APIKey: "k"})
	_, err := c.StreamChat(ctx, ChatRequest{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry wait should return context cancellation, got %v", err)
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
	defer srv.Close()
	_, _ = New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if calls.Load() != 1 {
		t.Fatalf("bad request attempts=%d, want 1", calls.Load())
	}
}
