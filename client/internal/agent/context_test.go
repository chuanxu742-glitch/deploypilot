package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deploypilot/deploypilot/internal/llm"
)

type longOutputExecutor struct{ *fakeExec }

func (e longOutputExecutor) Run(_ context.Context, command string, _ time.Duration, _ func(string)) (int, string, error) {
	return 0, "result for " + command + "\n" + strings.Repeat("中间日志 ", 6000) + "\nIMPORTANT_TAIL", nil
}

func assertToolPairs(t *testing.T, messages []llm.Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range messages {
		if m.Role == llm.RoleTool {
			if !pending[m.ToolCallID] {
				t.Errorf("orphan tool result %q", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			t.Errorf("missing tool results before %s: %v", m.Role, pending)
		}
		for _, tc := range m.ToolCalls {
			pending[tc.ID] = true
		}
	}
	if len(pending) > 0 {
		t.Errorf("request ends with missing results: %v", pending)
	}
}

func TestContextCompactionBoundsRequestsAndKeepsPairs(t *testing.T) {
	const budget = 24 * 1024
	const task = "Deploy the requested service; preserve the original database."
	requests, compactions := 0, 0
	sawHistory := false
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		if len(body) > budget {
			t.Errorf("request %d exceeds budget: %d > %d", requests, len(body), budget)
		}
		var req llm.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		if len(req.Messages) < 3 || req.Messages[0].Role != llm.RoleSystem || req.Messages[2].Content != task {
			t.Errorf("original task/system instructions lost: %+v", req.Messages)
		}
		assertToolPairs(t, req.Messages)
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "历史执行记录") {
				sawHistory = true
			}
			if m.Role == llm.RoleTool && !strings.Contains(m.Content, "IMPORTANT_TAIL") {
				t.Error("recent log tail lost")
			}
		}
		if requests <= 15 {
			fmt.Fprint(w, toolCallSSE(fmt.Sprintf("c%d", requests), toolRunCommand, fmt.Sprintf(`{"command":"probe %d"}`, requests)))
		} else {
			fmt.Fprint(w, toolCallSSE("done", toolFinish, `{"summary":"verified","success":true,"verification_call_ids":["c15"]}`))
		}
	}))
	defer provider.Close()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"}), Executor: longOutputExecutor{newFakeExec()}, ContextBytes: budget, Emit: func(e Event) {
		if e.Type == EventStatus && strings.Contains(e.Text, "压缩") {
			compactions++
		}
	}})
	if err := a.Run(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if requests != 16 || compactions == 0 || !sawHistory {
		t.Fatalf("long task not completed with visible compaction/history: requests=%d compactions=%d history=%v", requests, compactions, sawHistory)
	}
}

func TestOversizedTaskFailsBeforeModelRequest(t *testing.T) {
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; fmt.Fprint(w, textSSE("done")) }))
	defer provider.Close()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"}), Executor: newFakeExec(), ContextBytes: 16 * 1024})
	err := a.Run(context.Background(), strings.Repeat("goal", 10000))
	if err == nil || requests != 0 {
		t.Fatalf("oversized goal not rejected before network: error=%v requests=%d", err, requests)
	}
}

func TestContextLargeArgumentsKeepWholeToolRounds(t *testing.T) {
	prefix := []llm.Message{{Role: llm.RoleSystem, Content: "Keep constraints"}, {Role: llm.RoleUser, Content: "Keep original task"}}
	w := newContextWindow(prefix)
	args, _ := json.Marshal(map[string]string{"path": "/etc/service/config", "content": strings.Repeat("configuration", 10000)})
	w.turns = append(w.turns, []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "a", Type: "function", Function: llm.FunctionCall{Name: toolWriteFile, Arguments: string(args)}}, {ID: "b", Type: "function", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"verify"}`}}}},
		{Role: llm.RoleTool, ToolCallID: "a", Content: "ok: wrote /etc/service/config"},
		{Role: llm.RoleTool, ToolCallID: "b", Content: "exit=1\nverification failed"},
	})
	req, compacted, err := w.prepare("model", toolSet(), 16*1024)
	if err != nil || !compacted || requestBytes(req) > 16*1024 {
		t.Fatalf("cannot bound large call: compacted=%v error=%v", compacted, err)
	}
	assertToolPairs(t, req.Messages)
	encoded, _ := json.Marshal(req)
	if !strings.Contains(string(encoded), "/etc/service/config") || !strings.Contains(string(encoded), "exit=1") {
		t.Fatal("file identity or failed verification lost from summary")
	}
	if req.Messages[0].Content != prefix[0].Content || req.Messages[0].Role != prefix[0].Role || req.Messages[1].Content != prefix[1].Content {
		t.Fatal("protected prefix changed")
	}
}

func TestContextClippingPreservesUnicodeAndTail(t *testing.T) {
	s := "HEADER " + strings.Repeat("中文🙂", 1000) + " CRITICAL_TAIL"
	clipped := clipContext(s, 2048)
	if !utf8.ValidString(clipped) || len(clipped) > 2048 || !strings.HasPrefix(clipped, "HEADER") || !strings.HasSuffix(clipped, "CRITICAL_TAIL") || !strings.Contains(clipped, "已截短") {
		t.Fatal("clipping broke UTF-8, bounds, or useful boundaries")
	}
}
