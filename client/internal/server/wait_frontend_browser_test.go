package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// Short authentication expiry makes a silent wait cross the boundary without
// running a half-hour test. The model and executor remain local substitutes.
func TestWaitForFrontendBrowserIntegration(t *testing.T) {
	if os.Getenv("DP_BROWSER_INTEGRATION") != "1" {
		t.Skip("opt-in Chrome integration")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	browserProfile := os.Getenv("USERPROFILE")
	t.Setenv("USERPROFILE", t.TempDir())
	var ready atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer target.Close()
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		name, id := "wait_for", "ready-check"
		args, _ := json.Marshal(map[string]any{"kind": "url", "target": target.URL, "timeout_seconds": 30, "interval_seconds": 1})
		if n == 2 {
			name, id = "finish", "done"
			args = []byte(`{"summary":"等待验证完成","success":true,"verification_call_ids":["ready-check"]}`)
		}
		if n > 2 {
			id = "stop-check"
		}
		encoded, _ := json.Marshal(string(args))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", id, name, encoded)
	}))
	defer provider.Close()
	manager, err := auth.NewManager(auth.Options{IdleTimeout: 4 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Version: "wait-integration", Assets: os.DirFS(filepath.Join(repo, "client/internal/webui/dist")), Auth: manager, LLM: llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-only"}), HistoryDir: t.TempDir()})
	srv.newExecutor = func() agent.Executor { return commandExecutor{} }
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	srv.mux.HandleFunc("/test/ready", func(w http.ResponseWriter, _ *http.Request) { ready.Store(true); w.WriteHeader(http.StatusNoContent) })
	srv.mux.HandleFunc("/test/reset", func(w http.ResponseWriter, _ *http.Request) { ready.Store(false); w.WriteHeader(http.StatusNoContent) })
	srv.mux.HandleFunc("/test/count", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, requests.Load()) })
	host := httptest.NewServer(srv.Handler())
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web/tests/wait-real-backend.browser.cjs"))
	cmd.Env = append(os.Environ(), "DP_TEST_URL="+host.URL, "DP_TEST_TOKEN="+manager.Token())
	if browserProfile != "" {
		cmd.Env = append(cmd.Env, "USERPROFILE="+browserProfile)
	}
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("wait frontend integration: ", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("unexpected model replay: calls=%d want 3", requests.Load())
	}
}
