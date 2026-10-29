package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// simExec simulates a small Ubuntu server so the real model can drive a full
// multi-step agent loop on any dev host. Skipped unless BAILU_API_KEY is set.
type simExec struct {
	mu       sync.Mutex
	commands []string
}

func (s *simExec) Run(_ context.Context, command string, _ time.Duration, onOutput func(string)) (int, string, error) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	out, code := "", 0
	switch {
	case strings.Contains(command, "uname"):
		out = "Linux demo 5.15.0-91-generic x86_64 GNU/Linux"
	case strings.Contains(command, "df"):
		out = "/dev/sda1  100G  20G  80G  20% /"
	case strings.Contains(command, "free"):
		out = "Mem:  31Gi  4Gi  27Gi"
	case strings.Contains(command, "python3"):
		out = "Python 3.10.12"
	case strings.Contains(command, "nvidia-smi"):
		out, code = "bash: nvidia-smi: command not found", 127
	default:
		out = "ok"
	}
	if onOutput != nil {
		onOutput(out)
	}
	return code, out, nil
}

func (s *simExec) ReadFile(string, int64, int64) (ReadResult, error) {
	return ReadResult{}, os.ErrNotExist
}
func (s *simExec) WriteFile(string, string) error                     { return nil }
func (s *simExec) AppendFile(string, string) error                    { return nil }
func (s *simExec) EditFile(string, string, string, bool) (int, error) { return 0, nil }
func (s *simExec) MachineInfo(context.Context) string {
	return "OS: Ubuntu 22.04 (simulated)\nGPU: none"
}

func TestLiveAgentLoop(t *testing.T) {
	if os.Getenv("BAILU_API_KEY") == "" {
		t.Skip("BAILU_API_KEY not set; skipping live test")
	}
	c := llm.FromEnv()
	if c.Model() == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		models, err := c.ListModels(ctx)
		cancel()
		if err != nil || len(models) == 0 {
			t.Fatalf("no model: %v", err)
		}
		c.SetModel(models[0])
	}

	ex := &simExec{}
	var mu sync.Mutex
	var final *Event
	a := New(Config{
		LLM:      c,
		Executor: ex,
		MaxSteps: 8,
		Emit: func(e Event) {
			if e.Type == EventToolCall {
				t.Logf("tool %s: %s%s", e.Tool, e.Command, e.Path)
			}
			if e.Type == EventFinal {
				mu.Lock()
				ev := e
				final = &ev
				mu.Unlock()
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err := a.Run(ctx, "检查这台服务器的系统版本、磁盘空间、内存和是否有 NVIDIA GPU，然后调用 finish 总结是否适合部署 Qwen2.5-0.5B。")
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	if len(ex.commands) == 0 {
		t.Fatal("agent finished without running any command")
	}
	mu.Lock()
	defer mu.Unlock()
	if final == nil {
		t.Fatal("no final event")
	}
	t.Logf("commands=%d final=%q", len(ex.commands), final.Text)
}
