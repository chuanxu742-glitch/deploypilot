package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestInvalidToolArgumentsHaveNoSideEffects(t *testing.T) {
	cases := []struct{ name, args string }{
		{toolRunCommand, `{}`}, {toolRunCommand, `{"command":" "}`},
		{toolRunCommand, `{"command":"echo ok","timeout_seconds":-1}`},
		{toolRunCommand, `{"command":"echo ok","timeout_seconds":2147483647}`},
		{toolRunCommand, `{"command":"echo ok","extra":true}`},
		{toolReadFile, `{"path":"x","offset":-1}`},
		{toolReadFile, `{"path":"x","max_bytes":-1}`},
		{toolWriteFile, `{"path":"x"}`},
		{toolWriteFile, `{"path":"x","content":null}`},
		{toolAppendFile, `{"path":"","content":"x"}`},
		{toolEditFile, `{"path":"x","old_string":"","new_string":"insert","replace_all":true}`},
		{toolEditFile, `{"path":"x","old_string":"old"}`},
		{toolFinish, `{}`}, {toolFinish, `{"summary":"done"}`},
		{toolFinish, `{"summary":" ","success":true}`},
		{toolFinish, `{"summary":"done","success":"yes"}`},
	}
	for _, tt := range cases {
		t.Run(tt.name+tt.args, func(t *testing.T) {
			fe := newFakeExec()
			fe.writes["x"] = "old"
			var sawError bool
			a := New(Config{Executor: fe, Emit: func(e Event) {
				if e.Type == EventError {
					sawError = true
				}
			}})
			done, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "call", Type: "function", Function: llm.FunctionCall{Name: tt.name, Arguments: tt.args}})
			if done || !strings.HasPrefix(msg.Content, "error:") || !sawError {
				t.Fatalf("invalid call not rejected visibly: done=%v result=%+v error=%v", done, msg, sawError)
			}
			if msg.ToolCallID != "call" || len(fe.commands) != 0 || len(fe.reads) != 0 || len(fe.writes) != 1 || fe.writes["x"] != "old" {
				t.Fatalf("invalid call changed state or lost ID: %+v %+v", fe, msg)
			}
		})
	}
}

func TestEmptyFileContentIsValid(t *testing.T) {
	fe := newFakeExec()
	a := New(Config{Executor: fe})
	_, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "c", Function: llm.FunctionCall{Name: toolWriteFile, Arguments: `{"path":"empty.txt","content":""}`}})
	if strings.HasPrefix(msg.Content, "error:") {
		t.Fatal(msg.Content)
	}
	if content, ok := fe.writes["empty.txt"]; !ok || content != "" {
		t.Fatal("empty file not created")
	}
}

func TestFileFailureIsEmitted(t *testing.T) {
	var errors int
	a := New(Config{Executor: NewShellExecutor(), Emit: func(e Event) {
		if e.Type == EventError {
			errors++
		}
	}})
	_, msg := a.runTool(context.Background(), 1, llm.ToolCall{ID: "c", Function: llm.FunctionCall{Name: toolReadFile, Arguments: `{"path":"` + strings.ReplaceAll(t.TempDir(), `\`, `\\`) + `/missing"}`}})
	if !strings.HasPrefix(msg.Content, "error:") || errors != 1 {
		t.Fatalf("file failure not visible: %s errors=%d", msg.Content, errors)
	}
}

func TestCancellationWhilePublishingToolCallPreventsFileOperation(t *testing.T) {
	for name, args := range map[string]string{
		toolReadFile:   `{"path":"x"}`,
		toolWriteFile:  `{"path":"x","content":"changed"}`,
		toolEditFile:   `{"path":"x","old_string":"old","new_string":"changed"}`,
		toolAppendFile: `{"path":"x","content":"changed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fe := newFakeExec()
			fe.writes["x"] = "old"
			a := New(Config{Executor: fe, Emit: func(e Event) {
				if e.Type == EventToolCall {
					cancel()
				}
			}})
			a.runTool(ctx, 1, llm.ToolCall{ID: "c", Function: llm.FunctionCall{Name: name, Arguments: args}})
			if len(fe.reads) != 0 || fe.writes["x"] != "old" {
				t.Fatalf("file operation began after cancellation during event delivery: %+v", fe)
			}
		})
	}
}
