package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestSavedConversationBoundsWithoutMutatingSource(t *testing.T) {
	w := newContextWindow([]llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}, {Role: llm.RoleSystem, Content: "old machine facts"}, {Role: llm.RoleUser, Content: "original"}})
	for i := 0; i < 12; i++ {
		w.turns = append(w.turns, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}}}}, {Role: llm.RoleTool, ToolCallID: "call", Content: strings.Repeat("output", 12000)}})
	}
	before, _ := json.Marshal(w.request("", nil))
	c := &Conversation{window: w, appliedSteers: map[string]bool{"applied-once": true}}
	data, err := c.Encode("task-one", "original")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(w.request("", nil))
	if string(before) != string(after) {
		t.Fatal("saving compacted the running conversation in place")
	}
	if len(data) > MaxConversationBytes {
		t.Fatal("conversation exceeded storage budget")
	}
	restored, err := DecodeConversation(data, "task-one", "original")
	if err != nil || !restored.appliedSteers["applied-once"] {
		t.Fatalf("restore=%v error=%v", restored, err)
	}
	var dto savedConversation
	if err = json.Unmarshal(data, &dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.Turns) == 0 {
		t.Fatal("lost all completed turns")
	}
	dto.Turns[len(dto.Turns)-1] = dto.Turns[len(dto.Turns)-1][:1]
	broken, _ := json.Marshal(dto)
	if _, err = DecodeConversation(broken, "task-one", "original"); err == nil {
		t.Fatal("unpaired tool call was accepted")
	}
}
