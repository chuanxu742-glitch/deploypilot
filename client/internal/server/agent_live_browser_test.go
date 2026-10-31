package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// TestAgentLiveBrowser drives the built web app in Chrome against the real
// HTTP/WS server, the real terminal and the real model. Every command goes
// through the guard executor: file changes stay inside one temp directory and
// only processes started by this test may be stopped.
func TestAgentLiveBrowser(t *testing.T) {
	if os.Getenv("DP_AGENT_LIVE") != "1" || os.Getenv("BAILU_API_KEY") == "" || os.Getenv("DP_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DP_AGENT_LIVE=1, DP_BROWSER_INTEGRATION=1 and BAILU_API_KEY after building web")
	}
	if runtime.GOOS != "windows" {
		t.Skip("scenarios are written for Windows PowerShell")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(repo, "client", "internal", "webui", "dist")
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal("build web first: ", err)
	}
	out := os.Getenv("DP_LIVE_BROWSER_OUT")
	if out == "" {
		out = t.TempDir()
	}
	browserProfile := os.Getenv("USERPROFILE")
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())

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
	protected := runningPIDs(t)
	t.Cleanup(func() { stopLeftovers(t) })
	g := newGuardExecutor(srv.newExecutor(), work, protected)
	g.owned = func(pid int) bool {
		p, ok := descendants(t)[pid]
		return ok && p.name != "powershell.exe" && p.name != "conhost.exe"
	}
	srv.newExecutor = func() agent.Executor { return g }

	srv.mux.HandleFunc("/test/guard", func(w http.ResponseWriter, _ *http.Request) {
		commands, hits := g.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": commands, "hits": hits})
	})
	host := httptest.NewServer(srv.Handler())
	defer host.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web", "tests", "agent-live-model.browser.cjs"))
	cmd.Env = append(os.Environ(),
		"DP_TEST_URL="+host.URL,
		"DP_TEST_TOKEN="+manager.Token(),
		"DP_TEST_WORK="+work,
		"DP_TEST_OUT="+out,
		"BAILU_API_KEY=", // the browser script never needs the key
	)
	if browserProfile != "" {
		cmd.Env = append(cmd.Env, "USERPROFILE="+browserProfile)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	reportGuard(t, g)
	if err != nil {
		t.Fatal("live browser scenarios: ", err)
	}
}
