package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestSteerInvalidatesPriorVerificationAtStepBoundary(t *testing.T) {
	model := newRecordingLLM(t,
		reply(toolCallSSE("old-check", toolRunCommand, `{"command":"check old requirement"}`)),
		reply(toolCallSSE("old-finish", toolFinish, `{"summary":"obsolete success","success":true,"verification_call_ids":["old-check"]}`)),
		reply(toolCallSSE("fresh-check", toolRunCommand, `{"command":"check updated requirement"}`)),
		reply(toolCallSSE("fresh-finish", toolFinish, `{"summary":"updated success","success":true,"verification_call_ids":["fresh-check"]}`)),
	)
	emit, events, _ := collectEvents()
	boundary := 0
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.srv.URL, APIKey: "test"}), Executor: newFakeExec(), Emit: emit,
		ApplyPending: func(apply func([]Instruction) *Conversation) error {
			boundary++
			if boundary == 2 {
				apply([]Instruction{{RequestID: "update-one", Text: "updated requirement"}})
			}
			return nil
		},
	})
	if err := a.Run(context.Background(), "original requirement"); err != nil {
		t.Fatal(err)
	}
	rejected, finished := false, false
	for _, e := range *events {
		rejected = rejected || e.Type == EventError && strings.Contains(e.Text, "is no longer current")
		finished = finished || e.Type == EventFinal && e.Success && strings.Contains(e.Text, "updated success")
	}
	if !rejected || !finished {
		t.Fatalf("old verification survived instruction change: rejected=%v finished=%v", rejected, finished)
	}
	if len(model.requests) != 4 {
		t.Fatalf("unexpected request count: %d", len(model.requests))
	}
}
