package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/deploypilot/deploypilot/internal/agent"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// maxStoredEventText bounds one stored event's text.
const maxStoredEventText = 32768

// truncatedMarker matches the web UI's TRUNCATED prefix so it shows the notice.
const truncatedMarker = "…（较早内容已截断）\n"

// keepTailUTF8 keeps the end of s (where command failures appear) within max
// bytes, on a rune boundary, prefixed with a truncation marker.
func keepTailUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := len(s) - (max - len(truncatedMarker))
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return truncatedMarker + s[cut:]
}

// truncateUTF8 shortens s to at most max bytes without splitting a rune, so a
// persisted/streamed event never ends in an invalid byte sequence.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

type taskRecord struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   int64  `json:"endedAt,omitempty"`
	Phase     string `json:"phase"`
	// RequestID is the latest start request accepted for this task.
	RequestID         string                  `json:"requestId,omitempty"`
	Events            []agent.Event           `json:"events"`
	NextID            int64                   `json:"nextId"`
	CanContinue       bool                    `json:"canContinue"`
	ConversationSaved bool                    `json:"conversationSaved,omitempty"`
	PendingSteers     []agent.Instruction     `json:"pendingSteers,omitempty"`
	PendingFollowUps  []agent.Instruction     `json:"pendingFollowUps,omitempty"`
	SteerReceipts     map[string]string       `json:"steerReceipts,omitempty"`
	StartReceipts     map[string]startReceipt `json:"startReceipts,omitempty"`
}

func (s *Server) loadTasks() {
	if s.historyDir == "" {
		return
	}
	if err := os.MkdirAll(s.historyDir, 0700); err != nil {
		s.historyErr = err
		return
	}
	lock, err := lockHistory(filepath.Join(s.historyDir, "session.lock"))
	if err != nil {
		s.historyErr = fmt.Errorf("workspace history is already in use or unavailable: %w", err)
		return
	}
	s.historyLock = lock
	data, err := os.ReadFile(filepath.Join(s.historyDir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		if s.legacyAuditDir != "" {
			s.importLegacyHistory(s.legacyAuditDir)
		}
		for _, task := range s.tasks {
			task.CanContinue = s.canContinueTaskLocked(task)
		}
		s.persistTasksLocked()
		return
	}
	if err != nil {
		s.historyErr = err
		return
	}
	if err = json.Unmarshal(data, &s.tasks); err != nil {
		s.historyErr = err
		return
	}
	if len(s.tasks) > 0 && s.tasks[0].Events == nil {
		restored, readErr := s.readTaskLocked(s.tasks[0])
		if readErr != nil {
			s.historyErr = readErr
			return
		}
		s.tasks[0] = restored
	}
	for _, task := range s.tasks {
		if task.Phase == "running" || task.Phase == "stopping" {
			task.Phase = "interrupted"
			task.EndedAt = time.Now().UnixMilli()
		}
		task.CanContinue = s.canContinueTaskLocked(task)
	}
	s.persistTasksLocked()
}
func (s *Server) persistTasksLocked() {
	if s.historyDir == "" {
		return
	}
	if s.historyErr != nil {
		return
	}
	var err error
	for i, task := range s.tasks {
		if task.Events == nil {
			continue
		}
		var data []byte
		data, err = json.Marshal(task)
		if err == nil {
			err = atomicPrivateWrite(s.taskPath(task.ID), data)
		}
		if err != nil {
			break
		}
		if i > 0 {
			task.Events = nil
		}
	}
	if err == nil {
		var data []byte
		data, err = json.Marshal(s.summariesLocked())
		if err == nil {
			err = atomicPrivateWrite(filepath.Join(s.historyDir, "tasks.json"), data)
		}
	}
	if err != nil {
		s.historyErr = err
		s.broadcastTaskLocked(map[string]string{"type": "error", "text": "历史记录保存失败：" + err.Error()})
	}
}
func (s *Server) summariesLocked() []taskRecord {
	result := make([]taskRecord, 0, len(s.tasks))
	for _, task := range s.tasks {
		copy := *task
		copy.Events = nil
		result = append(result, copy)
	}
	return result
}

// runStateLocked is the phase of the running task, or "idle".
func (s *Server) runStateLocked() string {
	if s.taskCancel != nil && len(s.tasks) > 0 {
		return s.tasks[0].Phase
	}
	return "idle"
}

func (s *Server) snapshotLocked() any {
	state := s.runStateLocked()
	var task *taskRecord
	if len(s.tasks) > 0 {
		task = s.tasks[0]
	}
	snapshot := map[string]any{"type": "snapshot", "state": state, "task": task, "tasks": s.summariesLocked()}
	// Preserve the original single-ID hint for older browsers. New browsers use
	// the per-record capability to continue any saved task.
	if s.taskCancel == nil && task != nil && task.CanContinue {
		snapshot["continuable"] = task.ID
	}
	return snapshot
}

// runStateMessage ties a lifecycle update to its task when one exists. Idle
// notifications from an empty server intentionally have no task identity.
func runStateMessage(state string, task *taskRecord) map[string]any {
	message := map[string]any{"type": "run_state", "state": state}
	if task != nil {
		message["taskId"] = task.ID
		if task.RequestID != "" {
			message["requestId"] = task.RequestID
		}
	}
	return message
}
func (s *Server) broadcastTaskLocked(value any) {
	data, _ := json.Marshal(value)
	for ch := range s.taskSubscribers {
		select {
		case ch <- data:
		default:
			delete(s.taskSubscribers, ch)
			close(ch)
		}
	}
}
func (s *Server) subscribeTasks() (<-chan []byte, func()) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	ch := make(chan []byte, 256)
	data, _ := json.Marshal(s.snapshotLocked())
	ch <- data
	s.taskSubscribers[ch] = struct{}{}
	if s.historyErr != nil {
		data, _ = json.Marshal(map[string]string{"type": "error", "text": "历史记录读取或保存失败：" + s.historyErr.Error()})
		ch <- data
	}
	return ch, func() {
		s.taskMu.Lock()
		defer s.taskMu.Unlock()
		if _, ok := s.taskSubscribers[ch]; ok {
			delete(s.taskSubscribers, ch)
			close(ch)
		}
	}
}
func (s *Server) emitTask(task *taskRecord, event agent.Event) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	s.broadcastTaskLocked(s.appendEventLocked(task, event))
}

// appendEventLocked stores event on task (numbered, bounded, merged with the
// previous streaming delta) and schedules persistence. It returns the event as
// numbered, for broadcasting. Caller holds taskMu.
func (s *Server) appendEventLocked(task *taskRecord, event agent.Event) agent.Event {
	task.NextID++
	event.ID = task.NextID
	event.TaskID = task.ID
	if event.Timestamp == 0 {
		event.Timestamp = time.Now().UnixMilli()
	}
	if len(event.Text) > maxStoredEventText {
		event.Text = truncateUTF8(event.Text, maxStoredEventText)
	}
	// Streaming arrives as many small deltas. Store consecutive deltas of the
	// same kind (and same command, for output) as one event, so a reloaded or
	// finished task shows whole messages instead of one fragment per line, and
	// fragments cannot crowd tool calls out of the bounded history. Live
	// viewers still receive every delta below.
	merged := false
	if n := len(task.Events); n > 0 && event.Type == agent.EventActivity && event.State == "waiting" && event.CallID != "" {
		last := &task.Events[n-1]
		if last.Type == agent.EventActivity && last.State == "waiting" && last.CallID == event.CallID {
			task.Events[n-1] = event
			merged = true
		}
	}
	if n := len(task.Events); n > 0 && (event.Type == agent.EventThinking || event.Type == agent.EventOutput) {
		if last := &task.Events[n-1]; !merged && last.Type == event.Type && last.CallID == event.CallID {
			last.Text = keepTailUTF8(last.Text+event.Text, maxStoredEventText)
			merged = true
		}
	}
	if !merged {
		task.Events = append(task.Events, event)
	}
	trimTaskEvents(task)
	if s.historyTimer == nil {
		s.historyTimer = time.AfterFunc(200*time.Millisecond, func() { s.taskMu.Lock(); defer s.taskMu.Unlock(); s.persistTasksLocked(); s.historyTimer = nil })
	}
	return event
}

// reopenTaskLocked moves the task to the front of the history with its events
// loaded and marks it running again. Caller holds taskMu.
func (s *Server) reopenTaskLocked(id string) (*taskRecord, error) {
	for i, task := range s.tasks {
		if task.ID != id {
			continue
		}
		record, err := s.readTaskLocked(task)
		if err != nil {
			return nil, err
		}
		if record.Events == nil {
			record.Events = []agent.Event{}
		}
		record.Phase, record.EndedAt = "running", 0
		s.tasks = append([]*taskRecord{record}, append(s.tasks[:i:i], s.tasks[i+1:]...)...)
		return record, nil
	}
	return nil, errors.New("task not found")
}

// startTask starts a task (or continues continueFrom) and returns a rejection
// reason, or "" on success. A non-empty requestID is recorded on the task so a
// reconnecting client can confirm its start was accepted.
func (s *Server) startTask(text, owner, continueFrom, requestID string) string {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if !validStartRequestID(requestID) {
		return "任务请求编号无效，请重新提交。"
	}
	if _, receipt, found := s.findStartReceiptLocked(requestID); found {
		if !receipt.Legacy && (receipt.ContentHash != steerDigest(text) || receipt.ContinueFrom != continueFrom) {
			return "该请求编号已用于不同内容或会话归属，不能重复使用。"
		}
		return "此请求已接受，不会重放，请核对历史。"
	}
	if s.taskCancel != nil {
		return "已有任务在运行，请先停止。"
	}
	if strings.TrimSpace(text) == "" {
		return "任务内容不能为空。"
	}
	if len(text) > maxTaskTextBytes {
		return "任务内容不能超过64KiB。"
	}
	var resume *agent.Conversation
	var summaryRecovery bool
	if continueFrom != "" {
		var err error
		resume, summaryRecovery, err = s.resumeConversationLocked(continueFrom)
		if err != nil {
			return "无法继续：" + err.Error()
		}
		for _, record := range s.tasks {
			if record.ID == continueFrom {
				if requestID != "" && startReceiptCount(record) >= maxStartReceipts {
					return "当前会话的接受收据已达上限，不能再接受新的请求编号。"
				}
				bytes := len(text)
				for _, pending := range record.PendingFollowUps {
					bytes += len(pending.Text)
				}
				if len(record.PendingFollowUps) >= maxPendingFollowUps || bytes > maxPendingFollowUpBytes {
					return "未交付的续聊内容超过保存上限，请缩短本次补充。"
				}
				break
			}
		}
	}
	if !s.llm.HasKey() {
		return "尚未配置模型 API Key，请先在设置中填写。"
	}
	if s.historyErr != nil {
		return "历史记录不可写，请先检查本机存储：" + s.historyErr.Error()
	}
	releaseSession := func() {}
	if s.auth != nil && owner != "" {
		var ok bool
		releaseSession, ok = s.auth.HoldSession(owner)
		if !ok {
			return "会话已失效，请重新配对。"
		}
	}
	if !s.acquireAgent() {
		releaseSession()
		return "已有任务在运行或清理，请稍后重试。"
	}
	var task *taskRecord
	var followUpID string
	var initialInstructions []agent.Instruction
	if resume != nil {
		// A follow-up stays in the same session: same record, same history
		// entry, with the operator's message added to its timeline.
		var err error
		if task, err = s.reopenTaskLocked(continueFrom); err != nil {
			s.releaseAgent()
			releaseSession()
			return "无法继续：任务记录读取失败：" + err.Error()
		}
		task.CanContinue = false
		initialInstructions = pendingInputs(task)
		if summaryRecovery {
			s.appendEventLocked(task, agent.Event{Type: agent.EventStatus, Text: "历史摘要恢复，需要重新检查当前机器与部署结果；旧验证不能作为本轮证据。"})
		}
		followUpID = fmt.Sprintf("followup-%d", task.NextID+1)
		event := s.appendEventLocked(task, agent.Event{Type: agent.EventUser, Text: text, RequestID: followUpID})
		task.PendingFollowUps = append(task.PendingFollowUps, agent.Instruction{RequestID: followUpID, Text: text, Kind: "followup", Sequence: event.ID})
	} else {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			s.releaseAgent()
			releaseSession()
			return err.Error()
		}
		task = &taskRecord{ID: hex.EncodeToString(token), Title: text, StartedAt: time.Now().UnixMilli(), Phase: "running", Events: []agent.Event{}}
		s.tasks = append([]*taskRecord{task}, s.tasks...)
	}
	previousRequestID := task.RequestID
	rememberStart(task, requestID, text, continueFrom)
	if requestID != "" {
		task.RequestID = requestID
	}

	ctx, cancel := context.WithCancel(s.baseCtx)
	s.taskCancel = cancel
	s.taskAcceptingSteers = true
	if s.auth != nil && owner != "" {
		go func() {
			ticker := time.NewTicker(s.sessionCheckInterval())
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !s.auth.IsValid(owner) {
						cancel()
						return
					}
				}
			}
		}()
	}
	s.persistTasksLocked()
	if s.historyErr != nil {
		task.RequestID = previousRequestID
		if requestID != "" {
			delete(task.StartReceipts, requestID)
		}
		cancel()
		s.taskCancel = nil
		task.Phase = "interrupted"
		task.EndedAt = time.Now().UnixMilli()
		s.releaseAgent()
		releaseSession()
		return "任务未启动，历史记录保存失败：" + s.historyErr.Error()
	}
	s.broadcastTaskLocked(s.snapshotLocked())
	s.broadcastTaskLocked(runStateMessage("running", task))
	s.taskWG.Add(1)
	go func() {
		defer s.taskWG.Done()
		defer releaseSession()
		auditor := s.newAuditor()
		ag := agent.New(agent.Config{LLM: s.llm.Snapshot(), Executor: s.newExecutor(), Auditor: auditor, ContextBytes: s.agentContextBytes, MaxSteps: s.agentMaxSteps, Resume: resume, Emit: func(e agent.Event) { s.emitTask(task, e) },
			ApplyPending: func(apply func([]agent.Instruction) *agent.Conversation) error {
				return s.applySteers(ctx, task, apply)
			},
			TryFinalize:         func(e agent.Event) bool { return s.tryFinalizeTask(ctx, task, e) },
			InitialInstructions: initialInstructions,
			FollowUpID:          followUpID,
		})
		_ = ag.Run(ctx, text)
		_ = auditor.Close()
		s.taskMu.Lock()
		defer s.taskMu.Unlock()
		cancel()
		s.taskCancel = nil
		s.taskAcceptingSteers = false
		for _, pending := range task.PendingSteers {
			s.broadcastTaskLocked(s.appendEventLocked(task, agent.Event{Type: agent.EventActivity, State: "steer_deferred", RequestID: pending.RequestID, Text: "补充尚未应用，手动继续此任务后处理。"}))
		}
		s.lastConversation, s.lastConvTaskID = ag.Conversation(), task.ID
		if err := s.saveConversationLocked(task, ag.Conversation()); err != nil {
			s.historyErr = err
			s.broadcastTaskLocked(s.appendEventLocked(task, agent.Event{Type: agent.EventError, Text: "对话上下文保存失败：" + err.Error()}))
		}
		if s.historyErr == nil {
			reconcileCheckpoint(s, task, ag.Conversation())
		}
		task.Phase = "finished"
		if s.baseCtx.Err() != nil {
			task.Phase = "interrupted"
		}
		task.EndedAt = time.Now().UnixMilli()
		task.CanContinue = s.historyErr == nil && s.canContinueTaskLocked(task)
		s.releaseAgent()
		s.persistTasksLocked()
		s.broadcastTaskLocked(s.snapshotLocked())
		s.broadcastTaskLocked(runStateMessage("idle", task))
	}()
	return ""
}
func (s *Server) stopTask() {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	state := "idle"
	var task *taskRecord
	if s.taskCancel != nil {
		s.taskAcceptingSteers = false
		s.taskCancel()
		s.markAgentStopping()
		if len(s.tasks) > 0 {
			task = s.tasks[0]
			task.Phase = "stopping"
		}
		state = "stopping"
		s.persistTasksLocked()
	}
	s.broadcastTaskLocked(runStateMessage(state, task))
}
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Marshal the response under taskMu (fast, in-memory) but write it to the
	// possibly slow network connection without the lock, so a slow client
	// cannot stall emitTask (agent output) or terminal viewers.
	var payload []byte
	if r.URL.Path == "/api/tasks" {
		s.taskMu.Lock()
		payload, _ = json.Marshal(s.summariesLocked())
		s.taskMu.Unlock()
	} else {
		id := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		s.taskMu.Lock()
		var record *taskRecord
		var err error
		for _, task := range s.tasks {
			if task.ID == id {
				record, err = s.readTaskLocked(task)
				break
			}
		}
		if record != nil && err == nil {
			record.CanContinue = s.canContinueTaskLocked(record)
			payload, err = json.Marshal(record)
		}
		s.taskMu.Unlock()
		if err != nil {
			http.Error(w, "task history unavailable", 500)
			return
		}
		if record == nil {
			http.Error(w, "task not found", 404)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(payload)
}

func (s *Server) taskPath(id string) string { return filepath.Join(s.historyDir, "task-"+id+".json") }
func (s *Server) readTaskLocked(task *taskRecord) (*taskRecord, error) {
	if err := validateTaskRecord(task, task.ID, task.Title); err != nil {
		return nil, err
	}
	if task.Events != nil {
		return task, nil
	}
	if filepath.Base(task.ID) != task.ID || strings.ContainsAny(task.ID, "/\\") {
		return nil, errors.New("invalid history task id")
	}
	data, err := readBoundedPrivate(s.taskPath(task.ID), 4*1024*1024)
	if err != nil {
		return nil, err
	}
	var record taskRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if err := validateTaskRecord(&record, task.ID, task.Title); err != nil {
		return nil, err
	}
	if record.Phase == "running" || record.Phase == "stopping" {
		if task.Phase == "interrupted" {
			record.Phase, record.EndedAt = task.Phase, task.EndedAt
		}
	}
	return &record, nil
}
