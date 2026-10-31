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

func numericProviderError(code string) string {
	return `{"error":{"message":"Service is temporarily unavailable. secret-key","code":` + code + `,"metadata":{"provider_name":"BAILU AI"},"type":"api_error"}}`
}

func TestNumericStreamErrorsRetryOnlyCurrentRequest(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	for _, code := range []string{`429`, `500`, `503`, `599`, `"429"`, `"500"`, `"503"`, `"599"`} {
		t.Run(code, func(t *testing.T) {
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
						numericProviderError(code),
					))
					return
				}
				fmt.Fprint(w, sse(`{"choices":[{"delta":{"content":"fresh"},"finish_reason":"stop"}]}`))
			}))
			defer srv.Close()
			var waits int
			ctx := WithRetryWait(context.Background(), func(attempt int, _ time.Duration) {
				waits++
				if attempt != 1 {
					t.Errorf("unexpected retry attempt %d", attempt)
				}
			})
			request := ChatRequest{Messages: []Message{
				{Role: RoleUser, Content: "deploy once"},
				{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "completed", Type: "function", Function: FunctionCall{Name: "run_command", Arguments: `{"command":"install once"}`}}}},
				{Role: RoleTool, ToolCallID: "completed", Name: "run_command", Content: "exit=0"},
			}}
			result, err := New(Config{BaseURL: srv.URL, APIKey: "secret-key", Model: "m"}).StreamChat(ctx, request, nil)
			if err != nil {
				t.Fatalf("numeric transient stream error was not recovered: %v", err)
			}
			requestsMu.Lock()
			defer requestsMu.Unlock()
			if calls.Load() != 2 || waits != 1 || len(requests) != 2 || requests[0] != requests[1] {
				t.Fatalf("retry changed current request: calls=%d waits=%d requests=%q", calls.Load(), waits, requests)
			}
			if result.Message.Content != "fresh" || len(result.Message.ToolCalls) != 0 {
				t.Fatalf("failed response leaked partial text or tool call: %+v", result.Message)
			}
		})
	}
}

func TestNumericStreamErrorExhaustionIsBoundedAndRedacted(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, sse(numericProviderError(`503`)))
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, APIKey: "secret-key"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err == nil || calls.Load() != 8 || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want bounded error retaining provider code: err=%v calls=%d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("provider error leaked API key: %v", err)
	}
}

func TestNumericStreamErrorRetryWaitCanBeCancelled(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, sse(numericProviderError(`"429"`)))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithRetryWait(ctx, func(int, time.Duration) { cancel() })
	_, err := New(Config{BaseURL: srv.URL, APIKey: "secret-key"}).StreamChat(ctx, ChatRequest{}, nil)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("want cancellation before second request: err=%v calls=%d", err, calls.Load())
	}
}

func TestNonTransientNumericStreamErrorsDoNotRetry(t *testing.T) {
	for _, code := range []string{`400`, `401`, `403`, `404`, `422`, `499`, `600`, `"400"`, `"401"`, `"403"`, `"422"`, `"600"`, `"503.0"`, `"other_busy"`, `null`} {
		t.Run(code, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, sse(numericProviderError(code)))
			}))
			defer srv.Close()
			_, err := New(Config{BaseURL: srv.URL, APIKey: "secret-key"}).StreamChat(context.Background(), ChatRequest{}, nil)
			if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "secret-key") {
				t.Fatalf("want one redacted non-retryable error: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}
