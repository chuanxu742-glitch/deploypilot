package agent

import (
	"context"
	"encoding/json"
	"github.com/deploypilot/deploypilot/internal/llm"
	"testing"
)

func eventJSON(t *testing.T, e Event) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestActivitySeparatesModelWaitFromExecution(t *testing.T) {
	srv := scriptedLLM(t, []string{toolCallSSE("call-1", "run_command", `{"command":"echo hello"}`), toolCallSSE("finish-1", "finish", `{"summary":"done","success":true,"verification_call_ids":["call-1"]}`)})
	defer srv.Close()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "check"); err != nil {
		t.Fatal(err)
	}
	states := []string{}
	var call, output, done map[string]any
	for _, e := range *events {
		m := eventJSON(t, e)
		if ts, ok := m["timestamp"].(float64); !ok || ts <= 0 {
			t.Errorf("missing event time: %v", m)
		}
		if e.Type == "activity" {
			states = append(states, m["state"].(string))
		}
		if e.Type == EventToolCall && e.Tool == "run_command" {
			call = m
		}
		if e.Type == EventOutput {
			output = m
		}
		if e.Type == EventToolDone && e.Tool == "run_command" {
			done = m
		}
	}
	want := []string{"preparing", "generating", "executing", "generating"}
	if len(states) != len(want) {
		t.Fatalf("activities=%v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("activities=%v", states)
		}
	}
	for _, m := range []map[string]any{call, output, done} {
		if m["callId"] != "call-1" {
			t.Errorf("unlinked command event: %v", m)
		}
	}
	if call["hostname"] == "" || call["hostname"] == nil || call["cwd"] == "" || call["cwd"] == nil {
		t.Errorf("missing execution location: %v", call)
	}
	if v, ok := done["exitCode"].(float64); !ok || v != 0 {
		t.Errorf("successful exit must be explicit: %v", done)
	}
	if v, ok := done["durationMs"].(float64); !ok || v < 0 {
		t.Errorf("missing actual tool duration: %v", done)
	}
}

func TestBlockedCommandNeverReportsExecuting(t *testing.T) {
	emit, events, _ := collectEvents()
	a := New(Config{Executor: newFakeExec(), Emit: emit})
	a.runTool(context.Background(), 1, llm.ToolCall{ID: "blocked", Function: llm.FunctionCall{Name: "run_command", Arguments: `{"command":"rm -rf /"}`}})
	for _, e := range *events {
		if e.Type == "activity" && eventJSON(t, e)["state"] == "executing" {
			t.Fatal("blocked command was presented as running")
		}
	}
}
