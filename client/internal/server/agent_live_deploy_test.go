package server

// A real model drives a small deployment through the real server and the
// real persistent terminal, and the result is checked independently.
// Needs BAILU_API_KEY and DP_AGENT_LIVE=1. The task is confined to a temp
// directory, uses only the Python standard library and installs nothing.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestAgentLiveDeploy(t *testing.T) {
	if os.Getenv("DP_AGENT_LIVE") != "1" || os.Getenv("BAILU_API_KEY") == "" {
		t.Skip("set DP_AGENT_LIVE=1 and BAILU_API_KEY to run a real-model deployment on this machine")
	}
	if _, err := exec.LookPath("python"); err != nil && runtime.GOOS == "windows" {
		t.Skip("python not found")
	}
	model := llm.FromEnv()
	t.Chdir(t.TempDir()) // the terminal starts here; keep relative writes out of the repo
	srv, cookie := newLLMServer(t)
	srv.llm = model
	srv.agentMaxSteps = 20
	h := &deepHarness{t: t, srv: srv, model: &scriptedModel{}, conn: agentSocket(t, srv, cookie), cookie: cookie, work: t.TempDir()}
	port := freePort(t)
	t.Cleanup(func() { stopListener(t, port) })
	marker := fmt.Sprintf("deploypilot-live-%d", time.Now().UnixNano())
	task := fmt.Sprintf(`在目录 %s 中部署一个最小的 HTTP 服务：
- 只能在这个目录里创建或修改文件，不要安装任何软件，只用 Python 标准库（命令是 python）。
- 访问 http://127.0.0.1:%d/ 时返回纯文本 %s。
- 服务要在后台运行，不能阻塞终端，日志写到该目录。
- 启动后用真实请求验证返回内容，然后调用 finish 并引用验证命令的 call_id。`, h.work, port, marker)

	id := h.start(task, "")
	run := h.collect(id, 8*time.Minute, func(m map[string]any) {
		switch m["type"] {
		case "tool_call":
			t.Logf("tool %v: %v%v", m["tool"], m["command"], m["path"])
		case "tool_done":
			t.Logf("  done %v exit=%v success=%v state=%v", m["callId"], m["exitCode"], m["success"], m["state"])
		case "error", "status":
			t.Logf("  %v: %s", m["type"], truncateUTF8(fmt.Sprint(m["text"]), 300))
		}
	})
	t.Logf("elapsed %.0fs, %d events", run.elapsed.Seconds(), len(run.events))
	if len(run.finals) != 1 {
		t.Fatalf("want exactly one final, got %d", len(run.finals))
	}
	final := run.finals[0]
	t.Logf("final success=%v state=%v:\n%s", final["success"], final["state"], final["text"])

	// Independent check: the service answers with the marker.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("service is not reachable after the agent finished: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), marker) {
		t.Fatalf("service answered %d %q, want the marker", resp.StatusCode, truncateUTF8(string(body), 200))
	}
	if final["success"] != true {
		t.Errorf("the service works but the agent did not report verified success")
	}
	if s, _ := final["text"].(string); !strings.Contains(s, "实际验证记录") {
		t.Errorf("success final lacks the attached verification record")
	}
	// Nothing was written outside the work directory by file tools.
	for _, e := range run.of("tool_call") {
		if p, _ := e["path"].(string); p != "" {
			abs := p
			if !filepath.IsAbs(abs) {
				continue
			}
			if rel, err := filepath.Rel(h.work, abs); err != nil || strings.HasPrefix(rel, "..") {
				t.Errorf("file tool touched %s outside the work directory", p)
			}
		}
	}
}

// stopListener ends whatever the agent left listening on port.
func stopListener(t *testing.T, port int) {
	if runtime.GOOS != "windows" {
		_ = exec.Command("sh", "-c", fmt.Sprintf("fuser -k %d/tcp 2>/dev/null || true", port)).Run()
		return
	}
	script := fmt.Sprintf("Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }", port)
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput(); err != nil {
		t.Logf("cleanup: %v %s", err, out)
	}
}
