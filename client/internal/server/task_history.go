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
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	StartedAt int64         `json:"startedAt"`
	EndedAt   int64         `json:"endedAt,omitempty"`
	Phase     string        `json:"phase"`
	Events    []agent.Event `json:"events"`
	NextID    int64         `json:"nextId"`
	// ContinuedFrom is the task whose conversation this run resumed.
	ContinuedFrom string `json:"continuedFrom,omitempty"`
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
func (s *Server) snapshotLocked() any {
	state := "idle"
	var task *taskRecord
	if len(s.tasks) > 0 {
		task = s.tasks[0]
		if s.taskCancel != nil {
			state = task.Phase
		}
	}
	snapshot := map[string]any{"type": "snapshot", "state": state, "task": task, "tasks": s.summariesLocked()}
	// The only task whose in-memory conversation can be continued.
	if s.taskCancel == nil && s.lastConversation != nil {
		snapshot["continuable"] = s.lastConvTaskID
	}
	return snapshot
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
	task.NextID++
	event.ID = task.NextID
	event.TaskID = task.ID
	if len(event.Text) > maxStoredEventText {
		event.Text = truncateUTF8(event.Text, maxStoredEventText)
	}
	// Streaming arrives as many small deltas. Store consecutive deltas of the
	// same kind (and same command, for output) as one event, so a reloaded or
	// finished task shows whole messages instead of one fragment per line, and
	// fragments cannot crowd tool calls out of the bounded history. Live
	// viewers still receive every delta below.
	merged := false
	if n := len(task.Events); n > 0 && (event.Type == agent.EventThinking || event.Type == agent.EventOutput) {
		if last := &task.Events[n-1]; last.Type == event.Type && last.CallID == event.CallID {
			last.Text = keepTailUTF8(last.Text+event.Text, maxStoredEventText)
			merged = true
		}
	}
	if !merged {
		task.Events = append(task.Events, event)
	}
	if len(task.Events) > 512 {
		task.Events = task.Events[len(task.Events)-512:]
	}
	bytes := 0
	for i := len(task.Events) - 1; i >= 0; i-- {
		bytes += len(task.Events[i].Text) + len(task.Events[i].Command) + len(task.Events[i].Path) + 256
		if bytes > 512*1024 {
			task.Events = task.Events[i+1:]
			break
		}
	}
	if s.historyTimer == nil {
		s.historyTimer = time.AfterFunc(200*time.Millisecond, func() { s.taskMu.Lock(); defer s.taskMu.Unlock(); s.persistTasksLocked(); s.historyTimer = nil })
	}
	s.broadcastTaskLocked(event)
}
func (s *Server) startTask(text, owner, continueFrom string) string {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.taskCancel != nil {
		return "已有任务在运行，请先停止。"
	}
	if strings.TrimSpace(text) == "" {
		return "任务内容不能为空。"
	}
	var resume *agent.Conversation
	if continueFrom != "" {
		if s.lastConversation == nil || continueFrom != s.lastConvTaskID {
			return "无法继续：只能继续最近结束的一个任务，且上下文只保存在内存中，程序重启后不保留。请新建任务。"
		}
		resume = s.lastConversation
	}
	if !s.llm.HasKey() {
		return "尚未配置模型 API Key，请先在设置中填写。"
	}
	if s.historyErr != nil {
		return "历史记录不可写，请先检查本机存储：" + s.historyErr.Error()
	}
	if !s.acquireAgent() {
		return "已有任务在运行或清理，请稍后重试。"
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		s.releaseAgent()
		return err.Error()
	}
	task := &taskRecord{ID: hex.EncodeToString(token), Title: text, StartedAt: time.Now().UnixMilli(), Phase: "running", Events: []agent.Event{}, ContinuedFrom: continueFrom}
	s.tasks = append([]*taskRecord{task}, s.tasks...)

	ctx, cancel := context.WithCancel(s.baseCtx)
	s.taskCancel = cancel
	if s.auth != nil && owner != "" {
		go func() {
			interval := s.auth.IdleTimeout() / 4
			if interval > time.Second {
				interval = time.Second
			}
			if interval <= 0 {
				interval = time.Millisecond
			}
			ticker := time.NewTicker(interval)
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
		cancel()
		s.taskCancel = nil
		task.Phase = "interrupted"
		task.EndedAt = time.Now().UnixMilli()
		s.releaseAgent()
		return "任务未启动，历史记录保存失败：" + s.historyErr.Error()
	}
	s.broadcastTaskLocked(s.snapshotLocked())
	s.broadcastTaskLocked(map[string]string{"type": "run_state", "state": "running"})
	s.taskWG.Add(1)
	go func() {
		defer s.taskWG.Done()
		auditor := s.newAuditor()
		ag := agent.New(agent.Config{LLM: s.llm.Snapshot(), Executor: s.newExecutor(), Auditor: auditor, ContextBytes: s.agentContextBytes, MaxSteps: s.agentMaxSteps, Resume: resume, Emit: func(e agent.Event) { s.publishTerminalAgent(e); s.emitTask(task, e) }})
		_ = ag.Run(ctx, text)
		_ = auditor.Close()
		s.taskMu.Lock()
		defer s.taskMu.Unlock()
		cancel()
		s.taskCancel = nil
		// Keep only this run's conversation; an earlier one can no longer be resumed.
		s.lastConversation, s.lastConvTaskID = ag.Conversation(), task.ID
		task.Phase = "finished"
		if s.baseCtx.Err() != nil {
			task.Phase = "interrupted"
		}
		task.EndedAt = time.Now().UnixMilli()
		s.releaseAgent()
		s.persistTasksLocked()
		s.broadcastTaskLocked(s.snapshotLocked())
		s.broadcastTaskLocked(map[string]string{"type": "run_state", "state": "idle"})
	}()
	return ""
}
func (s *Server) stopTask() {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	state := "idle"
	if s.taskCancel != nil {
		s.taskCancel()
		s.markAgentStopping()
		s.tasks[0].Phase = "stopping"
		state = "stopping"
		s.persistTasksLocked()
	}
	s.broadcastTaskLocked(map[string]string{"type": "run_state", "state": state})
}
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
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
	if task.Events != nil {
		return task, nil
	}
	if filepath.Base(task.ID) != task.ID || strings.ContainsAny(task.ID, "/\\") {
		return nil, errors.New("invalid history task id")
	}
	data, err := os.ReadFile(s.taskPath(task.ID))
	if err != nil {
		return nil, err
	}
	var record taskRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}
