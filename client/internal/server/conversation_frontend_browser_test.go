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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/agent"
	"github.com/deploypilot/deploypilot/internal/auth"
	"github.com/deploypilot/deploypilot/internal/llm"
)

// TestConversationFrontendBrowserIntegration exercises plain replies,
// requests for operator input, and a real failed finish through Go HTTP/WS
// handlers and Chrome. The model and executor are local test substitutes.
func TestConversationFrontendBrowserIntegration(t *testing.T) {
	if os.Getenv("DP_BROWSER_INTEGRATION") != "1" {
		t.Skip("opt-in Chrome integration")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(repo, "client", "internal", "webui", "dist")
	if _, err := os.Stat(filepath.Join(assets, "index.html")); err != nil {
		t.Fatal("build web before running browser integration: ", err)
	}
	browserProfile := os.Getenv("USERPROFILE")
	t.Setenv("USERPROFILE", t.TempDir())
	helloPath := filepath.Join(t.TempDir(), "hello.py")
	if err := os.WriteFile(helloPath, []byte("print(\"Hello, World!\")\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	var readOutputSeen atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk any
		switch n {
		case 1:
			chunk = map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "你好！我可以帮你整理部署步骤。"}, "finish_reason": "stop"}}}
		case 2:
			chunk = finishToolChunk("conversation-needs-input", map[string]any{
				"summary": "部署服务需要目标服务器地址和登录方式，请提供这两项信息后继续。",
				"success": false,
				"outcome": "needs_input",
			})
		case 3:
			chunk = finishToolChunk("conversation-failed", map[string]any{
				"summary": "模拟部署失败：测试服务器无法访问。",
				"success": false,
			})
		case 4:
			chunk = toolCallChunk("read-hello", "read_file", map[string]any{"path": helloPath})
		case 5:
			var modelRequest llm.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&modelRequest); err == nil {
				for _, message := range modelRequest.Messages {
					if message.Role == llm.RoleTool && strings.Contains(message.Content, `print("Hello, World!")`) {
						readOutputSeen.Store(true)
					}
				}
			}
			chunk = map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": `没有明显问题，hello.py 中是 print("Hello, World!")。`}, "finish_reason": "stop"}}}
		default:
			http.Error(w, "unexpected extra model request", http.StatusInternalServerError)
			return
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	}))
	defer provider.Close()
	manager, err := auth.NewManager(auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{
		Version:    "conversation-integration",
		Assets:     os.DirFS(assets),
		Auth:       manager,
		LLM:        llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-only"}),
		HistoryDir: t.TempDir(),
	})
	fileReader := agent.NewShellExecutor()
	srv.newExecutor = func() agent.Executor {
		return readFileConversationExecutor{commandExecutor: commandExecutor{}, files: fileReader}
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	srv.mux.HandleFunc("/test/provider-count", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, requests.Load()) })
	srv.mux.HandleFunc("/test/read-output", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, readOutputSeen.Load()) })
	host := httptest.NewServer(srv.Handler())
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join(repo, "web", "tests", "conversation-real-backend.browser.cjs"))
	cmd.Env = append(os.Environ(), "DP_TEST_URL="+host.URL, "DP_TEST_TOKEN="+manager.Token())
	if browserProfile != "" {
		cmd.Env = append(cmd.Env, "USERPROFILE="+browserProfile)
	}
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal("conversation frontend integration: ", err)
	}
	if got := requests.Load(); got != 5 {
		t.Fatalf("unexpected model calls: got %d, want exactly 5", got)
	}
	if !readOutputSeen.Load() {
		t.Fatal("read_file result was missing from the next model request")
	}
}

func finishToolChunk(callID string, args map[string]any) any {
	return toolCallChunk(callID, "finish", args)
}

func toolCallChunk(callID, name string, args map[string]any) any {
	encodedArgs, _ := json.Marshal(args)
	argumentString, _ := json.Marshal(string(encodedArgs))
	return map[string]any{"choices": []any{map[string]any{
		"delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": callID, "type": "function",
			"function": map[string]any{"name": name, "arguments": json.RawMessage(argumentString)},
		}}},
		"finish_reason": "tool_calls",
	}}}
}

type readFileConversationExecutor struct {
	commandExecutor
	files agent.Executor
}

func (e readFileConversationExecutor) ReadFile(path string, offset, maxBytes int64) (agent.ReadResult, error) {
	return e.files.ReadFile(path, offset, maxBytes)
}
