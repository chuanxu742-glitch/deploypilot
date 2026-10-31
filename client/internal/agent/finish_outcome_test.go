package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestFinishExplicitOutcomesAndEmptyWaitingEvidence(t *testing.T) {
	for _, tc := range []struct{ name, args, state string }{
		{"completed", `{"summary":"verified","success":true,"outcome":"completed","verification_call_ids":["check"]}`, ""},
		{"failed", `{"summary":"deployment failed","success":false,"outcome":"failed","verification_call_ids":[]}`, ""},
		{"needs_input", `{"summary":"Which region?","success":false,"outcome":"needs_input","verification_call_ids":[]}`, "needs_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []Event
			a := New(Config{Executor: newFakeExec(), Emit: func(e Event) { events = append(events, e) }})
			a.runTool(context.Background(), 1, llm.ToolCall{ID: "check", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"check health"}`}})
			done, result := a.runTool(context.Background(), 2, llm.ToolCall{ID: "end", Function: llm.FunctionCall{Name: toolFinish, Arguments: tc.args}})
			if !done || strings.HasPrefix(result.Content, "error:") {
				t.Fatalf("valid explicit outcome rejected: done=%v result=%s", done, result.Content)
			}
			final := events[len(events)-1]
			if final.Type != EventFinal || final.State != tc.state {
				t.Fatalf("unexpected final state: %+v", final)
			}
		})
	}
}

func TestFinishExplicitOutcomeCannotBypassVerification(t *testing.T) {
	a := New(Config{Executor: newFakeExec()})
	for _, input := range []string{
		`{"summary":"done","success":true,"outcome":"completed","verification_call_ids":["invented"]}`,
		`{"summary":"done","success":false,"outcome":"completed"}`,
		`{"summary":"failed","success":true,"outcome":"failed"}`,
		`{"summary":"need input","success":false,"outcome":"needs_input","verification_call_ids":["check"]}`,
	} {
		done, result := a.runTool(context.Background(), 1, llm.ToolCall{ID: "end", Function: llm.FunctionCall{Name: toolFinish, Arguments: input}})
		if done || !strings.HasPrefix(result.Content, "error:") {
			t.Fatalf("invalid outcome accepted: done=%v result=%s", done, result.Content)
		}
	}
}
