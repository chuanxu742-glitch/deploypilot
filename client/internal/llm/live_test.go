package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Live tests against the real Bailu API. They are skipped unless
// BAILU_API_KEY is set. BAILU_MODEL is optional: when empty, the first model
// returned by GET /models is used.
//
//	go test ./internal/llm -run Live -v

func liveClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("BAILU_API_KEY") == "" {
		t.Skip("BAILU_API_KEY not set; skipping live test")
	}
	c := FromEnv()
	if c.Model() == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := c.ListModels(ctx)
		if err != nil || len(models) == 0 {
			t.Fatalf("BAILU_MODEL not set and /models failed: %v (models=%v)", err, models)
		}
		c.SetModel(models[0])
	}
	t.Logf("using model %q", c.Model())
	return c
}

func TestLiveListModels(t *testing.T) {
	if os.Getenv("BAILU_API_KEY") == "" {
		t.Skip("BAILU_API_KEY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := FromEnv().ListModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	t.Logf("models: %v", models)
}

func TestLiveStreamChat(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var deltas int
	res, err := c.StreamChat(ctx, ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "只回复两个字：你好"}},
	}, func(string) { deltas++ })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	t.Logf("deltas=%d finish=%s content=%q", deltas, res.FinishReason, res.Message.Content)
	if strings.TrimSpace(res.Message.Content) == "" {
		t.Fatal("empty response")
	}
}

// TestLiveToolCalling checks whether the model emits OpenAI-style tool calls.
// The agent depends on this.
func TestLiveToolCalling(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := c.StreamChat(ctx, ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "You operate a Linux server only through the provided tools."},
			{Role: RoleUser, Content: "Check the kernel version of the server."},
		},
		Tools: []Tool{{
			Type: "function",
			Function: FunctionDefinition{
				Name:        "run_command",
				Description: "Run a shell command on the server and return its output.",
				Parameters:  []byte(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
			},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	t.Logf("finish=%s content=%q tool_calls=%+v", res.FinishReason, res.Message.Content, res.Message.ToolCalls)
	if len(res.Message.ToolCalls) == 0 {
		t.Fatal("model did not return any tool call: tool calling may be unsupported")
	}
	if res.Message.ToolCalls[0].Function.Name != "run_command" {
		t.Fatalf("unexpected tool %q", res.Message.ToolCalls[0].Function.Name)
	}
}
