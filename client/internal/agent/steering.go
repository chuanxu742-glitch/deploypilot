package agent

import "context"

// Instruction is an accepted operator message, independent of a viewer socket.
// RequestID is retained in checkpoints to make replay after a crash idempotent.
type Instruction struct {
	RequestID string `json:"requestId"`
	Text      string `json:"text"`
	Kind      string `json:"kind,omitempty"`
	Sequence  int64  `json:"sequence,omitempty"`
}

func (a *Agent) applyInstructions(ctx context.Context) error {
	if a.applyPending == nil || ctx.Err() != nil {
		return ctx.Err()
	}
	return a.applyPending(func(messages []Instruction) *Conversation {
		a.appendInstructions(messages)
		if a.consumedInstructions == nil {
			a.consumedInstructions = make(map[string]bool)
		}
		for _, message := range messages {
			a.consumedInstructions[instructionKey(message)] = true
		}
		return a.Conversation()
	})
}

func (a *Agent) appendInstructions(messages []Instruction) {
	if a.appliedSteers == nil {
		a.appliedSteers = make(map[string]bool)
	}
	for _, message := range messages {
		key := instructionKey(message)
		if a.appliedSteers[key] {
			continue
		}
		a.window.addFollowUp(message.Text)
		a.appliedSteers[key] = true
		a.verification = nil
		a.audit.Append(AuditEntry{Kind: "steer", Note: message.Text})
	}
}

func instructionKey(message Instruction) string {
	if message.Kind == "followup" {
		return "\x00followup:" + message.RequestID
	}
	return message.RequestID
}

// IncludesInstruction means the text is in the saved prompt, not that a model
// boundary has consumed it. Early stop may save initial instructions first.
func (c *Conversation) IncludesInstruction(message Instruction) bool {
	return c != nil && c.appliedSteers[instructionKey(message)]
}

// ConsumedInstruction additionally proves that a complete execution boundary
// delivered the input, rather than only checkpointing it during an early stop.
func (c *Conversation) ConsumedInstruction(message Instruction) bool {
	return c != nil && c.consumedInstructions[instructionKey(message)]
}

func (a *Agent) finalize(event Event) bool {
	if a.tryFinalize != nil {
		return a.tryFinalize(event)
	}
	a.emit(event)
	return true
}
