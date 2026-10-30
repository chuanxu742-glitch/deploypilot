package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestStartRequestIDIsConfirmedInLiveStateSnapshotAndHistory(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test"})
	srv.newExecutor = func() agent.Executor { return staticInfoExecutor{} }

	conn := agentSocket(t, srv, cookie)
	if err := conn.WriteJSON(agentClientMsg{Type: "start", Task: "verify request receipt", RequestID: "request-live-1"}); err != nil {
		t.Fatal(err)
	}
	running := readAgentMessage(t, conn)
	if running["type"] != "run_state" || running["state"] != "running" || running["requestId"] != "request-live-1" || running["taskId"] == "" {
		t.Fatalf("start was not confirmed with request and task identity: %v", running)
	}
	taskID := running["taskId"]
	for {
		event := readAgentMessage(t, conn)
		if event["type"] == "run_state" && event["state"] == "idle" {
			if event["taskId"] != taskID {
				t.Fatalf("idle state lost task identity: %v", event)
			}
			break
		}
	}

	snapshot := latestSnapshot(t, srv, cookie)
	if got := snapshot["task"].(map[string]any)["requestId"]; got != "request-live-1" {
		t.Fatalf("reconnect snapshot requestId = %v", got)
	}
	if got := snapshot["tasks"].([]any)[0].(map[string]any)["requestId"]; got != "request-live-1" {
		t.Fatalf("history summary requestId = %v", got)
	}
	if snapshot["continuable"] != taskID {
		t.Fatalf("completed task not continuable: %v", snapshot["continuable"])
	}

	if err := conn.WriteJSON(agentClientMsg{Type: "start", Task: "add verification details", ContinueFrom: taskID.(string), RequestID: "request-live-2"}); err != nil {
		t.Fatal(err)
	}
	continued := readAgentMessage(t, conn)
	if continued["type"] != "run_state" || continued["state"] != "running" || continued["taskId"] != taskID || continued["requestId"] != "request-live-2" {
		t.Fatalf("continuation not associated with existing task and latest request: %v", continued)
	}
	for {
		event := readAgentMessage(t, conn)
		if event["type"] == "run_state" && event["state"] == "idle" {
			if event["taskId"] != taskID || event["requestId"] != "request-live-2" {
				t.Fatalf("continued idle state lost identity: %v", event)
			}
			break
		}
	}
	snapshot = latestSnapshot(t, srv, cookie)
	record := snapshot["task"].(map[string]any)
	if record["id"] != taskID || record["requestId"] != "request-live-2" {
		t.Fatalf("continuation did not retain one task with latest request id: %v", record)
	}
	sawFollowup := false
	for _, rawEvent := range record["events"].([]any) {
		event := rawEvent.(map[string]any)
		if event["type"] == "user" && event["text"] == "add verification details" {
			sawFollowup = true
		}
	}
	if !sawFollowup {
		t.Fatal("continued user message missing from same task history")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	restored := New(Options{Assets: testAssets(), Auth: srv.auth, LLM: srv.llm, HistoryDir: srv.historyDir})
	defer restored.Shutdown(context.Background())
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID.(string), nil)
	req.AddCookie(cookie)
	restored.Handler().ServeHTTP(response, req)
	var history map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history["requestId"] != "request-live-2" {
		t.Fatalf("persisted history requestId = %v", history["requestId"])
	}
	// The old instance has relinquished its history lock; avoid a second write
	// during cleanup of its already-shut-down state.
	srv.historyDir = ""
}

func TestRejectedStartEchoesRequestIDWithoutAcceptingTask(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
		continueID string
		wantText   string
	}{
		{name: "unconfigured", wantText: "尚未配置模型"},
		{name: "invalid continuation", configured: true, continueID: "not-a-task", wantText: "无法继续"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cookie := newLLMServer(t)
			if tc.configured {
				srv.llm.SetAPIKey("test")
			}
			conn := agentSocket(t, srv, cookie)
			if err := conn.WriteJSON(agentClientMsg{Type: "start", Task: "must not be accepted", ContinueFrom: tc.continueID, RequestID: "request-rejected-1"}); err != nil {
				t.Fatal(err)
			}
			event := readAgentMessage(t, conn)
			text, _ := event["text"].(string)
			if event["type"] != "error" || event["requestId"] != "request-rejected-1" || !strings.Contains(text, tc.wantText) {
				t.Fatalf("rejection not correlated to request: %v", event)
			}
			state := readAgentMessage(t, conn)
			if state["type"] != "run_state" || state["state"] != "idle" || state["taskId"] != nil || state["requestId"] != nil {
				t.Fatalf("rejected request fabricated task identity: %v", state)
			}
			snapshot := latestSnapshot(t, srv, cookie)
			if snapshot["task"] != nil || len(snapshot["tasks"].([]any)) != 0 {
				t.Fatalf("rejected request created a task: %v", snapshot)
			}
		})
	}
}
