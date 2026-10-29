package agent

import (
	"context"
	"encoding/json"
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
)

// Event is a single streamed update.
type Event struct {
	ID         int64     `json:"id,omitempty"`
	TaskID     string    `json:"taskId,omitempty"`
	Type       EventType `json:"type"`
	Text       string    `json:"text,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Command    string    `json:"command,omitempty"`
	Path       string    `json:"path,omitempty"`
	ExitCode   int       `json:"exitCode,omitempty"`
	Success    bool      `json:"success,omitempty"`
	Step       int       `json:"step,omitempty"`
	State      string    `json:"state,omitempty"`
	Timestamp  int64     `json:"timestamp,omitempty"` // server Unix time in milliseconds
	CallID     string    `json:"callId,omitempty"`
	Hostname   string    `json:"hostname,omitempty"`
	Cwd        string    `json:"cwd,omitempty"`
	DurationMs *int64    `json:"durationMs,omitempty"`
}

// MarshalJSON preserves explicit success/failure and command exit code zero,
// without giving unrelated status events or file operations a process exit code.
func (e Event) MarshalJSON() ([]byte, error) {
	type wireEvent Event
	var code *int
	var success *bool
	if e.Type == EventToolDone && e.Tool == toolRunCommand {
		code = &e.ExitCode
	}
	if e.Type == EventToolDone || e.Type == EventFinal {
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
	MaxSteps int
	// ContextBytes bounds the serialized model request, including tools. Zero
	// selects the default; this is a byte budget, not a tokenizer estimate.
	ContextBytes int
	// Emit receives events; must be safe to call from the Run goroutine.
	Emit func(Event)
	// Resume continues an earlier conversation: Run's task is appended as a
	// follow-up instruction instead of starting a fresh context.
	Resume *Conversation
}

// Conversation is the model-visible state of a finished run. It can seed a
// later run so the operator can continue a task (e.g. after the step budget
// ran out, or to supply information the model asked for).
type Conversation struct {
	window *contextWindow
}

// Agent drives a tool-using model loop to accomplish a deployment task.
type Agent struct {
	llm          *llm.Client
	exec         Executor
	audit        Auditor
	maxSteps     int
	contextBytes int
	emit         func(Event)
	resume       *Conversation
	window       *contextWindow
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
	return &Conversation{window: a.window}
}

// New creates an Agent, applying defaults.
func New(cfg Config) *Agent {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 30
	}
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
		resume: cfg.Resume,
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
		res, err := a.llm.StreamChat(ctx, req, func(delta string) {
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

// Run executes the task loop until the model finishes, the step budget is
// exhausted, or ctx is cancelled.
func (a *Agent) Run(ctx context.Context, task string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.audit.Append(AuditEntry{Kind: "task", Note: task})
	var window *contextWindow
	if a.resume != nil && a.resume.window != nil {
		// Continue the earlier conversation; the machine context is already in it.
		window = a.resume.window
		window.addFollowUp(task)
		a.emit(Event{Type: EventStatus, Text: "在上一个任务的上下文上继续执行。"})
	} else {
		a.emit(Event{Type: EventActivity, State: "preparing", Text: "正在收集机器环境"})
		info := a.exec.MachineInfo(ctx)
		window = newContextWindow([]llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleSystem, Content: "目标机器平台: " + runtime.GOOS + "/" + runtime.GOARCH + "\n目标机器环境信息:\n" + info},
			{Role: llm.RoleUser, Content: task},
		})
	}
	a.window = window
	tools := toolSet()
	budget := a.contextBytes
	nudged := false

	for step := 1; step <= a.maxSteps; step++ {
		select {
		case <-ctx.Done():
			a.emit(Event{Type: EventStatus, Text: "已停止。"})
			return ctx.Err()
		default:
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
			if !nudged {
				// A natural-language stop is not evidence of deployment success.
				// Ask once for the existing explicit completion protocol.
				nudged = true
				window.turns = append(window.turns, append(turn, llm.Message{Role: llm.RoleUser, Content: finishReplyNudge}))
				continue
			}
			window.turns = append(window.turns, turn)
			if content == "" {
				content = "模型没有给出总结。请查看上面的执行记录，或换个说法重新下达任务。"
			}
			content += "\n\n任务未确认完成：模型未调用 finish，请核对执行结果后再继续。"
			a.emit(Event{Type: EventFinal, Text: content, Success: false})
			a.audit.Append(AuditEntry{Kind: "final", Note: content})
			return fmt.Errorf("model stopped without explicit finish")
		}

		// Record the turn whatever happens below, with every call answered, so
		// the conversation stays valid if the operator continues it later.
		finished := false
		for _, tc := range res.Message.ToolCalls {
			if ctx.Err() != nil || finished {
				break
			}
			done, toolMsg := a.runTool(ctx, step, tc)
			turn = append(turn, toolMsg)
			finished = done
		}
		window.turns = append(window.turns, completeTurn(turn))
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
		return llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Name: name, Content: content}
	}
	failure := func(content string) llm.Message {
		if announced && !completed {
			emit(Event{Type: EventToolDone, Tool: name, ExitCode: -1, Success: false})
		}
		emit(Event{Type: EventError, Tool: name, Text: content})
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

		if err := announce(Event{Type: EventToolCall, Tool: name, Command: args.Command, Step: step}); err != nil {
			return false, failure(err.Error())
		}
		timeout := time.Duration(args.TimeoutSeconds) * time.Second
		exit, out, err := a.exec.Run(ctx, args.Command, timeout, func(chunk string) {
			emit(Event{Type: EventOutput, Text: chunk})
		})
		a.audit.Append(AuditEntry{Kind: "command", Command: args.Command, ExitCode: exit, Output: out})
		emit(Event{Type: EventToolDone, Tool: name, ExitCode: exit, Success: err == nil && exit == 0, Step: step})
		if err != nil {
			return false, failure(fmt.Sprintf("exit=%d error=%s\noutput:\n%s", exit, err.Error(), out))
		}
		if exit != 0 {
			return false, failure(fmt.Sprintf("exit=%d\noutput:\n%s", exit, out))
		}
		return false, result(fmt.Sprintf("exit=%d\noutput:\n%s", exit, out))

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
		emit(Event{Type: EventFinal, Text: args.Summary, Success: args.Success})
		a.audit.Append(AuditEntry{Kind: "final", Note: args.Summary})
		return true, result("acknowledged")

	default:
		return false, failure("unknown tool " + name)
	}
}

// finishReplyNudge requests an explicit outcome once; it is not an independent
// verifier. The model still owns finish.success and must cite observed results.
const finishReplyNudge = "你尚未调用 finish。若任务仍需执行，请继续使用工具；若已结束，请根据实际执行和验证结果调用 finish，填写 summary 和 success。无法完成或未验证成功时 success=false，不要只用文字宣称成功。"

// systemPrompt encodes the agent's role and deployment know-how.
var systemPrompt = strings.TrimSpace(`
你是 DeployPilot，一个运行在客户机器上的自动化部署工程师。你的目标是完成操作员交给你的部署任务，全程自主执行命令，直到任务完成或确实无法继续。按目标平台和工具说明选择命令语法：Linux 用 bash，Windows 用 Windows PowerShell。

工作方式:
- 使用 run_command 执行 shell 命令来检查环境、安装依赖、下载模型、启动服务并验证。先探查再动手，不要臆断。
- 每一步说明你要做什么、为什么这么做，再调用工具。
- 遇到报错先读取日志和错误输出，定位根因后再修复，不要盲目重试。
- 任务完成或无法继续时，调用 finish 给出清晰的总结（成果、如何验证、后续注意事项）。

文件操作:
- read_file 只用来读文本文件（配置、日志、脚本）。它会返回文件总大小和 next offset；文件很大时用 offset 分段续读。
- 绝对不要用 read_file 读模型权重等二进制大文件（.safetensors/.gguf/.bin）。模型只需要用 run_command 下载、用 ls -lh/du -sh 确认大小、把路径传给 vLLM/Ollama 加载，从不需要读取其内容。
- 大日志用 run_command 配合 grep/tail/sed 抓关键行，不要整文件读。
- 改已有文件用 edit_file（按唯一片段替换），新建或整体重写用 write_file，往末尾追加用 append_file。文件超大时用 run_command 的 sed/awk 修改。

安全:
- 不要执行破坏性命令（删库、格式化磁盘、关机重启等）。这类命令会被系统拦截。
- 优先使用非交互式命令（加 -y、DEBIAN_FRONTEND=noninteractive 等），避免卡在交互提示。

执行环境（重要）:
- 命令在操作员可见的同一个持久终端里依次执行：cd 和 export 的环境变量会保留到后续命令。不要在命令里用 exit 退出 shell，也不要单独执行 set -e；需要遇错即停时用 && 串联，或包成 bash -c '...'。
- 你无法回答任何交互提示：没有人能替你输入密码、y/n 或编辑器内容。sudo 用 sudo -n（环境信息里写明了是否有免密 sudo）；不要打开 vim/less/top 等全屏程序，查看内容用 cat/head/tail/grep，查进程用 ps。
- 长期运行的服务（vLLM、Ollama、Web 服务等）不要放在前台运行，否则命令会一直等到超时并被中断、服务随之被杀。用 nohup <命令> > /tmp/<名称>.log 2>&1 & 在后台启动（能用 systemd 或 docker -d 更好），再用 sleep、tail 日志和 curl 轮询确认就绪。Windows 用 Start-Process 并重定向日志。
- 预计较慢的命令（大模型下载、pip 安装大包、构建镜像）设置 timeout_seconds（最长 3600），或改为后台执行后轮询进度。
- 非零退出码不一定是故障：grep 没匹配、which/test 未找到都会返回 1，根据输出判断。
- 需要操作员提供的信息（密码、令牌、要选择的版本等）无法自行获得时，调用 finish（success=false），在 summary 里写清需要什么。操作员可以在该任务上继续执行并补充信息，已有进展会保留。

部署知识:
- ModelScope 下载模型: 用 pip install modelscope，然后 modelscope download --model <id> --local_dir <dir>；国内网络通常可直连 modelscope.cn。HuggingFace 不通时可设 HF_ENDPOINT=https://hf-mirror.com。
- vLLM: pip install vllm；用 python -m vllm.entrypoints.openai.api_server --model <path> --port 8000 启动 OpenAI 兼容服务；显存不足时调小 --gpu-memory-utilization、--max-model-len，或加 --quantization。注意 CUDA 与 PyTorch 版本匹配。
- Ollama: curl -fsSL https://ollama.com/install.sh | sh 安装；ollama serve 启动；ollama run <model>。
- Docker: 检查 docker 是否安装与可用；GPU 需要 nvidia-container-toolkit 并加 --gpus all。
- CUDA/GPU: 用 nvidia-smi 确认驱动与显存；"CUDA out of memory" 要减小 batch/模型或量化；驱动与 CUDA runtime 不匹配是常见坑。
- 启动服务后，用 curl 验证端口和接口确实可用，再报告完成。
`)
