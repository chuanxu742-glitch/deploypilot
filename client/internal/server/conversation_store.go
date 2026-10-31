package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deploypilot/deploypilot/internal/agent"
)

func readBoundedPrivate(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("private history exceeds %d bytes", limit)
	}
	return data, nil
}

func (s *Server) conversationPath(id string) string {
	return filepath.Join(s.historyDir, "conversation-"+id+".json")
}

func (s *Server) saveConversationLocked(task *taskRecord, c *agent.Conversation) error {
	if c == nil {
		return nil
	}
	data, err := c.Encode(task.ID, task.Title)
	if err != nil {
		return err
	}
	if s.historyDir != "" {
		if err = atomicPrivateWrite(s.conversationPath(task.ID), data); err != nil {
			return err
		}
	} else {
		if s.conversations == nil {
			s.conversations = make(map[string]*agent.Conversation)
		}
		s.conversations[task.ID] = c
	}
	task.ConversationSaved = true
	return nil
}

func (s *Server) resumeConversationLocked(id string) (*agent.Conversation, bool, error) {
	for index, task := range s.tasks {
		if task.ID != id {
			continue
		}
		record, err := s.readTaskLocked(task)
		if err != nil {
			return nil, false, err
		}
		if s.historyDir != "" {
			data, readErr := readBoundedPrivate(s.conversationPath(id), agent.MaxConversationBytes)
			if readErr == nil {
				c, err := agent.DecodeConversation(data, id, record.Title)
				if err == nil && reconcileCheckpoint(s, record, c) {
					s.tasks[index] = record
					s.persistTasksLocked()
					if s.historyErr != nil {
						return nil, false, s.historyErr
					}
				}
				return c, false, err
			}
			if !errors.Is(readErr, os.ErrNotExist) || record.ConversationSaved || task.ConversationSaved {
				return nil, false, fmt.Errorf("saved conversation unavailable: %w", readErr)
			}
		} else if c := s.conversations[id]; c != nil {
			if reconcileCheckpoint(s, record, c) {
				s.tasks[index] = record
			}
			return c, false, nil
		} else if record.ConversationSaved {
			return nil, false, errors.New("saved conversation is missing")
		}
		pendingIDs := map[string]bool{}
		for _, message := range pendingInputs(record) {
			pendingIDs[message.RequestID] = true
		}
		var historical []agent.Event
		for _, event := range record.Events {
			if event.Type != agent.EventUser || !pendingIDs[event.RequestID] {
				historical = append(historical, event)
			}
		}
		c, err := agent.ConversationFromEvents(record.Title, historical)
		return c, true, err
	}
	return nil, false, errors.New("task not found")
}

func validateTaskRecord(record *taskRecord, id, title string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, "/\\") || record.ID != id || record.Title != title {
		return errors.New("history task identity mismatch")
	}
	if len(record.Events) > 1024 || len(record.PendingSteers) > maxQueuedSteers || len(record.SteerReceipts) > maxSteerReceipts || len(record.PendingFollowUps) > maxPendingFollowUps || len(record.StartReceipts) > maxStartReceipts {
		return errors.New("history task exceeds structural bounds")
	}
	for id, receipt := range record.StartReceipts {
		if !validStartReceipt(id, receipt) {
			return errors.New("history start receipt is corrupt")
		}
	}
	for request, digest := range record.SteerReceipts {
		if request == "" || len(request) > 128 || len(digest) != 64 {
			return errors.New("history instruction receipt is corrupt")
		}
	}
	for _, e := range record.Events {
		if e.TaskID != "" && e.TaskID != id {
			return errors.New("history event belongs to a different task")
		}
	}
	total := 0
	seen := map[string]bool{}
	for _, pending := range record.PendingSteers {
		if pending.RequestID == "" || len(pending.RequestID) > 128 || len(pending.Text) > maxSteerTextBytes || strings.TrimSpace(pending.Text) == "" || seen[pending.RequestID] || record.SteerReceipts[pending.RequestID] != steerDigest(pending.Text) || pending.Kind != "" || pending.Sequence < 0 {
			return errors.New("history queued instruction is corrupt")
		}
		seen[pending.RequestID] = true
		total += len(pending.Text)
	}
	if total > maxQueuedSteerBytes {
		return errors.New("history instruction queue exceeds byte limit")
	}
	total = 0
	seen = map[string]bool{}
	for _, pending := range record.PendingFollowUps {
		if pending.Kind != "followup" || pending.RequestID == "" || len(pending.RequestID) > 128 || len(pending.Text) > 64*1024 || strings.TrimSpace(pending.Text) == "" || seen[pending.RequestID] || pending.Sequence <= 0 {
			return errors.New("history continuation journal is corrupt")
		}
		found := false
		for _, e := range record.Events {
			if e.Type == agent.EventUser && e.RequestID == pending.RequestID && e.ID == pending.Sequence && e.Text == truncateUTF8(pending.Text, maxStoredEventText) {
				found = true
				break
			}
		}
		if record.Events != nil && !found {
			return errors.New("history continuation is missing its accepted user event")
		}
		seen[pending.RequestID] = true
		total += len(pending.Text)
	}
	if total > maxPendingFollowUpBytes {
		return errors.New("history continuation journal exceeds byte limit")
	}
	return nil
}

// A checkpoint can precede its task receipt across a crash. Prompt inclusion
// delivers ordinary followups; only the explicit boundary marker proves steer
// application, so early-stop initial instructions remain honestly deferred.
func reconcileCheckpoint(s *Server, task *taskRecord, c *agent.Conversation) bool {
	if c == nil {
		return false
	}
	changed := false
	var follows []agent.Instruction
	for _, message := range task.PendingFollowUps {
		if c.IncludesInstruction(message) {
			changed = true
		} else {
			follows = append(follows, message)
		}
	}
	task.PendingFollowUps = follows
	var steers []agent.Instruction
	for _, message := range task.PendingSteers {
		if !c.ConsumedInstruction(message) {
			steers = append(steers, message)
			continue
		}
		changed = true
		known := false
		for _, e := range task.Events {
			known = known || e.RequestID == message.RequestID && e.State == "steer_applied"
		}
		if !known {
			s.broadcastTaskLocked(s.appendEventLocked(task, agent.Event{Type: agent.EventActivity, State: "steer_applied", RequestID: message.RequestID, Text: "已从保存的模型执行边界恢复补充应用记录。"}))
		}
	}
	task.PendingSteers = steers
	return changed
}

func (s *Server) canContinueTaskLocked(task *taskRecord) bool {
	if task.Phase == "running" || task.Phase == "stopping" || s.historyErr != nil {
		return false
	}
	_, _, err := s.resumeConversationLocked(task.ID)
	return err == nil
}
