package agent

import (
	"context"
	"os"
	"sort"
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

// Run answers every probe a command contains, in order, like a real shell
// running a combined check. nvidia-smi is missing: on its own it exits 127,
// but `nvidia-smi || echo ...` falls through to exit 0 as bash would.
func (s *simExec) Run(_ context.Context, command string, _ time.Duration, onOutput func(string)) (int, string, error) {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	s.mu.Unlock()
	probes := []struct{ key, out string }{
		{"os-release", "PRETTY_NAME=\"Ubuntu 22.04.4 LTS\"\nVERSION_ID=\"22.04\""},
		{"lsb_release", "Description:\tUbuntu 22.04.4 LTS"},
		{"uname", "Linux demo 5.15.0-91-generic x86_64 GNU/Linux"},
		{"df", "Filesystem Size Used Avail Use% Mounted on\n/dev/sda1  100G  20G  80G  20% /"},
		{"free", "              total   used   free\nMem:           31Gi    4Gi   27Gi"},
		{"meminfo", "MemTotal:       32768000 kB\nMemAvailable:   28311552 kB"},
		{"python3", "Python 3.10.12"},
		{"nvidia-smi", "bash: nvidia-smi: command not found"},
		{"lspci", ""},
	}
	type hit struct {
		at  int
		out string
	}
	var hits []hit
	for _, p := range probes {
		if i := strings.Index(command, p.key); i >= 0 {
			hits = append(hits, hit{i, p.out})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	var parts []string
	for _, h := range hits {
		if h.out != "" {
			parts = append(parts, h.out)
		}
	}
	out, code := strings.Join(parts, "\n"), 0
	if len(hits) == 0 {
		out = "ok"
	}
	if strings.Contains(command, "nvidia-smi") && !strings.Contains(command, "||") {
		code = 127
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
