//go:build linux

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// Only environment discovery is stubbed: ReadFile is the production executor.
type fifoReadExecutor struct{ agent.Executor }

func (fifoReadExecutor) MachineInfo(context.Context) string { return "isolated file test" }

func TestAgentFIFOReadRejectsAndReleasesTaskSlot(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "service.log")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	// A failing baseline must not strand a reader or delay server shutdown.
	defer func() {
		fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			syscall.Close(fd)
		}
	}()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		args, _ := json.Marshal(map[string]string{"path": fifo})
		encoded, _ := json.Marshal(string(args))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"read-fifo\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", encoded)
	}))
	defer provider.Close()
	srv, cookie := newLLMServer(t)
	srv.llm = llm.New(llm.Config{BaseURL: provider.URL, APIKey: "synthetic-test-key"})
	srv.agentMaxSteps = 1
	srv.newExecutor = func() agent.Executor { return fifoReadExecutor{agent.NewShellExecutor()} }
	conn := agentSocket(t, srv, cookie)
	for attempt := 0; attempt < 2; attempt++ {
		if err := conn.WriteJSON(agentClientMsg{Type: "start", Task: "read named pipe"}); err != nil {
			t.Fatal(err)
		}
		rejected := false
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			var event map[string]any
			if err := conn.ReadJSON(&event); err != nil {
				t.Fatalf("attempt %d did not release task slot after FIFO read: %v", attempt, err)
			}
			if event["type"] == "error" && event["tool"] == "read_file" {
				text, _ := event["text"].(string)
				rejected = strings.Contains(text, "regular file")
			}
			if event["type"] == "run_state" && event["state"] == "idle" {
				break
			}
		}
		if !rejected {
			t.Fatal("special file was not rejected explicitly")
		}
	}
}
