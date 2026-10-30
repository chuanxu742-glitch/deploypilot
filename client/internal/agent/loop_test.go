package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// recordingLLM serves scripted responses and keeps every request body.
type recordingLLM struct {
	mu        sync.Mutex
	responses []func(w http.ResponseWriter)
	requests  []llm.ChatRequest
	srv       *httptest.Server
}

func newRecordingLLM(t *testing.T, responses ...func(w http.ResponseWriter)) *recordingLLM {
	r := &recordingLLM{responses: responses}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var parsed llm.ChatRequest
		_ = json.Unmarshal(body, &parsed)
		r.mu.Lock()
		idx := len(r.requests)
		r.requests = append(r.requests, parsed)
		r.mu.Unlock()
		if idx >= len(r.responses) {
			t.Errorf("unexpected extra model call #%d", idx)
			w.WriteHeader(400)
			return
		}
		r.responses[idx](w)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func reply(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { fmt.Fprint(w, body) }
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(code); fmt.Fprint(w, body) }
}

func fastRetries(t *testing.T) {
	old := streamRetryDelay
	streamRetryDelay = time.Millisecond
	t.Cleanup(func() { streamRetryDelay = old })
}

const droppedStream = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"

func TestInterruptedTurnIsRetriedWithoutRunningTools(t *testing.T) {
	fastRetries(t)
	model := newRecordingLLM(t,
		reply(droppedStream),
		reply(toolCallSSE("c1", toolRunCommand, `{"command":"echo once"}`)),
		reply(toolCallSSE("c2", toolFinish, `{"summary":"done","success":true,"verification_call_ids":["c1"]}`)),
	)
	fe := newFakeExec()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "k"}), Executor: fe, Emit: emit})
	if err := a.Run(context.Background(), "deploy"); err != nil {
		t.Fatalf("interrupted turn not recovered: %v", err)
	}
	if len(fe.commands) != 1 {
		t.Fatalf("commands=%v; the dropped turn must not execute anything", fe.commands)
	}
	notified := false
	for _, e := range *events {
		if e.Type == EventStatus && strings.Contains(e.Text, "模型连接中断") {
			notified = true
		}
	}
	if !notified {
		t.Fatal("operator was not told the turn was retried")
	}
}

func TestPersistentInterruptionStillFails(t *testing.T) {
	fastRetries(t)
	model := newRecordingLLM(t, reply(droppedStream), reply(droppedStream), reply(droppedStream))
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "k"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "deploy"); err == nil {
		t.Fatal("a stream that keeps dropping must not succeed")
	}
	if len(model.requests) != 1+maxStreamRetries {
		t.Fatalf("requests=%d, want %d", len(model.requests), 1+maxStreamRetries)
	}
	for _, e := range *events {
		if e.Type == EventFinal && e.Success {
			t.Fatal("reported success")
		}
	}
}

func TestContextOverflowShrinksBudgetAndRetries(t *testing.T) {
	model := newRecordingLLM(t,
		status(400, `{"error":{"message":"This model's maximum context length is 4096 tokens"}}`),
		reply(toolCallSSE("c1", toolFinish, `{"summary":"done","success":false}`)),
	)
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "k"}), Executor: newFakeExec(), ContextBytes: 64 * 1024})
	if err := a.Run(context.Background(), "deploy"); err != nil {
		t.Fatalf("overflow not recovered: %v", err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(model.requests))
	}
}

func TestOtherBadRequestsAreNotRetried(t *testing.T) {
	model := newRecordingLLM(t, status(400, "invalid model"))
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "k"}), Executor: newFakeExec()})
	if err := a.Run(context.Background(), "deploy"); err == nil {
		t.Fatal("bad request accepted")
	}
	if len(model.requests) != 1 {
		t.Fatalf("non-overflow 400 retried %d times", len(model.requests))
	}
}

func TestContinueKeepsEarlierContextAndPairs(t *testing.T) {
	first := newRecordingLLM(t,
		reply(toolCallSSE("c1", toolRunCommand, `{"command":"nvidia-smi"}`)),
		reply(toolCallSSE("c2", toolRunCommand, `{"command":"pip install vllm"}`)),
	)
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: first.srv.URL, APIKey: "k"}), Executor: newFakeExec(), MaxSteps: 2})
	if err := a.Run(context.Background(), "deploy vllm"); err == nil {
		t.Fatal("expected step budget to run out")
	}
	conv := a.Conversation()
	if conv == nil {
		t.Fatal("no conversation to continue")
	}

	second := newRecordingLLM(t,
		reply(toolCallSSE("health", toolRunCommand, `{"command":"curl --fail http://localhost:8001/health"}`)),
		reply(toolCallSSE("c3", toolFinish, `{"summary":"vllm running","success":true,"verification_call_ids":["health"]}`)))
	b := New(Config{LLM: llm.New(llm.Config{BaseURL: second.srv.URL, APIKey: "k"}), Executor: newFakeExec(), Resume: conv})
	if err := b.Run(context.Background(), "继续，用 8001 端口"); err != nil {
		t.Fatalf("continued run: %v", err)
	}
	req := second.requests[0]
	assertToolPairs(t, req.Messages)
	joined := ""
	for _, m := range req.Messages {
		joined += m.Content + "\n"
		for _, tc := range m.ToolCalls {
			joined += tc.Function.Arguments + "\n"
		}
	}
	for _, want := range []string{"deploy vllm", "nvidia-smi", "pip install vllm", "继续，用 8001 端口"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("continued request lost %q", want)
		}
	}
	if last := req.Messages[len(req.Messages)-1]; last.Role != llm.RoleUser || !strings.Contains(last.Content, "8001") {
		t.Fatalf("follow-up is not the latest message: %+v", last)
	}
}

func TestStoppedTurnLeavesValidConversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &cancelProbeExecutor{cancel: cancel}
	calls := []map[string]any{
		{"index": 0, "id": "a", "type": "function", "function": map[string]string{"name": toolRunCommand, "arguments": `{"command":"echo probe"}`}},
		{"index": 1, "id": "b", "type": "function", "function": map[string]string{"name": toolWriteFile, "arguments": `{"path":"x","content":"y"}`}},
	}
	payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
	model := newRecordingLLM(t, reply(fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", payload)))
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "k"}), Executor: e})
	_ = a.Run(ctx, "stop midway")
	conv := a.Conversation()
	if conv == nil {
		t.Fatal("no conversation")
	}
	assertToolPairs(t, conv.window.request("m", nil).Messages)
}

func TestFollowUpSurvivesCompaction(t *testing.T) {
	w := newContextWindow([]llm.Message{{Role: llm.RoleSystem, Content: "rules"}, {Role: llm.RoleUser, Content: "original task"}})
	w.addFollowUp("use port 8001")
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("c%d", i)
		w.turns = append(w.turns, []llm.Message{
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Type: "function", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"probe"}`}}}},
			{Role: llm.RoleTool, ToolCallID: id, Content: strings.Repeat("log line ", 600)},
		})
	}
	req, compacted, err := w.prepare("m", nil, 8*1024)
	if err != nil || !compacted {
		t.Fatalf("compacted=%v err=%v", compacted, err)
	}
	assertToolPairs(t, req.Messages)
	found := false
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "use port 8001") {
			found = true
		}
	}
	if !found {
		t.Fatal("operator follow-up lost after compaction")
	}
}

func TestMachineInfoReportsReadableProbes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	info := NewShellExecutor().MachineInfo(ctx)
	t.Logf("probes took %s:\n%s", time.Since(start), info)
	for _, label := range []string{"OS:", "User:", "CPU/Mem:", "GPU:", "Python:", "Docker:", "Network:"} {
		if !strings.Contains(info, label) {
			t.Fatalf("missing %s in machine info", label)
		}
	}
	if strings.Contains(info, "is not recognized") || strings.Contains(info, "CommandNotFound") {
		t.Fatalf("probe leaked a shell error instead of a fallback:\n%s", info)
	}
	// Output that is not UTF-8 (e.g. GBK from a Chinese Windows console) is
	// replaced with '?' by the capture buffer; an OS name never contains one.
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "OS:") && strings.Contains(line, "?") {
			t.Fatalf("probe output was not UTF-8: %q", line)
		}
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("probes should run in parallel")
	}
}
