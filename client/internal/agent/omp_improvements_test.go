package agent

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestCapturedOutputRetainsHeadTailAcrossWrites(t *testing.T) {
	input := "INSTALL_START\n" + strings.Repeat("下载🙂", 10000) + "\nFATAL: missing CUDA library"
	for _, chunkSize := range []int{1, 123, len(input)} {
		buf := cappedBuffer{limit: maxCapturedOutput}
		for offset := 0; offset < len(input); offset += chunkSize {
			p := []byte(input[offset:min(offset+chunkSize, len(input))])
			if n, err := buf.Write(p); n != len(p) || err != nil {
				t.Fatalf("write n=%d err=%v", n, err)
			}
		}
		got := buf.String()
		if !strings.HasPrefix(got, "INSTALL_START") || !strings.HasSuffix(got, "FATAL: missing CUDA library") || !strings.Contains(got, "output truncated") {
			t.Fatalf("chunk=%d: lost boundaries or truncation notice", chunkSize)
		}
		if !utf8.ValidString(got) || len(got) > maxCapturedOutput+128 {
			t.Fatalf("chunk=%d: invalid UTF-8 or unbounded output (%d)", chunkSize, len(got))
		}
	}
}

func TestCapturedOutputKeepsShortContent(t *testing.T) {
	buf := cappedBuffer{limit: 32}
	for _, s := range []string{"hello ", "中文", "\n"} {
		_, _ = buf.Write([]byte(s))
	}
	if got := buf.String(); got != "hello 中文\n" {
		t.Fatalf("got %q", got)
	}
}

func TestShellFailureIncludesLogTail(t *testing.T) {
	command := `printf 'START\n'; printf '%20000s' ' '; printf '\nFATAL_END\n'; exit 7`
	if runtime.GOOS == "windows" {
		command = `[Console]::WriteLine('START'); [Console]::Write(('x' * 20000)); [Console]::WriteLine('FATAL_END'); exit 7`
	}
	exit, out, err := NewShellExecutor().Run(context.Background(), command, 10*time.Second, nil)
	if err != nil || exit != 7 || !strings.HasPrefix(out, "START") || !strings.Contains(out, "FATAL_END") || !strings.Contains(out, "output truncated") {
		t.Fatalf("exit=%d err=%v retained failure=%v", exit, err, strings.Contains(out, "FATAL_END"))
	}
}

func TestCompactionKeepsLatestResultBeforeOldHistory(t *testing.T) {
	w := newContextWindow([]llm.Message{{Role: llm.RoleUser, Content: "Keep the database"}})
	latest := "LATEST_BEGIN" + strings.Repeat("current evidence ", 240) + "LATEST_END"
	for _, pair := range []struct{ id, output string }{{"old", strings.Repeat("old data ", 1000)}, {"latest", latest}} {
		w.turns = append(w.turns, []llm.Message{
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: pair.id, Type: "function", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"inspect"}`}}}},
			{Role: llm.RoleTool, ToolCallID: pair.id, Content: pair.output},
		})
	}
	req, compacted, err := w.prepare("test", nil, 5500)
	if err != nil || !compacted {
		t.Fatalf("compacted=%v err=%v", compacted, err)
	}
	assertToolPairs(t, req.Messages)
	if requestBytes(req) > 5500 {
		t.Fatal("request over budget")
	}
	for _, m := range req.Messages {
		if m.ToolCallID == "latest" {
			if m.Content != latest {
				t.Fatal("latest evidence shortened before reclaiming old history")
			}
			return
		}
	}
	t.Fatal("latest complete turn discarded")
}

func TestPlainReplyWithoutToolUseFinishesAsReplyOnce(t *testing.T) {
	srv := scriptedLLM(t, []string{textSSE("Hello there!")})
	defer srv.Close()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: srv.URL, APIKey: "test"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("plain reply: %v", err)
	}
	final := lastFinal(events)
	if final == nil || final.State != "reply" || final.Success || final.Text != "Hello there!" {
		t.Fatalf("unexpected reply event: %+v", final)
	}
}

func TestPlainReplyAfterToolUseStillRequiresFinish(t *testing.T) {
	srv := scriptedLLM(t, []string{
		toolCallSSE("check", toolRunCommand, `{"command":"echo checked"}`),
		textSSE("already done"),
		toolCallSSE("finish", toolFinish, `{"summary":"needs operator input","success":false}`),
	})
	defer srv.Close()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: srv.URL, APIKey: "test"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "deploy"); err != nil {
		t.Fatalf("finish(false) should end normally: %v", err)
	}
	final := lastFinal(events)
	if final == nil || final.State != "" || final.Success || final.Text != "needs operator input" {
		t.Fatalf("plain reply after tool use bypassed finish: %+v", final)
	}
}

func TestResumeWithToolHistoryDoesNotBecomePlainReply(t *testing.T) {
	first := scriptedLLM(t, []string{toolCallSSE("read", toolReadFile, `{"path":"service.conf"}`)})
	emit, _, _ := collectEvents()
	initial := New(Config{LLM: llm.New(llm.Config{BaseURL: first.URL, APIKey: "test"}), Executor: newFakeExec(), MaxSteps: 1, Emit: emit})
	if err := initial.Run(context.Background(), "deploy"); err == nil {
		t.Fatal("first run should stop at its one-turn cap")
	}
	conversation := initial.Conversation()
	first.Close()

	continued := scriptedLLM(t, []string{
		textSSE("done"),
		toolCallSSE("finish", toolFinish, `{"summary":"still needs a final disposition","success":false}`),
	})
	defer continued.Close()
	_, resumedEvents, _ := collectEvents()
	resumed := New(Config{LLM: llm.New(llm.Config{BaseURL: continued.URL, APIKey: "test"}), Executor: newFakeExec(), Resume: conversation, Emit: func(e Event) { (*resumedEvents) = append(*resumedEvents, e) }})
	if err := resumed.Run(context.Background(), "continue"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	final := lastFinal(resumedEvents)
	if final == nil || final.State == "reply" || final.Success || final.Text != "still needs a final disposition" {
		t.Fatalf("resumed task was treated as plain chat: %+v", final)
	}
}

func TestMissingFinishStopsAfterOneReminder(t *testing.T) {
	srv := scriptedLLM(t, []string{
		toolCallSSE("check", toolRunCommand, `{"command":"echo checked"}`),
		textSSE("还没有完成"),
		textSSE("无法继续"),
	})
	defer srv.Close()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: srv.URL, APIKey: "test"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "deploy"); err == nil {
		t.Fatal("missing finish must not be success")
	}
	last := (*events)[len(*events)-1]
	if last.Type != EventFinal || last.Success || !strings.Contains(last.Text, "无法继续") {
		t.Fatalf("unexpected final: %+v", last)
	}
}

func lastFinal(events *[]Event) *Event {
	for i := len(*events) - 1; i >= 0; i-- {
		if (*events)[i].Type == EventFinal {
			return &(*events)[i]
		}
	}
	return nil
}

type failingToolExecutor struct{ *fakeExec }

func (f failingToolExecutor) Run(context.Context, string, time.Duration, func(string)) (int, string, error) {
	return 7, "package not found", nil
}
func (f failingToolExecutor) WriteFile(string, string) error { return errors.New("disk full") }
func (f failingToolExecutor) ReadFile(string, int64, int64) (ReadResult, error) {
	return ReadResult{}, errors.New("permission denied")
}
func (f failingToolExecutor) AppendFile(string, string) error { return errors.New("read only") }
func (f failingToolExecutor) EditFile(string, string, string, bool) (int, error) {
	return 0, errors.New("old string missing")
}

func TestToolFailuresHaveOneCompletionAndError(t *testing.T) {
	cases := []struct{ name, args string }{
		{toolRunCommand, `{"command":"install"}`},
		{toolRunCommand, `{"command":"rm -rf /"}`},
		{toolWriteFile, `{"path":"config","content":"x"}`},
		{toolReadFile, `{"path":"config"}`},
		{toolAppendFile, `{"path":"config","content":"x"}`},
		{toolEditFile, `{"path":"config","old_string":"x","new_string":"y"}`},
		{toolWriteFile, `{"path":"config"}`},
		{"unknown", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			emit, events, _ := collectEvents()
			a := New(Config{Executor: failingToolExecutor{newFakeExec()}, Emit: emit})
			done, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "call", Function: llm.FunctionCall{Name: tc.name, Arguments: tc.args}})
			if done || !strings.HasPrefix(msg.Content, "error:") {
				t.Fatalf("failure not explicit to model: %+v", msg)
			}
			calls, ends, failures := 0, 0, 0
			for _, e := range *events {
				if e.Type == EventToolCall {
					calls++
				}
				if e.Type == EventError {
					failures++
				}
				if e.Type == EventToolDone {
					ends++
					wire := eventJSON(t, e)
					if e.CallID != "call" || wire["success"] != false {
						t.Fatalf("missing explicit failure: %v", wire)
					}
					if tc.name == toolRunCommand && strings.Contains(tc.args, "install") && e.ExitCode != 7 {
						t.Fatal("lost actual exit code")
					}
				}
			}
			if calls != 1 || ends != 1 || failures != 1 {
				t.Fatalf("calls=%d ends=%d errors=%d", calls, ends, failures)
			}
		})
	}
}

func TestSuccessfulToolCompletionIsExplicit(t *testing.T) {
	emit, events, _ := collectEvents()
	a := New(Config{Executor: newFakeExec(), Emit: emit})
	_, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "write", Function: llm.FunctionCall{Name: toolWriteFile, Arguments: `{"path":"config","content":"error: ordinary file text"}`}})
	if !strings.HasPrefix(msg.Content, "ok:") {
		t.Fatal(msg.Content)
	}
	for _, e := range *events {
		if e.Type == EventError {
			t.Fatal("normal file content treated as failure")
		}
		if e.Type == EventToolDone {
			b, _ := json.Marshal(e)
			if !strings.Contains(string(b), `"success":true`) {
				t.Fatalf("missing success: %s", b)
			}
			return
		}
	}
	t.Fatal("no completion")
}
