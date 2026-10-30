package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestRepeatGuardIgnoresCallAndLogIDs(t *testing.T) {
	var guard repetitionGuard
	for i, id := range []string{"a", "b", "c", "d"} {
		call := llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"probe"}`}}
		out := llm.Message{Content: "[call_id: " + id + "]\nexit=1\nunchanged\n[full output: /logs/" + id + ".log]"}
		warn, stop := guard.observe(call, out)
		if warn != (i == 2) || stop != (i == 3) {
			t.Fatalf("repeat %d: warn=%v stop=%v", i+1, warn, stop)
		}
	}
	_, stop := guard.observe(llm.ToolCall{Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"different"}`}}, llm.Message{Content: "unchanged"})
	if stop {
		t.Fatal("changing the call must reset repetition")
	}
}

func TestFinishRequiresRealSuccessfulVerification(t *testing.T) {
	a := New(Config{Executor: newFakeExec()})
	call := func(id, name, args string) (bool, llm.Message) {
		return a.runTool(context.Background(), 1, llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: name, Arguments: args}})
	}
	for _, args := range []string{`{"summary":"done","success":true}`, `{"summary":"done","success":true,"verification_call_ids":["invented"]}`} {
		if done, msg := call("finish", toolFinish, args); done || !strings.Contains(msg.Content, "error:") {
			t.Fatalf("unverified success accepted: done=%v result=%s", done, msg.Content)
		}
	}
	call("probe", toolRunCommand, `{"command":"curl --fail http://localhost:8000/health"}`)
	if done, msg := call("finish", toolFinish, `{"summary":"done","success":true,"verification_call_ids":["probe"]}`); !done || strings.Contains(msg.Content, "error:") {
		t.Fatalf("observed verification rejected: done=%v result=%s", done, msg.Content)
	}
}

func TestFinishNeedsInputIsSeparateFromFailureAndSuccess(t *testing.T) {
	for _, input := range []string{
		`{"summary":"need input","success":true,"outcome":"needs_input"}`,
		`{"summary":"need input","success":false,"outcome":"needs_input","verification_call_ids":[]}`,
		`{"summary":"need input","success":false,"outcome":"unknown"}`,
	} {
		if err := validateToolArgs(toolFinish, input); err == nil {
			t.Errorf("accepted incompatible finish outcome: %s", input)
		}
	}

	var events []Event
	a := New(Config{Executor: newFakeExec(), Emit: func(e Event) { events = append(events, e) }})
	done, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "need", Function: llm.FunctionCall{
		Name: toolFinish, Arguments: `{"summary":"Which region should I use?","success":false,"outcome":"needs_input"}`,
	}})
	if !done || strings.HasPrefix(msg.Content, "error:") {
		t.Fatalf("needs_input should end normally: done=%v, result=%s", done, msg.Content)
	}
	final := events[len(events)-1]
	if final.Type != EventFinal || final.State != "needs_input" || final.Success || final.Text != "Which region should I use?" {
		t.Fatalf("unexpected needs_input event: %+v", final)
	}

	events = nil
	done, _ = a.runTool(context.Background(), 2, llm.ToolCall{ID: "failed", Function: llm.FunctionCall{
		Name: toolFinish, Arguments: `{"summary":"could not complete","success":false}`,
	}})
	if !done {
		t.Fatal("ordinary failure finish should still end")
	}
	final = events[len(events)-1]
	if final.Type != EventFinal || final.State != "" || final.Success || final.Text != "could not complete" {
		t.Fatalf("ordinary finish(false) changed: %+v", final)
	}
}

func TestFileChangeInvalidatesVerification(t *testing.T) {
	a := New(Config{Executor: newFakeExec()})
	call := func(id, name, args string) (bool, llm.Message) {
		return a.runTool(context.Background(), 1, llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: name, Arguments: args}})
	}
	call("check", toolRunCommand, `{"command":"check"}`)
	call("write", toolWriteFile, `{"path":"config","content":"changed"}`)
	if done, _ := call("done", toolFinish, `{"summary":"done","success":true,"verification_call_ids":["check"]}`); done {
		t.Fatal("verification before a file change must be repeated")
	}
	if done, _ := call("help", toolFinish, `{"summary":"needs input","success":false}`); !done {
		t.Fatal("unsuccessful finish should remain available")
	}
}

func TestAgentPausesRepeatedCalls(t *testing.T) {
	responses := make([]string, 4)
	for i, id := range []string{"a", "b", "c", "d"} {
		responses[i] = toolCallSSE(id, toolRunCommand, `{"command":"unchanged"}`)
	}
	model := scriptedLLM(t, responses)
	defer model.Close()
	executor := newFakeExec()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.URL, APIKey: "test"}), Executor: executor, Emit: emit})
	if err := a.Run(context.Background(), "probe"); err == nil {
		t.Fatal("repeated calls should pause")
	}
	if len(executor.commands) != 4 {
		t.Fatalf("commands=%d, expected 4", len(executor.commands))
	}
	warned := false
	for _, event := range *events {
		if event.Type == EventStatus && strings.Contains(event.Text, "连续出现 3 次") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("no warning before pausing")
	}
	assertToolPairs(t, a.Conversation().window.request("test", nil).Messages)
}

func TestWaitForCanFinishWithObservedEvidence(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer health.Close()
	model := scriptedLLM(t, []string{
		toolCallSSE("health", toolWaitFor, `{"kind":"url","target":"`+health.URL+`"}`),
		toolCallSSE("done", toolFinish, `{"summary":"ready","success":true,"verification_call_ids":["health"]}`),
	})
	defer model.Close()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.URL, APIKey: "test"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "wait for health"); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Type != EventFinal || !last.Success || !strings.Contains(last.Text, "HTTP 204") {
		t.Fatalf("missing observed evidence: %+v", last)
	}
}

func TestFailedCommandIsNotVerification(t *testing.T) {
	a := New(Config{Executor: failingToolExecutor{newFakeExec()}})
	_, _ = a.runTool(context.Background(), 1, llm.ToolCall{ID: "failed", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"bad"}`}})
	done, _ := a.runTool(context.Background(), 2, llm.ToolCall{ID: "finish", Function: llm.FunctionCall{Name: toolFinish, Arguments: `{"summary":"ready","success":true,"verification_call_ids":["failed"]}`}})
	if done {
		t.Fatal("failed command accepted as verification")
	}
}
