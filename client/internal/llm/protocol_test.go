package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRejectIncompleteAndMalformedStreams(t *testing.T) {
	for name, response := range map[string]string{
		"eof":               "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
		"length":            sse(`{"choices":[{"delta":{"content":"partial"},"finish_reason":"length"}]}`),
		"filtered":          sse(`{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`),
		"bad_json":          "data: {broken}\n\ndata: [DONE]\n\n",
		"provider_error":    sse(`{"error":{"message":"provider failed"}}`),
		"missing_tool_id":   sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"write_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
		"duplicate_tool_id": sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"same","function":{"name":"read_file","arguments":"{}"}},{"index":1,"id":"same","function":{"name":"write_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`),
		"bad_arguments":     sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"write_file","arguments":"{"}}]},"finish_reason":"tool_calls"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts.Add(1); fmt.Fprint(w, response) }))
			defer srv.Close()
			c := New(Config{BaseURL: srv.URL, APIKey: "test"})
			_, err := c.StreamChat(context.Background(), ChatRequest{}, nil)
			if err == nil {
				t.Fatal("invalid response accepted")
			}
			if attempts.Load() != 1 {
				t.Fatalf("invalid/partial stream replayed %d times", attempts.Load())
			}
		})
	}
}

func TestStreamTotalSizeIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		line := `data: {"choices":[{"delta":{"content":"` + strings.Repeat("x", 4096) + `"}}]}` + "\n\n"
		for i := 0; i < 1024; i++ {
			if _, err := fmt.Fprint(w, line); err != nil {
				return
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	_, err := New(Config{BaseURL: srv.URL, APIKey: "test"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err == nil {
		t.Fatal("unbounded response accepted")
	}
}

// Bailu sends a ~260-byte envelope for every few characters, mostly
// reasoning. A long but ordinary reply must not be mistaken for an oversized one.
func TestEnvelopeHeavyStreamIsAccepted(t *testing.T) {
	envelope := `data: {"id":"chatcmpl-aa48f549db50f640b0aadbbd","object":"chat.completion.chunk","created":1793416226,"model":"bailu-auto","provider":"BAILU AI","choices":[{"index":0,"delta":{"content":"%s","role":"assistant","reasoning_content":"%s"},"finish_reason":null}]}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 12000; i++ { // about 3 MiB on the wire, 60 KiB of text
			fmt.Fprintf(w, envelope, "", "思考一下")
		}
		fmt.Fprintf(w, envelope, "完成", "")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	res, err := New(Config{BaseURL: srv.URL, APIKey: "test"}).StreamChat(context.Background(), ChatRequest{}, nil)
	if err != nil {
		t.Fatalf("ordinary long reasoning rejected: %v", err)
	}
	if res.Message.Content != "完成" {
		t.Fatalf("content = %q", res.Message.Content)
	}
}

// Reasoning counts toward the text limit, and a stream of empty events is
// still bounded on the wire.
func TestReasoningAndEmptyEventsAreBounded(t *testing.T) {
	for name, line := range map[string]string{
		"reasoning": `data: {"choices":[{"delta":{"reasoning_content":"` + strings.Repeat("r", 4096) + `"}}]}` + "\n\n",
		"empty":     `data: {"choices":[{"delta":{"content":""}}],"padding":"` + strings.Repeat("p", 4096) + `"}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for i := 0; i < 9000; i++ {
					if _, err := fmt.Fprint(w, line); err != nil {
						return
					}
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			if _, err := New(Config{BaseURL: srv.URL, APIKey: "test"}).StreamChat(context.Background(), ChatRequest{}, nil); err == nil {
				t.Fatal("unbounded response accepted")
			}
		})
	}
}
