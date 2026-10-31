package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// TestAgentLiveBrowser drives the built web app in Chrome against the real
// HTTP/WS server, the real terminal and the real model. Every command goes
// through the guard executor: file changes stay inside one temp directory and
// only processes started by this test may be stopped.
func TestAgentLiveBrowser(t *testing.T) {
	requireLive(t, "DP_BROWSER_INTEGRATION")
	requireWindows(t)
	repo, assets := browserAssets(t)
	out := os.Getenv("DP_LIVE_BROWSER_OUT")
	if out == "" {
		out = t.TempDir()
	}
	browserProfile := isolateProfile(t)
	t.Setenv("HOME", t.TempDir())
	leaveRepository(t) // repo and assets are already absolute

	manager, err := auth.NewManager(auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Version: "live-browser", Assets: os.DirFS(assets), Auth: manager, LLM: llm.FromEnv(), HistoryDir: t.TempDir()})
	srv.agentMaxSteps = 30
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	work := t.TempDir()
	g := installGuard(t, srv, srv.newExecutor(), work)

	srv.mux.HandleFunc("/test/guard", func(w http.ResponseWriter, _ *http.Request) {
		commands, hits := g.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": commands, "hits": hits})
	})
	host := httptest.NewServer(srv.Handler())
	defer host.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := browserScript(ctx, repo, "agent-live-model.browser.cjs", browserProfile,
		"DP_TEST_URL="+host.URL,
		"DP_TEST_TOKEN="+manager.Token(),
		"DP_TEST_WORK="+work,
		"DP_TEST_OUT="+out,
		"BAILU_API_KEY=", // the browser script never needs the key
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	reportGuard(t, g)
	if err != nil {
		t.Fatal("live browser scenarios: ", err)
	}
}
