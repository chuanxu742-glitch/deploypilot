package llm

// streamChunk is one SSE "data:" event from /chat/completions with stream=true.
type streamChunk struct {
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

func (a *assembler) add(d deltaMsg) {
	if d.Content != "" {
		a.content = append(a.content, d.Content...)
	}
	for _, tc := range d.ToolCalls {
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
}

func (a *assembler) result() *ChatResult {
	msg := Message{Role: RoleAssistant, Content: string(a.content)}
	for _, idx := range a.order {
		msg.ToolCalls = append(msg.ToolCalls, *a.toolCalls[idx])
	}
	fr := a.finishReason
	if fr == "" {
		if len(msg.ToolCalls) > 0 {
			fr = "tool_calls"
		} else {
			fr = "stop"
		}
	}
	return &ChatResult{Message: msg, FinishReason: fr}
}
