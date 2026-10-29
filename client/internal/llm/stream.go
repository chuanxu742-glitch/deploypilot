package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// streamChunk is one SSE "data:" event from /chat/completions with stream=true.
type streamChunk struct {
	Error   json.RawMessage `json:"error"`
	Choices []struct {
		Delta        deltaMsg `json:"delta"`
		FinishReason string   `json:"finish_reason"`
	} `json:"choices"`
}

// deltaMsg is the incremental delta within a stream chunk.
type deltaMsg struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []toolCallDelta `json:"tool_calls"`
}

// toolCallDelta is a streamed fragment of a tool call. Fields arrive across
// multiple chunks and are keyed by Index.
type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// assembler reconstructs a complete Message from streamed deltas.
type assembler struct {
	content      []byte
	toolCalls    map[int]*ToolCall
	order        []int
	finishReason string
}

func newAssembler() *assembler {
	return &assembler{toolCalls: make(map[int]*ToolCall)}
}

func (a *assembler) add(d deltaMsg) error {
	if d.Content != "" {
		a.content = append(a.content, d.Content...)
	}
	for _, tc := range d.ToolCalls {
		if tc.Index < 0 || tc.Index >= 32 {
			return fmt.Errorf("tool call index outside 0..31: %d", tc.Index)
		}
		existing, ok := a.toolCalls[tc.Index]
		if !ok {
			existing = &ToolCall{Type: "function"}
			a.toolCalls[tc.Index] = existing
			a.order = append(a.order, tc.Index)
		}
		if tc.ID != "" {
			existing.ID = tc.ID
		}
		if tc.Type != "" {
			existing.Type = tc.Type
		}
		if tc.Function.Name != "" {
			existing.Function.Name += tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			existing.Function.Arguments += tc.Function.Arguments
		}
	}
	return nil
}

func (a *assembler) result() *ChatResult {
	msg := Message{Role: RoleAssistant, Content: string(a.content)}
	for _, idx := range a.order {
		msg.ToolCalls = append(msg.ToolCalls, *a.toolCalls[idx])
	}
	return &ChatResult{Message: msg, FinishReason: a.finishReason}
}

func validateResult(r *ChatResult) error {
	if r.FinishReason != "stop" && r.FinishReason != "tool_calls" {
		return fmt.Errorf("model response incomplete (finish_reason=%q); no tools executed", r.FinishReason)
	}
	if (r.FinishReason == "tool_calls") != (len(r.Message.ToolCalls) > 0) {
		return fmt.Errorf("model finish reason does not match tool calls")
	}
	seen := make(map[string]bool)
	for _, tc := range r.Message.ToolCalls {
		if tc.ID == "" || seen[tc.ID] || tc.Type != "function" || strings.TrimSpace(tc.Function.Name) == "" {
			return fmt.Errorf("model returned missing, duplicate or invalid tool identity")
		}
		seen[tc.ID] = true
		if !json.Valid([]byte(tc.Function.Arguments)) {
			return fmt.Errorf("model returned incomplete JSON arguments for %s", tc.Function.Name)
		}
	}
	return nil
}
