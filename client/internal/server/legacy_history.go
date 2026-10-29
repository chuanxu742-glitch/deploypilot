package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/deploypilot/deploypilot/internal/agent"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Older versions saved execution audits but no UI session. Import only recorded
// facts; missing intermediate messages and outcomes cannot be reconstructed.
func (s *Server) importLegacyHistory(dir string) {
	paths, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	if len(paths) > 100 {
		paths = paths[:100]
	}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(io.LimitReader(file, 8*1024*1024))
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		var current *taskRecord
		index := 0
		add := func(event agent.Event) {
			if len(current.Events) >= 256 {
				return
			}
			if len(event.Text) > 16384 {
				event.Text = event.Text[:16384]
			}
			current.NextID++
			event.ID = current.NextID
			event.TaskID = current.ID
			current.Events = append(current.Events, event)
		}
		for scanner.Scan() {
			var entry agent.AuditEntry
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			if entry.Kind == "task" {

				index++
				hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", filepath.Base(path), index)))
				current = &taskRecord{ID: fmt.Sprintf("legacy-%x", hash[:8]), Title: entry.Note, StartedAt: entry.Time.UnixMilli(), Phase: "interrupted", Events: []agent.Event{}}
				s.tasks = append(s.tasks, current)
				add(agent.Event{Type: agent.EventStatus, Text: "从旧版执行日志恢复，未记录的过程无法还原。", Timestamp: entry.Time.UnixMilli()})
				continue
			}
			if current == nil {
				continue
			}
			current.EndedAt = entry.Time.UnixMilli()
			switch entry.Kind {
			case "command":
				callID := fmt.Sprintf("legacy-%d", current.NextID)
				add(agent.Event{Type: agent.EventToolCall, Tool: "run_command", Command: entry.Command, CallID: callID, Timestamp: entry.Time.UnixMilli()})
				add(agent.Event{Type: agent.EventOutput, Tool: "run_command", Text: entry.Output, CallID: callID, Timestamp: entry.Time.UnixMilli()})
				add(agent.Event{Type: agent.EventToolDone, Tool: "run_command", ExitCode: entry.ExitCode, Success: entry.ExitCode == 0, CallID: callID, Timestamp: entry.Time.UnixMilli()})
			case "final":
				current.Phase = "finished"
				add(agent.Event{Type: agent.EventStatus, Text: entry.Note, Timestamp: entry.Time.UnixMilli()})
			}
		}
		file.Close()

	}
	sort.SliceStable(s.tasks, func(i, j int) bool { return s.tasks[i].StartedAt > s.tasks[j].StartedAt })
}
