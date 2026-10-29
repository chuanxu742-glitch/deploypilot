package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: ")
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestStreamChatContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		var body ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			t.Error("stream must be true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
			`{"choices":[{"delta":{"content":", world"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "test-key", Model: "m"})
	var got strings.Builder
	res, err := c.StreamChat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(s string) { got.WriteString(s) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got.String() != "Hello, world" {
		t.Fatalf("delta callback got %q", got.String())
	}
	if res.Message.Content != "Hello, world" {
		t.Fatalf("assembled content %q", res.Message.Content)
	}
	if res.FinishReason != "stop" {
		t.Fatalf("finish reason %q", res.FinishReason)
	}
}

func TestStreamChatToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"run_command"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	res, err := c.StreamChat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "list files"}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(res.Message.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(res.Message.ToolCalls))
	}
	tc := res.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "run_command" {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("assembled args %q", tc.Function.Arguments)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish reason %q", res.FinishReason)
	}
}

func TestRetryOn500(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	res, err := c.StreamChat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if res.Message.Content != "ok" {
		t.Fatalf("content %q", res.Message.Content)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
}

func TestStatusErrorRedactsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "secret-key-123", Model: "m"})
	_, err := c.StreamChat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, nil)
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if strings.Contains(err.Error(), "secret-key-123") {
		t.Fatalf("error leaked api key: %v", err)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"bailu-pro"},{"id":"bailu-lite"}]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL, APIKey: "k"})
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 2 || models[0] != "bailu-pro" {
		t.Fatalf("unexpected models: %v", models)
	}
}

func TestAPIKeyIsTrimmed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-abc" {
			t.Errorf("auth header = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(Config{BaseURL: srv.URL})
	c.SetAPIKey("  sk-abc\r\n")
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}
}
