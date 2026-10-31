package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

type endedCommandExecutor struct {
	*fakeExec
	err error
}

func (e endedCommandExecutor) Run(context.Context, string, time.Duration, func(string)) (int, string, error) {
	return -1, "partial output\n[full output: C:/logs/command.log]", e.err
}

func TestCommandEndStatesAndTimeoutMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, state, display string
		err                  error
		seconds              int
	}{
		{"cancelled", "cancelled", "命令已取消", fmt.Errorf("command cancelled: %w", context.Canceled), 0},
		{"timeout", "timeout", "17 秒", fmt.Errorf("command cancelled: %w", context.DeadlineExceeded), 17},
		{"failed", "", "exit=-1", nil, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			emit, events, _ := collectEvents()
			a := New(Config{Executor: endedCommandExecutor{newFakeExec(), tc.err}, Emit: emit})
			done, result := a.runTool(context.Background(), 1, llm.ToolCall{ID: "probe", Function: llm.FunctionCall{
				Name: toolRunCommand, Arguments: fmt.Sprintf(`{"command":"test command","timeout_seconds":%d}`, tc.seconds),
			}})
			if done || !strings.Contains(result.Content, "exit=-1") || !strings.Contains(result.Content, "partial output") {
				t.Fatalf("model lost diagnostic: done=%v result=%s", done, result.Content)
			}
			var call, end, failure map[string]any
			for _, event := range *events {
				encoded, _ := json.Marshal(event)
				var wire map[string]any
				_ = json.Unmarshal(encoded, &wire)
				switch event.Type {
				case EventToolCall:
					call = wire
				case EventToolDone:
					end = wire
				case EventError:
					failure = wire
				}
			}
			wantSeconds := tc.seconds
			if wantSeconds == 0 {
				wantSeconds = 600
			}
			if call["timeoutSeconds"] != float64(wantSeconds) {
				t.Fatalf("timeout metadata = %v", call)
			}
			if end["logPath"] != "C:/logs/command.log" || end["success"] != false {
				t.Fatalf("completion metadata = %v", end)
			}
			if tc.state != "" && (end["state"] != tc.state || failure["state"] != tc.state) {
				t.Fatalf("end=%v failure=%v", end, failure)
			}
			if !strings.Contains(fmt.Sprint(failure["text"]), tc.display) {
				t.Fatalf("unexpected display = %v", failure)
			}
		})
	}
}
