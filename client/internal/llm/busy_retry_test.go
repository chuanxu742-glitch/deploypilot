package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const upstreamBusyEvent = `{"error":{"message":"Service is temporarily busy. secret-key","code":"upstream_busy","metadata":{"provider_name":"BAILU AI"},"type":"api_error"}}`

func TestUpstreamBusyRetriesThenReturnsFreshResponse(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	var calls atomic.Int32
	var requests []string
	var requestsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestsMu.Lock()
		requests = append(requests, string(body))
		requestsMu.Unlock()
		if calls.Add(1) == 1 {
			fmt.Fprint(w, sse(
				`{"choices":[{"delta":{"content":"partial","tool_calls":[{"index":0,"id":"discard-me","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"x\",\"content\":"}}]}}]}`,
				upstreamBusyEvent,
			))
			return
		}
		fmt.Fprint(w, sse(`{"choices":[{"delta":{"content":"fresh"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	var callbackCalls atomic.Int32
	c := New(Config{BaseURL: srv.URL, APIKey: "secret-key", Model: "m"})
	ctx := WithRetryWait(context.Background(), func(attempt int, delay time.Duration) {
		if attempt != 1 || delay != retryBaseDelay {
			t.Errorf("retry callback = (%d, %s), want (1, %s)", attempt, delay, retryBaseDelay)
		}
		callbackCalls.Add(1)
	})
	var displayed strings.Builder
	result, err := c.StreamChat(ctx, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hello"}}}, func(delta string) { displayed.WriteString(delta) })
	if err != nil {
		t.Fatalf("busy response was not recovered: %v", err)
	}
	if calls.Load() != 2 || callbackCalls.Load() != 1 {
		t.Fatalf("calls=%d callbacks=%d, want 2 and 1", calls.Load(), callbackCalls.Load())
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("retry did not resend the same model request: %q", requests)
	}
	if result.Message.Content != "fresh" || len(result.Message.ToolCalls) != 0 {
		t.Fatalf("partial busy response leaked into result: %+v", result.Message)
	}
	if displayed.String() != "partialfresh" {
		t.Fatalf("streamed display = %q, want partial and retry deltas", displayed.String())
	}
}

func TestUpstreamBusyExhaustionIsReadableAndRetainsCode(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, sse(upstreamBusyEvent))
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, APIKey: "secret-key"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err == nil || calls.Load() != 8 {
		t.Fatalf("err=%v calls=%d, want an error after 8 attempts", err, calls.Load())
	}
	if !strings.Contains(err.Error(), "模型服务持续繁忙，请稍后继续当前任务或更换模型") || !strings.Contains(err.Error(), "upstream_busy") {
		t.Fatalf("busy exhaustion error lacks recovery guidance or code: %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("busy error leaked API key: %v", err)
	}
}

func TestUpstreamBusyRetryWaitCancellationStopsImmediately(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, sse(upstreamBusyEvent))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithRetryWait(ctx, func(int, time.Duration) { cancel() })
	_, err := New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(ctx, ChatRequest{}, nil)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d, want canceled after one request", err, calls.Load())
	}
}

func TestUnknownStreamErrorAndUnauthorizedDoNotRetry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		payload string
	}{
		{name: "unknown_stream_code", status: http.StatusOK, payload: `{"error":{"message":"temporarily busy","code":"other_busy","type":"api_error"}}`},
		{name: "unauthorized", status: http.StatusUnauthorized, payload: "unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.status != http.StatusOK {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.payload)
					return
				}
				fmt.Fprint(w, sse(tc.payload))
			}))
			defer srv.Close()
			_, err := New(Config{BaseURL: srv.URL, APIKey: "k"}).StreamChat(context.Background(), ChatRequest{}, nil)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d, want one request and error", err, calls.Load())
			}
		})
	}
}
