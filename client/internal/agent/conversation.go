package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/deploypilot/deploypilot/internal/llm"
)

const MaxConversationBytes = 512 * 1024

type savedConversation struct {
	Version              int             `json:"version"`
	TaskID               string          `json:"taskId"`
	Title                string          `json:"title"`
	Prefix               []llm.Message   `json:"prefix"`
	Turns                [][]llm.Message `json:"turns"`
	History              []historyEntry  `json:"history"`
	Omitted              int             `json:"omitted"`
	FollowUps            []string        `json:"followUps"`
	AppliedSteers        map[string]bool `json:"appliedSteers,omitempty"`
	ConsumedInstructions map[string]bool `json:"consumedInstructions,omitempty"`
}

// Encode stores only conversation facts, never executor state or verification
// records. Compaction operates on a deep copy so saving cannot change a run.
func (c *Conversation) Encode(taskID, title string) ([]byte, error) {
	if c == nil || c.window == nil {
		return nil, errors.New("conversation is unavailable")
	}
	w := c.window
	dto := savedConversation{1, taskID, title, w.prefix, w.turns, w.history, w.omitted, w.followUps, c.appliedSteers, c.consumedInstructions}
	data, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxConversationBytes {
		var cloned savedConversation
		if err = json.Unmarshal(data, &cloned); err != nil {
			return nil, err
		}
		dto = cloned
		copyWindow := &contextWindow{prefix: dto.Prefix, turns: dto.Turns, history: dto.History, omitted: dto.Omitted, followUps: dto.FollowUps}
		if _, _, err = copyWindow.prepare("", nil, 384*1024); err != nil {
			return nil, err
		}
		dto.Turns, dto.History, dto.Omitted, dto.FollowUps = copyWindow.turns, copyWindow.history, copyWindow.omitted, copyWindow.followUps
		data, err = json.Marshal(dto)
	}
	if err != nil {
		return nil, err
	}
	if _, err = DecodeConversation(data, taskID, title); err != nil {
		return nil, err
	}
	return data, nil
}

// DecodeConversation rejects corrupt/mismatched state rather than disguising it
// as a new task or silently replacing it with a less complete history summary.
func DecodeConversation(data []byte, taskID, title string) (*Conversation, error) {
	if len(data) > MaxConversationBytes {
		return nil, errors.New("conversation exceeds private storage limit")
	}
	var dto savedConversation
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&dto); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("conversation has trailing data")
	}
	if dto.Version != 1 || dto.TaskID != taskID || dto.Title != title || taskID == "" {
		return nil, errors.New("conversation version or task identity mismatch")
	}
	if len(dto.Prefix) != 3 || dto.Prefix[0].Role != llm.RoleSystem || dto.Prefix[1].Role != llm.RoleSystem || dto.Prefix[2].Role != llm.RoleUser || dto.Prefix[2].Content != title {
		return nil, errors.New("conversation task prefix is invalid")
	}
	for _, m := range dto.Prefix {
		if len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			return nil, errors.New("conversation prefix contains tool messages")
		}
	}
	if len(dto.Turns) > 2048 || len(dto.History) > 1024 || len(dto.FollowUps) > 1024 || len(dto.AppliedSteers) > 1024 || len(dto.ConsumedInstructions) > 1024 || dto.Omitted < 0 {
		return nil, errors.New("conversation exceeds structural bounds")
	}
	for key, consumed := range dto.ConsumedInstructions {
		if consumed && !dto.AppliedSteers[key] {
			return nil, errors.New("consumed instruction is absent from saved prompt")
		}
	}
	for _, turn := range dto.Turns {
		if len(turn) == 0 || len(turn) > 65 {
			return nil, errors.New("conversation turn is invalid")
		}
		calls := map[string]string{}
		answered := map[string]bool{}
		for _, m := range turn {
			if m.Role != llm.RoleAssistant && m.Role != llm.RoleUser && m.Role != llm.RoleTool {
				return nil, errors.New("conversation message role is invalid")
			}
			for _, tc := range m.ToolCalls {
				if m.Role != llm.RoleAssistant || tc.ID == "" || calls[tc.ID] != "" || tc.Type != "function" || tc.Function.Name == "" || !json.Valid([]byte(tc.Function.Arguments)) {
					return nil, errors.New("conversation tool call is invalid")
				}
				calls[tc.ID] = tc.Function.Name
			}
			if m.Role == llm.RoleTool {
				if calls[m.ToolCallID] == "" || answered[m.ToolCallID] || (m.Name != "" && m.Name != calls[m.ToolCallID]) {
					return nil, errors.New("conversation has an unpaired tool result")
				}
				answered[m.ToolCallID] = true
			} else if m.ToolCallID != "" {
				return nil, errors.New("conversation has unexpected tool identity")
			}
		}
		if len(calls) != len(answered) {
			return nil, errors.New("conversation has incomplete tool calls")
		}
	}
	dto.Prefix[0].Content = systemPrompt
	return &Conversation{window: &contextWindow{prefix: dto.Prefix, turns: dto.Turns, history: dto.History, omitted: dto.Omitted, followUps: dto.FollowUps}, appliedSteers: dto.AppliedSteers, consumedInstructions: dto.ConsumedInstructions}, nil
}

// ConversationFromEvents is only for older records with no saved model context.
// Events remain assistant-side historical data, never current execution proof.
func ConversationFromEvents(title string, events []Event) (*Conversation, error) {
	if strings.TrimSpace(title) == "" {
		return nil, fmt.Errorf("historical task has no original instruction")
	}
	w := newContextWindow([]llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}, {Role: llm.RoleSystem, Content: "此对话从历史摘要恢复，旧机器状态和验证均已过时，需要重新检查。"}, {Role: llm.RoleUser, Content: title}})
	if len(events) > 512 {
		events = events[len(events)-512:]
	}
	for _, e := range events {
		if e.Type == EventUser && e.State == "queued" {
			continue
		}
		if e.Text == "" && e.Command == "" && e.Path == "" {
			continue
		}
		w.history = append(w.history, historyEntry{Tool: string(e.Type), Command: clipContext(e.Command, 512), Path: clipContext(e.Path, 512), Note: clipContext(e.Text, 1024)})
	}
	w.boundHistory(8192)
	return &Conversation{window: w}, nil
}
