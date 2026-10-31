package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/deploypilot/deploypilot/internal/agent"
)

const (
	maxQueuedSteers         = 16
	maxQueuedSteerBytes     = 64 * 1024
	maxSteerTextBytes       = 16 * 1024
	maxSteerReceipts        = 1024
	maxPendingFollowUps     = 16
	maxPendingFollowUpBytes = 256 * 1024
)

func steerDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// queueSteer persists the receipt before acknowledging acceptance. A viewer
// disconnect never removes it; only an explicit safe agent boundary consumes it.
func (s *Server) queueSteer(taskID, text, requestID string) string {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if requestID == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "\x00\r\n") {
		return "补充请求编号无效。"
	}
	if strings.TrimSpace(text) == "" || len(text) > maxSteerTextBytes {
		return "补充内容不能为空，且不能超过16KiB。"
	}
	var task *taskRecord
	for _, candidate := range s.tasks {
		if candidate.ID == taskID {
			task = candidate
			break
		}
	}
	if task == nil {
		return "补充消息所属任务不存在。"
	}
	if s.historyErr != nil {
		return "历史记录不可写，补充未接受。"
	}
	if previous, ok := task.SteerReceipts[requestID]; ok {
		if previous != steerDigest(text) {
			return "同一补充请求编号不能用于不同内容。"
		}
		return ""
	}
	if s.taskCancel == nil || len(s.tasks) == 0 || s.tasks[0].ID != taskID || task.Phase != "running" || !s.taskAcceptingSteers {
		return "任务已结束或正在停止，补充未发送；请手动继续此任务。"
	}
	if s.historyErr != nil {
		return "历史记录不可写，补充未接受。"
	}
	queuedBytes := len(text)
	for _, pending := range task.PendingSteers {
		queuedBytes += len(pending.Text)
	}
	if len(task.PendingSteers) >= maxQueuedSteers || queuedBytes > maxQueuedSteerBytes {
		return "等待应用的补充过多，请等待当前执行步骤结束。"
	}
	if len(task.SteerReceipts) >= maxSteerReceipts {
		return "当前会话补充记录已达上限，请新建任务。"
	}
	if task.SteerReceipts == nil {
		task.SteerReceipts = make(map[string]string)
	}
	task.SteerReceipts[requestID] = steerDigest(text)
	task.PendingSteers = append(task.PendingSteers, agent.Instruction{RequestID: requestID, Text: text, Sequence: task.NextID + 1})
	event := s.appendEventLocked(task, agent.Event{Type: agent.EventUser, Text: text, RequestID: requestID, State: "queued"})
	s.persistTasksLocked()
	if s.historyErr != nil {
		return "补充记录保存失败，未确认接受：" + s.historyErr.Error()
	}
	s.broadcastTaskLocked(event)
	return ""
}

// applySteers and tryFinalizeTask share taskMu with queueSteer/stopTask. This
// makes a final reply either precede a rejection or defer to every accepted
// instruction, never acknowledge a message into an already completed run.
func (s *Server) applySteers(ctx context.Context, task *taskRecord, apply func([]agent.Instruction) *agent.Conversation) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.historyErr != nil {
		return s.historyErr
	}
	if task.Phase != "running" || !s.taskAcceptingSteers {
		return errors.New("task is no longer accepting instructions")
	}
	if len(task.PendingSteers) == 0 && len(task.PendingFollowUps) == 0 {
		return nil
	}
	checkpoint := apply(pendingInputs(task))
	if err := s.saveConversationLocked(task, checkpoint); err != nil {
		s.historyErr = err
		return err
	}
	pending := task.PendingSteers
	task.PendingSteers = nil
	task.PendingFollowUps = nil
	for _, message := range pending {
		s.broadcastTaskLocked(s.appendEventLocked(task, agent.Event{Type: agent.EventActivity, State: "steer_applied", RequestID: message.RequestID, Text: "补充已应用；后续执行按更新后的要求继续。"}))
	}
	s.persistTasksLocked()
	return s.historyErr
}

func (s *Server) tryFinalizeTask(ctx context.Context, task *taskRecord, event agent.Event) bool {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if ctx.Err() != nil || task.Phase != "running" || !s.taskAcceptingSteers || len(task.PendingSteers) > 0 || len(task.PendingFollowUps) > 0 {
		return false
	}
	s.taskAcceptingSteers = false
	s.broadcastTaskLocked(s.appendEventLocked(task, event))
	return true
}

// Unconsumed receipts remain visible even when a long step produces enough
// progress to evict ordinary history. Reserve their share of the same limits.
func trimTaskEvents(task *taskRecord) {
	weight := func(e agent.Event) int { return len(e.Text) + len(e.Command) + len(e.Path) + len(e.RequestID) + 256 }
	bytes := 0
	for _, e := range task.Events {
		bytes += weight(e)
	}
	if len(task.Events) <= 512 && bytes <= 512*1024 {
		return
	}
	pending := make(map[string]bool, len(task.PendingSteers))
	for _, p := range task.PendingSteers {
		pending[p.RequestID] = true
	}
	for _, p := range task.PendingFollowUps {
		pending[p.RequestID] = true
	}
	protected := func(e agent.Event) bool {
		return e.Type == agent.EventUser && pending[e.RequestID]
	}
	remainingCount, remainingBytes := 512, 512*1024
	for _, e := range task.Events {
		if protected(e) {
			remainingCount--
			remainingBytes -= weight(e)
		}
	}
	kept := make([]agent.Event, 0, min(512, len(task.Events)))
	for i := len(task.Events) - 1; i >= 0; i-- {
		e := task.Events[i]
		if protected(e) {
			kept = append(kept, e)
		} else if remainingCount > 0 && weight(e) <= remainingBytes {
			kept = append(kept, e)
			remainingCount--
			remainingBytes -= weight(e)
		}
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	task.Events = kept
}

// Both input kinds share chronological order. Older files have no sequence on
// steers, so their protected event IDs provide the equivalent stable ordering.
func pendingInputs(task *taskRecord) []agent.Instruction {
	inputs := append(append([]agent.Instruction(nil), task.PendingSteers...), task.PendingFollowUps...)
	for i := range inputs {
		if inputs[i].Sequence == 0 {
			for _, e := range task.Events {
				if e.Type == agent.EventUser && e.RequestID == inputs[i].RequestID {
					inputs[i].Sequence = e.ID
					break
				}
			}
		}
	}
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].Sequence < inputs[j].Sequence })
	return inputs
}
