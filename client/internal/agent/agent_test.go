package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// fakeExec records commands and returns scripted results.
type fakeExec struct {
	mu       sync.Mutex
	commands []string
	reads    []string
	writes   map[string]string
}

func newFakeExec() *fakeExec { return &fakeExec{writes: map[string]string{}} }

func (f *fakeExec) Run(_ context.Context, command string, _ time.Duration, onOutput func(string)) (int, string, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	f.mu.Unlock()
	if onOutput != nil {
		onOutput("output of: " + command)
	}
	return 0, "output of: " + command, nil
}

func (f *fakeExec) ReadFile(path string, offset, maxBytes int64) (ReadResult, error) {
	f.reads = append(f.reads, path)
	c := "contents of " + path
	return ReadResult{Content: c, Offset: offset, Size: int64(len(c)), NextOffset: int64(len(c)), EOF: true}, nil
}

func (f *fakeExec) WriteFile(path, content string) error {
	f.writes[path] = content
	return nil
}

func (f *fakeExec) AppendFile(path, content string) error {
	f.writes[path] += content
	return nil
}

func (f *fakeExec) EditFile(path, oldStr, newStr string, replaceAll bool) (int, error) {
	cur, ok := f.writes[path]
	if !ok {
		return 0, fmt.Errorf("no such file %s", path)
	}
	if replaceAll {
		n := strings.Count(cur, oldStr)
		f.writes[path] = strings.ReplaceAll(cur, oldStr, newStr)
		return n, nil
	}
	f.writes[path] = strings.Replace(cur, oldStr, newStr, 1)
	return 1, nil
}

func (f *fakeExec) MachineInfo(context.Context) string { return "fake machine" }

// scriptedLLM returns a sequence of SSE responses, one per request.
func scriptedLLM(t *testing.T, responses []string) *httptest.Server {
	var i int
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		idx := i
		i++
		mu.Unlock()
		if idx >= len(responses) {
			t.Errorf("unexpected extra LLM call #%d", idx)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responses[idx])
	}))
}

func toolCallSSE(id, name, args string) string {
	esc := strings.ReplaceAll(args, `"`, `\"`)
	return "data: " +
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":"` + esc + `"}}]},"finish_reason":"tool_calls"}]}` +
		"\n\ndata: [DONE]\n\n"
}

func textSSE(content string) string {
	return "data: " + `{"choices":[{"delta":{"content":"` + content + `"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
}

func collectEvents() (func(Event), *[]Event, *sync.Mutex) {
	var mu sync.Mutex
	events := []Event{}
	return func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}, &events, &mu
}

func TestAgentRunsCommandThenFinishes(t *testing.T) {
	srv := scriptedLLM(t, []string{
		toolCallSSE("c1", "run_command", `{"command":"echo hello"}`),
		toolCallSSE("c2", "finish", `{"summary":"done","success":true}`),
	})
	defer srv.Close()

	fe := newFakeExec()
	emit, events, mu := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: fe,
		Emit:     emit,
	})

	if err := a.Run(context.Background(), "say hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fe.commands) != 1 || fe.commands[0] != "echo hello" {
		t.Fatalf("commands = %v", fe.commands)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawFinal bool
	for _, e := range *events {
		if e.Type == EventFinal && e.Success {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Fatalf("expected successful final event, got %+v", *events)
	}
}

func TestAgentBlocksDangerousCommand(t *testing.T) {
	srv := scriptedLLM(t, []string{
		toolCallSSE("c1", "run_command", `{"command":"rm -rf /"}`),
		toolCallSSE("c2", "finish", `{"summary":"stopped","success":false}`),
	})
	defer srv.Close()

	fe := newFakeExec()
	emit, events, mu := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: fe,
		Emit:     emit,
	})

	_ = a.Run(context.Background(), "destroy everything")

	if len(fe.commands) != 0 {
		t.Fatalf("dangerous command must not execute, ran: %v", fe.commands)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawError bool
	for _, e := range *events {
		if e.Type == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected an error event for blocked command")
	}
}

func TestAgentMaxSteps(t *testing.T) {
	// Always ask to run a command; never finish.
	resp := toolCallSSE("c", "run_command", `{"command":"true"}`)
	responses := make([]string, 3)
	for i := range responses {
		responses[i] = resp
	}
	srv := scriptedLLM(t, responses)
	defer srv.Close()

	fe := newFakeExec()
	emit, events, mu := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: fe,
		MaxSteps: 3,
		Emit:     emit,
	})

	err := a.Run(context.Background(), "loop forever")
	if err == nil {
		t.Fatal("expected max-steps error")
	}
	if len(fe.commands) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(fe.commands))
	}
	mu.Lock()
	defer mu.Unlock()
	last := (*events)[len(*events)-1]
	if last.Type != EventFinal || last.Success {
		t.Fatalf("expected unsuccessful final, got %+v", last)
	}
}

func TestAgentStopViaContext(t *testing.T) {
	srv := scriptedLLM(t, []string{
		toolCallSSE("c1", "run_command", `{"command":"sleep"}`),
	})
	defer srv.Close()

	fe := newFakeExec()
	emit, _, _ := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: fe,
		Emit:     emit,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before start
	err := a.Run(ctx, "do something")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestAgentReadWriteFile(t *testing.T) {
	srv := scriptedLLM(t, []string{
		toolCallSSE("c1", "write_file", `{"path":"/tmp/x.conf","content":"hello"}`),
		toolCallSSE("c2", "read_file", `{"path":"/tmp/x.conf"}`),
		textSSE("all set"),
	})
	defer srv.Close()

	fe := newFakeExec()
	emit, _, _ := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: fe,
		Emit:     emit,
	})
	if err := a.Run(context.Background(), "configure"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fe.writes["/tmp/x.conf"] != "hello" {
		t.Fatalf("write not recorded: %v", fe.writes)
	}
	if len(fe.reads) != 1 || fe.reads[0] != "/tmp/x.conf" {
		t.Fatalf("read not recorded: %v", fe.reads)
	}
}

// An empty model reply must not end the task with a blank "完成" card; the
// agent asks once for a summary.
func TestAgentNudgesOnEmptyReply(t *testing.T) {
	srv := scriptedLLM(t, []string{
		textSSE(""),
		toolCallSSE("c1", "finish", `{"summary":"ok summary","success":true}`),
	})
	defer srv.Close()

	emit, events, mu := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: newFakeExec(),
		Emit:     emit,
	})
	if err := a.Run(context.Background(), "x"); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	last := (*events)[len(*events)-1]
	if last.Type != EventFinal || last.Text != "ok summary" {
		t.Fatalf("expected final with summary, got %+v", last)
	}
}

// Two empty replies in a row still end with a readable message.
func TestAgentEmptyReplyFallback(t *testing.T) {
	srv := scriptedLLM(t, []string{textSSE(""), textSSE("")})
	defer srv.Close()

	emit, events, mu := collectEvents()
	a := New(Config{
		LLM:      llm.New(llm.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}),
		Executor: newFakeExec(),
		Emit:     emit,
	})
	_ = a.Run(context.Background(), "x")
	mu.Lock()
	defer mu.Unlock()
	last := (*events)[len(*events)-1]
	if last.Type != EventFinal || strings.TrimSpace(last.Text) == "" {
		t.Fatalf("expected non-empty final text, got %+v", last)
	}
}
