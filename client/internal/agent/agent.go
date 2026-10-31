package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// EventType classifies agent events streamed to the UI.
type EventType string

const (
	EventActivity EventType = "activity" // observed execution phase, not model reasoning
	EventStatus   EventType = "status"
	EventThinking EventType = "thinking"  // assistant content delta
	EventToolCall EventType = "tool_call" // a tool is about to run
	EventOutput   EventType = "output"    // live command output chunk
	EventToolDone EventType = "tool_done" // tool finished
	EventFinal    EventType = "final"     // task complete
	EventError    EventType = "error"
	// EventUser is an operator follow-up message in a continued session. The
	// server records it; the agent loop never emits it.
	EventUser EventType = "user"
)

// Event is a single streamed update.
type Event struct {
	ID             int64     `json:"id,omitempty"`
	TaskID         string    `json:"taskId,omitempty"`
	Type           EventType `json:"type"`
	Text           string    `json:"text,omitempty"`
	Tool           string    `json:"tool,omitempty"`
	Command        string    `json:"command,omitempty"`
	Path           string    `json:"path,omitempty"`
	ExitCode       int       `json:"exitCode,omitempty"`
	Success        bool      `json:"success,omitempty"`
	Step           int       `json:"step,omitempty"`
	State          string    `json:"state,omitempty"`
	Timestamp      int64     `json:"timestamp,omitempty"` // server Unix time in milliseconds
	CallID         string    `json:"callId,omitempty"`
	Hostname       string    `json:"hostname,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	DurationMs     *int64    `json:"durationMs,omitempty"`
	TimeoutSeconds int       `json:"timeoutSeconds,omitempty"`
	LogPath        string    `json:"logPath,omitempty"`
	RequestID      string    `json:"requestId,omitempty"`
}

// MarshalJSON preserves explicit tool/task outcomes and command exit code zero.
// Neutral final replies have no success/failure; file tools have no process exit code.
func (e Event) MarshalJSON() ([]byte, error) {
	type wireEvent Event
	var code *int
	var success *bool
	if e.Type == EventToolDone && e.Tool == toolRunCommand {
		code = &e.ExitCode
	}
	if e.Type == EventToolDone || (e.Type == EventFinal && e.State != "reply" && e.State != "needs_input") {
		success = &e.Success
	}
	return json.Marshal(struct {
		wireEvent
		ExitCode *int  `json:"exitCode,omitempty"`
		Success  *bool `json:"success,omitempty"`
	}{wireEvent(e), code, success})
}

// Config configures an Agent.
type Config struct {
	LLM      *llm.Client
	Executor Executor
	Auditor  Auditor
	// MaxSteps caps model turns per run; zero or less means no cap.
	MaxSteps int
	// ContextBytes bounds the serialized model request, including tools. Zero
	// selects the default; this is a byte budget, not a tokenizer estimate.
	ContextBytes int
	// Emit receives events; must be safe to call from the Run goroutine.
	Emit func(Event)
	// Resume continues an earlier conversation: Run's task is appended as a
	// follow-up instruction instead of starting a fresh context.
	Resume *Conversation
	// ApplyPending commits accepted instructions at a complete model/tool-step
	// boundary. The owner persists the returned checkpoint before acknowledging
	// application, and must never invoke apply after cancellation.
	ApplyPending func(apply func([]Instruction) *Conversation) error
	// TryFinalize atomically arbitrates a normal final reply against new queued
	// instructions. A false result keeps the run open for its next boundary.
	TryFinalize func(Event) bool
	// InitialInstructions precede the new instruction on an explicit continuation.
	InitialInstructions []Instruction
	// FollowUpID identifies the ordinary continuation accepted durably by the
	// server, independently of viewer reconnects or model execution.
	FollowUpID string
}

// Conversation is the model-visible state of a finished run. It can seed a
// later run so the operator can continue a task (e.g. after a stop, an
// optional step cap, or to supply information the model asked for).
type Conversation struct {
	window               *contextWindow
	appliedSteers        map[string]bool
	consumedInstructions map[string]bool
}

// Agent drives a tool-using model loop to accomplish a deployment task.
type Agent struct {
	llm                  *llm.Client
	exec                 Executor
	audit                Auditor
	maxSteps             int
	contextBytes         int
	emit                 func(Event)
	resume               *Conversation
	window               *contextWindow
	verification         map[string]verificationRecord
	checks               map[string]bool // run_command/wait_for IDs this run; true once succeeded
	appliedSteers        map[string]bool
	applyPending         func(func([]Instruction) *Conversation) error
	tryFinalize          func(Event) bool
	finishDeferred       bool
	initialInstructions  []Instruction
	followUpID           string
	consumedInstructions map[string]bool
}

// Turn-level retry limits. Interrupted streams executed no tools, and an
// overflowing request can be compacted further, so both are safe to repeat.
const (
	maxStreamRetries   = 2
	maxOverflowRetries = 2
	minContextBytes    = 16 * 1024
)

// streamRetryDelay is a variable so tests can shorten it.
var streamRetryDelay = time.Second

// Conversation returns the state of the last Run, or nil if it never started
// a model conversation. Every assistant tool call in it has a tool result, so
// it is valid to resume.
func (a *Agent) Conversation() *Conversation {
	if a.window == nil {
		return nil
	}
	return &Conversation{window: a.window, appliedSteers: a.appliedSteers, consumedInstructions: a.consumedInstructions}
}

// New creates an Agent, applying defaults.
func New(cfg Config) *Agent {
	if cfg.ContextBytes <= 0 {
		cfg.ContextBytes = DefaultContextBytes
	}
	if cfg.Executor == nil {
		cfg.Executor = NewShellExecutor()
	}
	if cfg.Auditor == nil {
		cfg.Auditor = NewNopAuditor()
	}
	if cfg.Emit == nil {
		cfg.Emit = func(Event) {}
	}
	return &Agent{
		llm:          cfg.LLM,
		exec:         cfg.Executor,
		audit:        cfg.Auditor,
		maxSteps:     cfg.MaxSteps,
		contextBytes: cfg.ContextBytes,
		emit: func(e Event) {
			e.Timestamp = time.Now().UnixMilli()
			cfg.Emit(e)
		},
		resume:              cfg.Resume,
		applyPending:        cfg.ApplyPending,
		tryFinalize:         cfg.TryFinalize,
		initialInstructions: cfg.InitialInstructions,
		followUpID:          cfg.FollowUpID,
	}
}

// completeTurn gives every tool call in the assistant message a result, so a
// stopped or finished turn never leaves an unpaired call in the history.
func completeTurn(turn []llm.Message) []llm.Message {
	if len(turn) == 0 {
		return turn
	}
	answered := map[string]bool{}
	for _, m := range turn[1:] {
		if m.Role == llm.RoleTool {
			answered[m.ToolCallID] = true
		}
	}
	for _, tc := range turn[0].ToolCalls {
		if !answered[tc.ID] {
			turn = append(turn, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Name: tc.Function.Name, Content: "error: not executed (the task ended before this call)"})
		}
	}
	return turn
}

// streamTurn requests one model turn. It retries a connection that dropped
// mid-response and, when the provider reports a context overflow, compacts to
// a smaller budget and asks again. budget persists across turns.
func (a *Agent) streamTurn(ctx context.Context, step int, window *contextWindow, tools []llm.Tool, budget *int) (*llm.ChatResult, error) {
	interrupted, overflowed := 0, 0
	for {
		req, compacted, err := window.prepare(a.llm.Model(), tools, *budget)
		if err != nil {
			a.emit(Event{Type: EventError, Text: err.Error()})
			return nil, err
		}
		if compacted {
			a.emit(Event{Type: EventStatus, Text: "已压缩较早的上下文，保留任务目标与执行摘要；缺少的细节可重新检查。", Step: step})
			a.audit.Append(AuditEntry{Kind: "context_compaction", Note: fmt.Sprintf("request_bytes=%d budget=%d", requestBytes(req), *budget)})
		}
		a.emit(Event{Type: EventActivity, State: "generating", Text: "等待模型响应", Step: step})
		requestCtx := llm.WithRetryWait(ctx, func(attempt int, delay time.Duration) {
			a.emit(Event{Type: EventStatus, Step: step, Text: fmt.Sprintf("模型接口暂不可用，等待 %s 后重试（第 %d 次）。", delay.Round(time.Second), attempt)})
		})
		res, err := a.llm.StreamChat(requestCtx, req, func(delta string) {
			a.emit(Event{Type: EventThinking, Text: delta})
		})
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case llm.IsStreamInterrupted(err) && interrupted < maxStreamRetries:
			interrupted++
			a.emit(Event{Type: EventStatus, Text: fmt.Sprintf("模型连接中断，本轮没有执行任何工具，正在重新请求（第 %d 次）；上面可能出现重复的文字。", interrupted), Step: step})
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(streamRetryDelay * time.Duration(interrupted)):
			}
			continue
		case llm.IsContextOverflow(err) && overflowed < maxOverflowRetries && *budget > minContextBytes:
			overflowed++
			*budget = max(minContextBytes, *budget*2/3)
			a.emit(Event{Type: EventStatus, Text: fmt.Sprintf("模型提示上下文超长，已收紧到 %d 字节并重新请求。可调低 DP_CONTEXT_BYTES 避免这一步。", *budget), Step: step})
			continue
		}
		a.emit(Event{Type: EventError, Text: a.llm.Redact(err.Error())})
		return nil, err
	}
}

// Run executes the task loop until the model finishes, an optional step cap is
// reached, or ctx is cancelled.
func (a *Agent) Run(ctx context.Context, task string) (runErr error) {
	// A stop can win at any point (model wait, a running tool, a step
	// boundary). Report it once, whichever path returned.
	defer func() {
		if runErr != nil && errors.Is(ctx.Err(), context.Canceled) {
			a.emit(Event{Type: EventStatus, Text: "已停止。"})
		}
	}()
	a.verification, a.checks = nil, nil
	a.audit.Append(AuditEntry{Kind: "task", Note: task})
	var window *contextWindow
	if a.resume != nil && a.resume.window != nil {
		// Historical machine facts and successful checks are not current evidence.
		window = a.resume.window
		a.appliedSteers = a.resume.appliedSteers
		a.consumedInstructions = a.resume.consumedInstructions
		a.window = window
		// The server already accepted this message. Preserve it even when stop
		// wins before Run starts; this checkpoint makes no model/tool execution.
		a.appendInstructions(a.initialInstructions)
		window.addFollowUp(task)
		if a.followUpID != "" {
			a.appliedSteers[instructionKey(Instruction{RequestID: a.followUpID, Kind: "followup"})] = true
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		window.prefix[0].Content = systemPrompt
		window.prefix[1].Content = "当前重新检查的机器环境：\n" + a.exec.MachineInfo(ctx)
	} else {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.emit(Event{Type: EventActivity, State: "preparing", Text: "正在收集机器环境"})
		info := a.exec.MachineInfo(ctx)
		window = newContextWindow([]llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleSystem, Content: "目标机器平台: " + runtime.GOOS + "/" + runtime.GOARCH + "\n目标机器环境信息:\n" + info + "\n" + newSessionNote},
			{Role: llm.RoleUser, Content: task},
		})
	}
	a.window = window
	tools := toolSet()
	budget := a.contextBytes
	nudged := false
	var repetition repetitionGuard

	// Like pi and oh-my-pi, a run has no turn cap by default: it ends with a
	// direct plain reply, finish, repeated empty responses, failure, or cancellation.
	for step := 1; a.maxSteps <= 0 || step <= a.maxSteps; step++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := a.applyInstructions(ctx); err != nil {
			return err
		}

		res, err := a.streamTurn(ctx, step, window, tools, &budget)
		if err != nil {
			return err
		}

		turn := []llm.Message{res.Message}
		if res.FinishReason != "stop" && res.FinishReason != "tool_calls" {
			err := fmt.Errorf("model response did not finish normally: %s", res.FinishReason)
			a.emit(Event{Type: EventError, Text: err.Error()})
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		if len(res.Message.ToolCalls) == 0 {
			content := strings.TrimSpace(res.Message.Content)
			if content != "" {
				window.turns = append(window.turns, turn)
				if !a.finalize(Event{Type: EventFinal, State: "reply", Text: content, Success: false}) {
					continue
				}
				a.audit.Append(AuditEntry{Kind: "final", Note: content})
				return nil
			}
			if !nudged {
				// Ask once for a valid answer; an empty model response is not a result.
				nudged = true
				window.turns = append(window.turns, append(turn, llm.Message{Role: llm.RoleUser, Content: emptyReplyNudge}))
				continue
			}
			window.turns = append(window.turns, turn)
			content = "模型没有返回有效答复。请查看上面的执行记录，或换个说法重新下达任务。"
			a.emit(Event{Type: EventFinal, Text: content, Success: false})
			a.audit.Append(AuditEntry{Kind: "final", Note: content})
			return fmt.Errorf("model returned no valid answer after reminder")
		}

		// Record the turn whatever happens below, with every call answered, so
		// the conversation stays valid if the operator continues it later.
		finished := false
		a.finishDeferred = false
		repeated := false
		for _, tc := range res.Message.ToolCalls {
			if ctx.Err() != nil || finished {
				break
			}
			done, toolMsg := a.runTool(ctx, step, tc)
			if !done {
				warn, stop := repetition.observe(tc, toolMsg)
				if warn {
					warning := "同一工具、参数和结果已连续出现 3 次。请换一种方法或检查新的证据；继续重复将暂停任务。"
					a.emit(Event{Type: EventStatus, Step: step, Text: warning})
					toolMsg.Content += "\n[重复保护] " + warning
				}
				repeated = stop
			}
			turn = append(turn, toolMsg)
			finished = done
			if repeated || a.finishDeferred {
				break
			}
		}
		window.turns = append(window.turns, completeTurn(turn))
		if repeated {
			message := "相同调用与结果连续重复 4 次，任务已暂停。请检查记录后继续，或补充新的信息。"
			a.emit(Event{Type: EventFinal, Text: message, Success: false, Step: step})
			a.audit.Append(AuditEntry{Kind: "repeat_guard", Note: message})
			return fmt.Errorf("task paused after repeated identical tool results")
		}
		if finished {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	a.emit(Event{Type: EventFinal, Text: fmt.Sprintf("已达到最大步数 (%d)，任务暂停。可以在该任务上继续执行，保留已有进展。", a.maxSteps), Success: false})
	return fmt.Errorf("max steps (%d) reached", a.maxSteps)
}

// runTool executes a single tool call and returns the tool-result message to
// feed back to the model. done is true when the agent should stop (finish).
func (a *Agent) runTool(ctx context.Context, step int, tc llm.ToolCall) (done bool, msg llm.Message) {
	name := tc.Function.Name
	var started time.Time
	var failureState, failureLogPath string
	announced, completed := false, false
	emit := func(e Event) {
		e.CallID = tc.ID
		e.Step = step
		if e.Type == EventToolCall {
			announced = true
			e.Hostname, _ = os.Hostname()
			e.Cwd, _ = os.Getwd()
			if state, ok := a.exec.(interface{ WorkingDirectory() string }); ok {
				e.Cwd = state.WorkingDirectory()
			}
		}
		if e.Type == EventToolDone {
			completed = true
		}
		if e.Type == EventToolDone && !started.IsZero() {
			elapsed := time.Since(started).Milliseconds()
			e.DurationMs = &elapsed
		}
		a.emit(e)
	}
	result := func(content string) llm.Message {
		if name == toolRunCommand || name == toolWaitFor {
			content += "\n[call_id: " + tc.ID + "]"
		}
		return llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Name: name, Content: content}
	}
	failure := func(content string) llm.Message {
		if failureState == "" {
			failureState = toolTerminationState(ctx.Err())
		}
		if announced && !completed {
			emit(Event{Type: EventToolDone, Tool: name, ExitCode: -1, Success: false, State: failureState, LogPath: failureLogPath})
		}
		display := content
		if failureLogPath != "" {
			display = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(display), CommandLogReference(failureLogPath)))
		}
		emit(Event{Type: EventError, Tool: name, Text: display, State: failureState, LogPath: failureLogPath})
		return result("error: " + content)
	}
	if err := ctx.Err(); err != nil {
		return false, failure(err.Error())
	}
	if err := validateToolArgs(name, tc.Function.Arguments); err != nil {
		emit(Event{Type: EventToolCall, Tool: name, Step: step})
		return false, failure("invalid arguments: " + err.Error())
	}
	// Publishing can block on a slow socket; recheck cancellation after it,
	// immediately before starting a new side effect.
	announce := func(e Event) error {
		emit(e)
		if err := ctx.Err(); err != nil {
			return err
		}
		emit(Event{Type: EventActivity, State: "executing", Tool: name, Text: "正在执行工具"})
		started = time.Now()
		return ctx.Err()
	}

	switch name {
	case toolRunCommand:
		var args runCommandArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, failure("invalid arguments: " + err.Error())
		}
		if blocked, reason := CheckCommand(args.Command); blocked {
			emit(Event{Type: EventToolCall, Tool: name, Command: args.Command, Step: step})
			a.audit.Append(AuditEntry{Kind: "blocked", Command: args.Command, Note: reason})
			return false, failure("BLOCKED: " + reason + ". Choose a safer approach.")
		}
		if runtime.GOOS == "windows" {
			if blocked, reason := CheckPowerShell(args.Command); blocked {
				emit(Event{Type: EventToolCall, Tool: name, Command: args.Command, Step: step})
				a.audit.Append(AuditEntry{Kind: "blocked", Command: args.Command, Note: reason})
				return false, failure("NOT RUN: " + reason + ".")
			}
		}

		timeoutSeconds := args.TimeoutSeconds
		if timeoutSeconds <= 0 {
			timeoutSeconds = 600
		}
		if err := announce(Event{Type: EventToolCall, Tool: name, Command: args.Command, Step: step, TimeoutSeconds: timeoutSeconds}); err != nil {
			return false, failure(err.Error())
		}
		timeout := time.Duration(args.TimeoutSeconds) * time.Second
		a.verification = nil
		a.noteCheck(tc.ID)
		exit, out, err := a.exec.Run(ctx, args.Command, timeout, func(chunk string) {
			emit(Event{Type: EventOutput, Text: chunk})
		})
		a.audit.Append(AuditEntry{Kind: "command", Command: args.Command, ExitCode: exit, Output: out})
		failureState = toolTerminationState(err)
		failureLogPath = commandOutputLogPath(out)
		emit(Event{Type: EventToolDone, Tool: name, ExitCode: exit, Success: err == nil && exit == 0, Step: step, State: failureState, LogPath: failureLogPath})
		// The terminal persists across commands and tasks, so a relative path
		// resolves against wherever the last command left it. Tell the model.
		withCwd := func(m llm.Message) llm.Message {
			cwd := a.workingDirectory()
			if cwd == "" {
				return m
			}
			if i := strings.Index(m.Content, "\n"); i >= 0 {
				m.Content = m.Content[:i] + "\ncwd: " + cwd + m.Content[i:]
			} else {
				m.Content += "\ncwd: " + cwd
			}
			return m
		}
		if err != nil {
			diagnostic := fmt.Sprintf("exit=%d error=%s\noutput:\n%s", exit, err.Error(), out)
			if failureState != "" {
				display := "命令已取消"
				if failureState == "timeout" {
					display = fmt.Sprintf("命令已超过 %d 秒时限", timeoutSeconds)
				}
				message := failure(display)
				// Keep the original error and output available to the model without
				// exposing implementation errors as the operator's status label.
				message.Content = result("error: " + diagnostic).Content
				return false, withCwd(message)
			}
			return false, withCwd(failure(diagnostic))
		}
		if exit != 0 {
			return false, withCwd(failure(fmt.Sprintf("exit=%d\noutput:\n%s", exit, out)))
		}
		a.rememberVerification(tc.ID, args.Command, fmt.Sprintf("exit=%d\n%s", exit, out))
		return false, withCwd(result(fmt.Sprintf("exit=%d\noutput:\n%s", exit, out)))

	case toolWaitFor:
		var args waitForArgs
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		command := args.Kind + ": " + args.Target
		if args.Kind == "log" {
			command += " contains: " + args.Contains
		}
		if err := announce(Event{Type: EventToolCall, Tool: name, Command: command}); err != nil {
			return false, failure(err.Error())
		}
		a.noteCheck(tc.ID)
		out, err := waitFor(ctx, a.exec, args, func(elapsed time.Duration, observation string) {
			emit(Event{Type: EventActivity, State: "waiting", Tool: name, Text: waitForActivityText(args, elapsed, observation)})
		})
		a.audit.Append(AuditEntry{Kind: "wait_for", Note: args.Kind + ": " + args.Target, Output: out})
		if err != nil {
			failureState = toolTerminationState(err)
			return false, failure(err.Error())
		}
		emit(Event{Type: EventOutput, Text: out})
		emit(Event{Type: EventToolDone, Tool: name, Success: true})
		a.rememberVerification(tc.ID, "wait_for "+args.Kind+" "+args.Target, out)
		return false, result(out)

	case toolReadFile:
		var args readFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, failure("invalid arguments: " + err.Error())
		}
		if err := announce(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step}); err != nil {
			return false, failure(err.Error())
		}
		r, err := a.exec.ReadFile(args.Path, args.Offset, args.MaxBytes)
		a.audit.Append(AuditEntry{Kind: "read_file", Path: args.Path})
		if err != nil {
			return false, failure(err.Error())
		}
		emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Success: true, Step: step})
		header := fmt.Sprintf("[file %s | size=%d bytes | shown %d-%d]", args.Path, r.Size, r.Offset, r.NextOffset)
		if !r.EOF {
			header += fmt.Sprintf(" more remains; read next with offset=%d", r.NextOffset)
		}
		return false, result(header + "\n" + r.Content)

	case toolWriteFile:
		var args writeFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, failure("invalid arguments: " + err.Error())
		}
		if err := announce(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step}); err != nil {
			return false, failure(err.Error())
		}
		a.verification = nil
		err := a.exec.WriteFile(args.Path, args.Content)
		a.audit.Append(AuditEntry{Kind: "write_file", Path: args.Path, Output: fmt.Sprintf("%d bytes", len(args.Content))})
		if err != nil {
			return false, failure(err.Error())
		}
		emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Success: true, Step: step})
		return false, result("ok: wrote " + args.Path)

	case toolEditFile:
		var args editFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, failure("invalid arguments: " + err.Error())
		}
		if err := announce(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step}); err != nil {
			return false, failure(err.Error())
		}
		a.verification = nil
		count, err := a.exec.EditFile(args.Path, args.OldString, args.NewString, args.ReplaceAll)
		a.audit.Append(AuditEntry{Kind: "edit_file", Path: args.Path, Output: fmt.Sprintf("%d replacement(s)", count)})
		if err != nil {
			return false, failure(err.Error())
		}
		emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Success: true, Step: step})
		return false, result(fmt.Sprintf("ok: edited %s (%d replacement(s))", args.Path, count))

	case toolAppendFile:
		var args appendFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, failure("invalid arguments: " + err.Error())
		}
		if err := announce(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step}); err != nil {
			return false, failure(err.Error())
		}
		a.verification = nil
		err := a.exec.AppendFile(args.Path, args.Content)
		a.audit.Append(AuditEntry{Kind: "append_file", Path: args.Path, Output: fmt.Sprintf("%d bytes", len(args.Content))})
		if err != nil {
			return false, failure(err.Error())
		}
		emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Success: true, Step: step})
		return false, result("ok: appended to " + args.Path)

	case toolFinish:
		var args finishArgs
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		summary := args.Summary
		if args.Success {
			evidence, err := a.finishEvidence(args.VerificationCallIDs)
			if err != nil {
				emit(Event{Type: EventToolCall, Tool: name})
				return false, failure(err.Error())
			}
			summary += "\n\n实际验证记录：\n" + evidence
		}
		state := ""
		if args.Outcome == "needs_input" {
			state = "needs_input"
		}
		if !a.finalize(Event{Type: EventFinal, State: state, Text: summary, Success: args.Success}) {
			a.finishDeferred = true
			return false, result("not finished: new operator instructions are pending; apply them before checking the updated task again")
		}
		a.audit.Append(AuditEntry{Kind: "final", Note: summary})
		return true, result("acknowledged")

	default:
		return false, failure("unknown tool " + name)
	}
}

// workingDirectory is the terminal's current directory when the executor
// keeps one (the shared terminal does), otherwise "".
func (a *Agent) workingDirectory() string {
	if state, ok := a.exec.(interface{ WorkingDirectory() string }); ok {
		return state.WorkingDirectory()
	}
	return ""
}

func toolTerminationState(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return ""
}

// Executors append the authoritative log reference after command output. Use
// the last reference so an earlier line of process output cannot replace it.
func commandOutputLogPath(output string) string {
	var path string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[full output: ") && strings.HasSuffix(line, "]") {
			path = strings.TrimSuffix(strings.TrimPrefix(line, "[full output: "), "]")
		}
	}
	return path
}

// newSessionNote opens a fresh conversation. The persistent terminal keeps
// the directory of whatever ran before, and a model that finds a server.py
// there tends to treat it as the thing to deploy.
const newSessionNote = "这是一个新会话，没有之前的对话。终端的当前目录可能是更早的其他任务留下的，其中的文件、服务和进程不是本任务的对象，除非任务明确指向它们。"

// emptyReplyNudge requests a useful response after an empty model turn.
const emptyReplyNudge = "本轮没有有效答复；若还需操作请继续使用工具，否则给出自然语言结果。若要报告部署成功，必须先完成真实检查，再调用 finish(success=true) 并引用验证证据；任务确实失败时调用 finish(success=false)；如果缺少操作员才能提供的信息，调用 finish(outcome=needs_input, success=false) 并提出具体问题。"

// systemPrompt encodes the agent's role and deployment know-how.
var systemPrompt = strings.TrimSpace(`
你是 DeployPilot，一个运行在客户机器上的自动化部署工程师。你的目标是完成操作员交给你的部署任务，全程自主执行命令，直到任务完成或确实无法继续。按目标平台和工具说明选择命令语法：Linux 用 bash，Windows 用 Windows PowerShell。

工作方式:
- 使用 run_command 执行 shell 命令来检查环境、安装依赖、下载模型、启动服务并验证。先探查再动手，不要臆断。
- 每一步说明你要做什么、为什么这么做，再调用工具。
- 遇到报错先读取日志和错误输出，定位根因后再修复，不要盲目重试。
- 问候和普通问答可以直接自然语言回答。若有助于完成请求，可以先检查相关文件或环境，再用自然语言解释结果；不必仅为结束任务而调用 finish。
- 只有在明确报告部署成功且已完成真实检查时，才调用 finish(success:true) 总结并提供验证证据；任务真实失败时可调用 finish(success:false)。缺少只有操作员能提供的信息时，调用 finish(outcome:"needs_input", success:false)，在 summary 里提出具体问题；这表示等待补充信息，不表示任务失败。
- 成功完成前执行真实检查（服务接口、健康状态、预期文件等），finish(success=true) 的 verification_call_ids 必须引用 run_command 或 wait_for 返回的 call_id。只有最近一次 run_command（及其后的 wait_for）算当前证据，任何新命令或文件修改都会使更早的验证失效；请把所有最终检查合并到 finish 前的一条 run_command 中，只引用它。不能只填写一段声称成功的文字。
- 同一调用连续得到相同结果时换方法，优先读完整日志；不要重复运行无变化的命令。第三次会收到提醒，第四次任务会暂停。

文件操作:
- read_file 只用来读文本文件（配置、日志、脚本）。它会返回文件总大小和 next offset；文件很大时用 offset 分段续读。
- 绝对不要用 read_file 读模型权重等二进制大文件（.safetensors/.gguf/.bin）。模型只需要用 run_command 下载、用 ls -lh/du -sh 确认大小、把路径传给 vLLM/Ollama 加载，从不需要读取其内容。
- 大日志用 run_command 配合 grep/tail/sed 抓关键行，不要整文件读。
- 命令结果末尾的 full output 路径保存完整输出，中间报错被截短时到该文件搜索。write_file/edit_file 覆盖已有文件前会保存同目录 .bak-* 备份。
- 改已有文件用 edit_file（按唯一片段替换），新建或整体重写用 write_file，往末尾追加用 append_file。文件超大时用 run_command 的 sed/awk 修改。

安全:
- 不要执行破坏性命令（删库、格式化磁盘、关机重启等）。这类命令会被系统拦截。
- 优先使用非交互式命令（加 -y、DEBIAN_FRONTEND=noninteractive 等），避免卡在交互提示。

执行环境（重要）:
- 命令在操作员可见的同一个持久终端里依次执行，目录和环境变量会保留。Linux 用 cd/export；需要遇错即停时可以用 &&，或包成 bash -c '...'。不要在命令里用 exit 退出 shell，也不要单独执行 set -e。
- Windows 使用 Windows PowerShell 5.1，不支持 &&、||、export 或 bash 语法。用 Set-Location/$env:NAME 修改目录和环境；相关命令分开调用，或用分号并在失败时通过 if ($LASTEXITCODE -ne 0) / -ErrorAction Stop 中止当前操作。Start-Process 的 stdout 和 stderr 必须重定向到不同文件，使用 -WindowStyle Hidden；在需要读取 PID 时用 -PassThru。
- Windows 双引号字符串中，变量后紧跟冒号时使用 ${name} 或格式运算符（如 '{0}: exists' -f $name），不要写 "$name: exists"；后者会被解析为作用域/驱动器变量并报语法错误。
- 你无法回答任何交互提示：没有人能替你输入密码、y/n 或编辑器内容。sudo 用 sudo -n（环境信息里写明了是否有免密 sudo）；不要打开 vim/less/top 等全屏程序，查看内容用 cat/head/tail/grep，查进程用 ps。
- 长期运行的服务（vLLM、Ollama、Web 服务等）不要放在前台运行，否则命令会一直等到超时并被中断、服务随之被杀。用 nohup <命令> > /tmp/<名称>.log 2>&1 & 在后台启动（能用 systemd 或 docker -d 更好），再用 sleep、tail 日志和 curl 轮询确认就绪。Windows 用 Start-Process 并重定向日志。
- 预计较慢的命令（大模型下载、pip 安装大包、构建镜像）设置 timeout_seconds（最长 3600），或改为后台执行后轮询进度。
- 非零退出码不一定是故障：grep 没匹配、which/test 未找到都会返回 1，根据输出判断。
- 等待网址可访问、日志出现某句话或进程退出时用 wait_for，一次调用在程序内等待，不要反复 sleep/curl 占用模型轮次。
- Windows 网络探测请显式使用 curl.exe --connect-timeout 10 --max-time 20，或 Invoke-WebRequest -Uri <网址> -Method Head -UseBasicParsing -TimeoutSec 20。Windows PowerShell 中 curl 可能是 Invoke-WebRequest 别名，不要使用 curl -I 这类会触发缺少必填参数交互的写法；网络探测应设置短时限。
- 部署对象（哪个项目或目录）或部署位置（"测试服务器"之类的另一台机器、地址、域名）在任务和环境中无法确定时，不要拿当前目录、旧任务留下的文件或已在运行的服务代替，也不要据此报告成功；调用 finish(outcome:"needs_input", success:false) 问清楚。
- 缺少操作员才能提供的信息（密码、令牌、版本选择等）而不能继续当前任务时，必须调用 finish(outcome:"needs_input", success:false)，在 summary 里写清具体问题；不要只用普通自然回复提出问题后结束。这样界面才会显示等待补充信息，操作员继续该任务后已有进展会保留。普通问答或已经回答完毕的说明仍可自然回复结束。

部署知识:
- ModelScope 下载模型: 用 pip install modelscope，然后 modelscope download --model <id> --local_dir <dir>；国内网络通常可直连 modelscope.cn。HuggingFace 不通时可设 HF_ENDPOINT=https://hf-mirror.com。
- vLLM: pip install vllm；用 python -m vllm.entrypoints.openai.api_server --model <path> --port 8000 启动 OpenAI 兼容服务；显存不足时调小 --gpu-memory-utilization、--max-model-len，或加 --quantization。注意 CUDA 与 PyTorch 版本匹配。
- Ollama: curl -fsSL https://ollama.com/install.sh | sh 安装；ollama serve 启动；ollama run <model>。
- Docker: 检查 docker 是否安装与可用；GPU 需要 nvidia-container-toolkit 并加 --gpus all。
- CUDA/GPU: 用 nvidia-smi 确认驱动与显存；"CUDA out of memory" 要减小 batch/模型或量化；驱动与 CUDA runtime 不匹配是常见坑。
- 启动服务后，用 curl 验证端口和接口确实可用，再报告完成。
`)
