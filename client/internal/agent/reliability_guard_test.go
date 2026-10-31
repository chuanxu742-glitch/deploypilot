package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestRepeatGuardIgnoresCallAndLogIDs(t *testing.T) {
	var guard repetitionGuard
	for i, id := range []string{"a", "b", "c", "d"} {
		call := llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"probe"}`}}
		out := llm.Message{Content: "[call_id: " + id + "]\nexit=1\nunchanged\n[full output: /logs/" + id + ".log]"}
		warn, stop := guard.observe(call, out)
		if warn != (i == 2) || stop != (i == 3) {
			t.Fatalf("repeat %d: warn=%v stop=%v", i+1, warn, stop)
		}
	}
	_, stop := guard.observe(llm.ToolCall{Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"different"}`}}, llm.Message{Content: "unchanged"})
	if stop {
		t.Fatal("changing the call must reset repetition")
	}
}

func TestFinishRequiresRealSuccessfulVerification(t *testing.T) {
	a := New(Config{Executor: newFakeExec()})
	call := func(id, name, args string) (bool, llm.Message) {
		return a.runTool(context.Background(), 1, llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: name, Arguments: args}})
	}
	for _, args := range []string{`{"summary":"done","success":true}`, `{"summary":"done","success":true,"verification_call_ids":["invented"]}`} {
		if done, msg := call("finish", toolFinish, args); done || !strings.Contains(msg.Content, "error:") {
			t.Fatalf("unverified success accepted: done=%v result=%s", done, msg.Content)
		}
	}
	call("probe", toolRunCommand, `{"command":"curl --fail http://localhost:8000/health"}`)
	if done, msg := call("finish", toolFinish, `{"summary":"done","success":true,"verification_call_ids":["probe"]}`); !done || strings.Contains(msg.Content, "error:") {
		t.Fatalf("observed verification rejected: done=%v result=%s", done, msg.Content)
	}
}

func TestFinishNeedsInputIsSeparateFromFailureAndSuccess(t *testing.T) {
	for _, input := range []string{
		`{"summary":"need input","success":true,"outcome":"needs_input"}`,
		`{"summary":"need input","success":false,"outcome":"needs_input","verification_call_ids":["unexpected-check"]}`,
		`{"summary":"need input","success":false,"outcome":"unknown"}`,
	} {
		if err := validateToolArgs(toolFinish, input); err == nil {
			t.Errorf("accepted incompatible finish outcome: %s", input)
		}
	}

	var events []Event
	a := New(Config{Executor: newFakeExec(), Emit: func(e Event) { events = append(events, e) }})
	done, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "need", Function: llm.FunctionCall{
		Name: toolFinish, Arguments: `{"summary":"Which region should I use?","success":false,"outcome":"needs_input"}`,
	}})
	if !done || strings.HasPrefix(msg.Content, "error:") {
		t.Fatalf("needs_input should end normally: done=%v, result=%s", done, msg.Content)
	}
	final := events[len(events)-1]
	if final.Type != EventFinal || final.State != "needs_input" || final.Success || final.Text != "Which region should I use?" {
		t.Fatalf("unexpected needs_input event: %+v", final)
	}

	events = nil
	done, _ = a.runTool(context.Background(), 2, llm.ToolCall{ID: "failed", Function: llm.FunctionCall{
		Name: toolFinish, Arguments: `{"summary":"could not complete","success":false}`,
	}})
	if !done {
		t.Fatal("ordinary failure finish should still end")
	}
	final = events[len(events)-1]
	if final.Type != EventFinal || final.State != "" || final.Success || final.Text != "could not complete" {
		t.Fatalf("ordinary finish(false) changed: %+v", final)
	}
}

func TestFileChangeInvalidatesVerification(t *testing.T) {
	a := New(Config{Executor: newFakeExec()})
	call := func(id, name, args string) (bool, llm.Message) {
		return a.runTool(context.Background(), 1, llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: name, Arguments: args}})
	}
	call("check", toolRunCommand, `{"command":"check"}`)
	call("write", toolWriteFile, `{"path":"config","content":"changed"}`)
	if done, msg := call("done", toolFinish, `{"summary":"done","success":true,"verification_call_ids":["check"]}`); done || !strings.Contains(msg.Content, "is no longer current") || !strings.Contains(msg.Content, "No check is current now") {
		t.Fatalf("verification before a file change must be repeated, with the reason: %s", msg.Content)
	}
	if done, _ := call("help", toolFinish, `{"summary":"needs input","success":false}`); !done {
		t.Fatal("unsuccessful finish should remain available")
	}
}

func TestAgentPausesRepeatedCalls(t *testing.T) {
	responses := make([]string, 4)
	for i, id := range []string{"a", "b", "c", "d"} {
		responses[i] = toolCallSSE(id, toolRunCommand, `{"command":"unchanged"}`)
	}
	model := scriptedLLM(t, responses)
	defer model.Close()
	executor := newFakeExec()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.URL, APIKey: "test"}), Executor: executor, Emit: emit})
	if err := a.Run(context.Background(), "probe"); err == nil {
		t.Fatal("repeated calls should pause")
	}
	if len(executor.commands) != 4 {
		t.Fatalf("commands=%d, expected 4", len(executor.commands))
	}
	warned := false
	for _, event := range *events {
		if event.Type == EventStatus && strings.Contains(event.Text, "连续出现 3 次") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("no warning before pausing")
	}
	assertToolPairs(t, a.Conversation().window.request("test", nil).Messages)
}

func TestWaitForCanFinishWithObservedEvidence(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer health.Close()
	model := scriptedLLM(t, []string{
		toolCallSSE("health", toolWaitFor, `{"kind":"url","target":"`+health.URL+`"}`),
		toolCallSSE("done", toolFinish, `{"summary":"ready","success":true,"verification_call_ids":["health"]}`),
	})
	defer model.Close()
	emit, events, _ := collectEvents()
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.URL, APIKey: "test"}), Executor: newFakeExec(), Emit: emit})
	if err := a.Run(context.Background(), "wait for health"); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Type != EventFinal || !last.Success || !strings.Contains(last.Text, "HTTP 204") {
		t.Fatalf("missing observed evidence: %+v", last)
	}
}

func TestFailedCommandIsNotVerification(t *testing.T) {
	a := New(Config{Executor: failingToolExecutor{newFakeExec()}})
	_, _ = a.runTool(context.Background(), 1, llm.ToolCall{ID: "failed", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"bad"}`}})
	done, msg := a.runTool(context.Background(), 2, llm.ToolCall{ID: "finish", Function: llm.FunctionCall{Name: toolFinish, Arguments: `{"summary":"ready","success":true,"verification_call_ids":["failed"]}`}})
	if done || !strings.Contains(msg.Content, "did not succeed") {
		t.Fatalf("failed command accepted as verification or reason missing: %s", msg.Content)
	}
}

// A real model (bailu-auto) ran four separate checks and cited all of them;
// being told only "not current" it reran the checks until the step cap. The
// rejection must say which IDs failed for what reason and which are accepted.
func TestRejectedEvidenceExplainsEachIDAndListsCurrentOnes(t *testing.T) {
	a := New(Config{Executor: newFakeExec()})
	call := func(id, name, args string) llm.Message {
		_, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: id, Function: llm.FunctionCall{Name: name, Arguments: args}})
		return msg
	}
	call("os", toolRunCommand, `{"command":"cat /etc/os-release"}`)
	call("read", toolReadFile, `{"path":"/etc/hosts"}`)
	call("mem", toolRunCommand, `{"command":"free -h"}`)
	msg := call("done", toolFinish, `{"summary":"ok","success":true,"verification_call_ids":["os","read","invented","mem"]}`)
	for _, want := range []string{`"os" is no longer current`, `"read" is not a run_command or wait_for call`, `"invented" is not a run_command`, "Currently accepted: mem."} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("rejection lacks %q:\n%s", want, msg.Content)
		}
	}
	if strings.Contains(msg.Content, `"mem" `) {
		t.Errorf("the current check must not be reported as a problem:\n%s", msg.Content)
	}
	if msg := call("done2", toolFinish, `{"summary":"ok","success":true,"verification_call_ids":["mem"]}`); strings.HasPrefix(msg.Content, "error:") {
		t.Fatalf("listed ID was not accepted: %s", msg.Content)
	}
}

type cwdExecutor struct {
	Executor
	dir string
}

func (e cwdExecutor) WorkingDirectory() string { return e.dir }

// In a live test the model ran `python count.py` in a directory left by an
// earlier task. Results now state where the command ran; the operator's error
// card is unchanged.
func TestCommandResultsReportTheTerminalDirectory(t *testing.T) {
	emit, events, _ := collectEvents()
	a := New(Config{Executor: cwdExecutor{newFakeExec(), `C:\work\old-task`}, Emit: emit})
	_, ok := a.runTool(context.Background(), 1, llm.ToolCall{ID: "c", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"echo hi"}`}})
	if !strings.HasPrefix(ok.Content, "exit=0\ncwd: C:\\work\\old-task\noutput:") {
		t.Fatalf("successful result lacks cwd: %q", ok.Content)
	}
	b := New(Config{Executor: cwdExecutor{failingToolExecutor{newFakeExec()}, `C:\work\old-task`}, Emit: emit})
	_, bad := b.runTool(context.Background(), 1, llm.ToolCall{ID: "f", Function: llm.FunctionCall{Name: toolRunCommand, Arguments: `{"command":"bad"}`}})
	if !strings.Contains(bad.Content, "\ncwd: C:\\work\\old-task\n") {
		t.Fatalf("failed result lacks cwd: %q", bad.Content)
	}
	for _, e := range *events {
		if e.Type == EventError && strings.Contains(e.Text, "cwd:") {
			t.Fatalf("operator error card should not repeat cwd: %q", e.Text)
		}
	}
}

type blockingRunExecutor struct {
	Executor
	started chan struct{}
}

func (e blockingRunExecutor) Run(ctx context.Context, _ string, _ time.Duration, _ func(string)) (int, string, error) {
	close(e.started)
	<-ctx.Done()
	return -1, "", fmt.Errorf("command cancelled: %w", ctx.Err())
}

// A live stop during a running command ended the run without the "已停止"
// status, which was only emitted when the stop landed between steps.
func TestStopDuringACommandReportsStoppedOnce(t *testing.T) {
	model := scriptedLLM(t, []string{toolCallSSE("long", toolRunCommand, `{"command":"sleep 60"}`)})
	defer model.Close()
	emit, events, _ := collectEvents()
	exec := blockingRunExecutor{newFakeExec(), make(chan struct{})}
	a := New(Config{LLM: llm.New(llm.Config{BaseURL: model.URL, APIKey: "test"}), Executor: exec, Emit: emit})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-exec.started; cancel() }()
	if err := a.Run(ctx, "run something long"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	stopped := 0
	for _, e := range *events {
		if e.Type == EventStatus && e.Text == "已停止。" {
			stopped++
		}
		if e.Type == EventFinal {
			t.Fatalf("a stopped run must not report a final result: %+v", e)
		}
	}
	if stopped != 1 {
		t.Fatalf("want exactly one stopped status, got %d", stopped)
	}
}

// The final summary is rendered as Markdown. Raw output under a "----" row
// would become a heading, and lines would merge into one paragraph.
func TestEvidenceIsFencedSoOutputStaysLiteral(t *testing.T) {
	output := "exit=0\nName  Id\n----  --\npython 42\n*not emphasis*"
	block := evidenceBlock("call-1", "Get-Process python | Format-Table Name, Id", output)
	want := "call-1\n```text\n> Get-Process python | Format-Table Name, Id\n" + output + "\n```"
	if block != want {
		t.Fatalf("block:\n%s\nwant:\n%s", block, want)
	}
	tricky := evidenceBlock("call-2", "echo", "```\ninner ``` fence")
	if !strings.HasPrefix(tricky, "call-2\n````text\n") || !strings.HasSuffix(tricky, "\n````") {
		t.Fatalf("fence must outgrow backticks in the output:\n%s", tricky)
	}
}
