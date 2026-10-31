package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// The opt-in browser test serves the built application through the real HTTP
// and WebSocket handlers. Its provider and executor cannot deploy anything.
func TestFrontendBrowserIntegration(t *testing.T) {
	requireBrowserIntegration(t)
	repo, assets := browserAssets(t)
	browserUserProfile := isolateProfile(t)
	var requests atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		args, _ := json.Marshal(`{"summary":"本地联调完成","success":false}`)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"本地模型回复\",\"tool_calls\":[{\"index\":0,\"id\":\"browser-%d\",\"type\":\"function\",\"function\":{\"name\":\"finish\",\"arguments\":%s}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", n, args)
	}))
	defer provider.Close()
	manager, err := auth.NewManager(auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Version: "frontend-integration", Assets: os.DirFS(assets), Auth: manager, LLM: llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-only"}), HistoryDir: t.TempDir()})
	srv.newExecutor = func() agent.Executor { return commandExecutor{} }
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	srv.mux.HandleFunc("/test/release", func(w http.ResponseWriter, _ *http.Request) { unblock(); w.WriteHeader(http.StatusNoContent) })
	srv.mux.HandleFunc("/test/expire", func(w http.ResponseWriter, _ *http.Request) { manager.Revoke(); w.WriteHeader(http.StatusNoContent) })
	srv.mux.HandleFunc("/test/provider-count", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, requests.Load()) })
	host := httptest.NewServer(srv.Handler())
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := browserScript(ctx, repo, "real-backend.browser.cjs", browserUserProfile, "DP_TEST_URL="+host.URL, "DP_TEST_TOKEN="+manager.Token())
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("real backend browser integration: ", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("reload or reconnect duplicated model requests: got %d, want 2", requests.Load())
	}
}
