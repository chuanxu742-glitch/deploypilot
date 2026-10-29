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

func TestInterruptedStreamIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err == nil || !IsStreamInterrupted(err) {
		t.Fatalf("early EOF must be classified as interrupted, got %v", err)
	}
}

func TestDeterministicBadStreamsAreNotInterruptions(t *testing.T) {
	for name, body := range map[string]string{
		"length":   sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"length"}]}`),
		"bad_json": "data: {broken}\n\ndata: [DONE]\n\n",
		"no_reason_with_done": sse(`{"choices":[{"delta":{"content":"x"}}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			_, err := New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(context.Background(), ChatRequest{}, nil)
			if err == nil {
				t.Fatal("bad stream accepted")
			}
			if IsStreamInterrupted(err) {
				t.Fatalf("deterministic failure misclassified as interruption: %v", err)
			}
		})
	}
}

func TestStopWithToolCallsIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"run_command","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	res, err := New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err != nil {
		t.Fatalf("stop + tool_calls rejected: %v", err)
	}
	if res.FinishReason != "tool_calls" || len(res.Message.ToolCalls) != 1 {
		t.Fatalf("not normalised: reason=%q calls=%d", res.FinishReason, len(res.Message.ToolCalls))
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = 10 * time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	start := time.Now()
	if _, err := New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(context.Background(), ChatRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After ignored: retried after %s", elapsed)
	}
}

func TestContextOverflowDetection(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"openai":   {&apiError{status: 400, body: `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`}, true},
		"vllm":     {&apiError{status: 400, body: "prompt is too long"}, true},
		"413":      {&apiError{status: 413, body: "payload"}, true},
		"auth":     {&apiError{status: 401, body: "context length"}, false},
		"other400": {&apiError{status: 400, body: "invalid model"}, false},
		"network":  {errors.New("context length"), false},
	}
	for name, tc := range cases {
		if got := IsContextOverflow(tc.err); got != tc.want {
			t.Errorf("%s: IsContextOverflow=%v want %v", name, got, tc.want)
		}
	}
}
