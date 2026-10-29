package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// EventType classifies agent events streamed to the UI.
type EventType string

const (
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
	Type     EventType `json:"type"`
	Text     string    `json:"text,omitempty"`
	Tool     string    `json:"tool,omitempty"`
	Command  string    `json:"command,omitempty"`
	Path     string    `json:"path,omitempty"`
	ExitCode int       `json:"exitCode,omitempty"`
	Success  bool      `json:"success,omitempty"`
	Step     int       `json:"step,omitempty"`
}

// Config configures an Agent.
type Config struct {
	LLM      *llm.Client
	Executor Executor
	Auditor  Auditor
	MaxSteps int
	// Emit receives events; must be safe to call from the Run goroutine.
	Emit func(Event)
}

// Agent drives a tool-using model loop to accomplish a deployment task.
type Agent struct {
	llm      *llm.Client
	exec     Executor
	audit    Auditor
	maxSteps int
	emit     func(Event)
}

// New creates an Agent, applying defaults.
func New(cfg Config) *Agent {
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 30
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
		llm:      cfg.LLM,
		exec:     cfg.Executor,
		audit:    cfg.Auditor,
		maxSteps: cfg.MaxSteps,
		emit:     cfg.Emit,
	}
}

// Run executes the task loop until the model finishes, the step budget is
// exhausted, or ctx is cancelled.
func (a *Agent) Run(ctx context.Context, task string) error {
	a.audit.Append(AuditEntry{Kind: "task", Note: task})
	a.emit(Event{Type: EventStatus, Text: "正在收集机器环境信息…"})
	info := a.exec.MachineInfo(ctx)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleSystem, Content: "目标机器环境信息:\n" + info},
		{Role: llm.RoleUser, Content: task},
	}
	tools := toolSet()
	nudged := false

	for step := 1; step <= a.maxSteps; step++ {
		select {
		case <-ctx.Done():
			a.emit(Event{Type: EventStatus, Text: "已停止。"})
			return ctx.Err()
		default:
		}

		a.emit(Event{Type: EventStatus, Text: fmt.Sprintf("第 %d 步：模型思考中…", step), Step: step})

		res, err := a.llm.StreamChat(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    tools,
		}, func(delta string) {
			a.emit(Event{Type: EventThinking, Text: delta})
		})
		if err != nil {
			a.emit(Event{Type: EventError, Text: a.llm.Redact(err.Error())})
			return err
		}

		messages = append(messages, res.Message)

		if len(res.Message.ToolCalls) == 0 {
			content := strings.TrimSpace(res.Message.Content)
			if content == "" && !nudged {
				// The model stopped without a summary; ask once for one.
				nudged = true
				messages = append(messages, llm.Message{Role: llm.RoleUser, Content: emptyReplyNudge})
				continue
			}
			if content == "" {
				content = "模型没有给出总结。请查看上面的执行记录，或换个说法重新下达任务。"
			}
			// No tool calls: treat content as the final answer.
			a.emit(Event{Type: EventFinal, Text: content, Success: true})
			a.audit.Append(AuditEntry{Kind: "final", Note: content})
			return nil
		}

		for _, tc := range res.Message.ToolCalls {
			done, toolMsg := a.runTool(ctx, step, tc)
			messages = append(messages, toolMsg)
			if done {
				return nil
			}
		}
	}

	a.emit(Event{Type: EventFinal, Text: fmt.Sprintf("已达到最大步数 (%d)，任务终止。", a.maxSteps), Success: false})
	return fmt.Errorf("max steps (%d) reached", a.maxSteps)
}

// runTool executes a single tool call and returns the tool-result message to
// feed back to the model. done is true when the agent should stop (finish).
func (a *Agent) runTool(ctx context.Context, step int, tc llm.ToolCall) (done bool, msg llm.Message) {
	name := tc.Function.Name
	result := func(content string) llm.Message {
		return llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Name: name, Content: content}
	}

	switch name {
	case toolRunCommand:
		var args runCommandArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, result("error: invalid arguments: " + err.Error())
		}
		if blocked, reason := CheckCommand(args.Command); blocked {
			a.emit(Event{Type: EventToolCall, Tool: name, Command: args.Command, Step: step})
			a.emit(Event{Type: EventError, Text: reason})
			a.audit.Append(AuditEntry{Kind: "blocked", Command: args.Command, Note: reason})
			return false, result("BLOCKED: " + reason + ". Choose a safer approach.")
		}

		a.emit(Event{Type: EventToolCall, Tool: name, Command: args.Command, Step: step})
		timeout := time.Duration(args.TimeoutSeconds) * time.Second
		exit, out, err := a.exec.Run(ctx, args.Command, timeout, func(chunk string) {
			a.emit(Event{Type: EventOutput, Text: chunk})
		})
		a.audit.Append(AuditEntry{Kind: "command", Command: args.Command, ExitCode: exit, Output: out})
		a.emit(Event{Type: EventToolDone, Tool: name, ExitCode: exit, Step: step})
		if err != nil {
			return false, result(fmt.Sprintf("exit=%d error=%s\noutput:\n%s", exit, err.Error(), out))
		}
		return false, result(fmt.Sprintf("exit=%d\noutput:\n%s", exit, out))

	case toolReadFile:
		var args readFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, result("error: invalid arguments: " + err.Error())
		}
		a.emit(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step})
		r, err := a.exec.ReadFile(args.Path, args.Offset, args.MaxBytes)
		a.audit.Append(AuditEntry{Kind: "read_file", Path: args.Path})
		if err != nil {
			return false, result("error: " + err.Error())
		}
		a.emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Step: step})
		header := fmt.Sprintf("[file %s | size=%d bytes | shown %d-%d]", args.Path, r.Size, r.Offset, r.NextOffset)
		if !r.EOF {
			header += fmt.Sprintf(" more remains; read next with offset=%d", r.NextOffset)
		}
		return false, result(header + "\n" + r.Content)

	case toolWriteFile:
		var args writeFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, result("error: invalid arguments: " + err.Error())
		}
		a.emit(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step})
		err := a.exec.WriteFile(args.Path, args.Content)
		a.audit.Append(AuditEntry{Kind: "write_file", Path: args.Path, Output: fmt.Sprintf("%d bytes", len(args.Content))})
		if err != nil {
			return false, result("error: " + err.Error())
		}
		a.emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Step: step})
		return false, result("ok: wrote " + args.Path)

	case toolEditFile:
		var args editFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, result("error: invalid arguments: " + err.Error())
		}
		a.emit(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step})
		count, err := a.exec.EditFile(args.Path, args.OldString, args.NewString, args.ReplaceAll)
		a.audit.Append(AuditEntry{Kind: "edit_file", Path: args.Path, Output: fmt.Sprintf("%d replacement(s)", count)})
		if err != nil {
			return false, result("error: " + err.Error())
		}
		a.emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Step: step})
		return false, result(fmt.Sprintf("ok: edited %s (%d replacement(s))", args.Path, count))

	case toolAppendFile:
		var args appendFileArgs
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return false, result("error: invalid arguments: " + err.Error())
		}
		a.emit(Event{Type: EventToolCall, Tool: name, Path: args.Path, Step: step})
		err := a.exec.AppendFile(args.Path, args.Content)
		a.audit.Append(AuditEntry{Kind: "append_file", Path: args.Path, Output: fmt.Sprintf("%d bytes", len(args.Content))})
		if err != nil {
			return false, result("error: " + err.Error())
		}
		a.emit(Event{Type: EventToolDone, Tool: name, Path: args.Path, Step: step})
		return false, result("ok: appended to " + args.Path)

	case toolFinish:
		var args finishArgs
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		a.emit(Event{Type: EventFinal, Text: args.Summary, Success: args.Success})
		a.audit.Append(AuditEntry{Kind: "final", Note: args.Summary})
		return true, result("acknowledged")

	default:
		return false, result("error: unknown tool " + name)
	}
}

// emptyReplyNudge is sent when the model ends a turn with neither tool calls
// nor text.
const emptyReplyNudge = "你上一条回复是空的。请根据已经执行的结果，调用 finish 给操作员一个清晰的结论。"

// systemPrompt encodes the agent's role and deployment know-how.
var systemPrompt = strings.TrimSpace(`
你是 DeployPilot，一个运行在客户 Linux 服务器上的自动化部署工程师。你的目标是完成操作员交给你的部署任务，全程自主执行命令，直到任务完成或确实无法继续。

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

部署知识:
- ModelScope 下载模型: 用 pip install modelscope，然后 modelscope download --model <id> --local_dir <dir>；国内网络通常可直连 modelscope.cn。HuggingFace 不通时可设 HF_ENDPOINT=https://hf-mirror.com。
- vLLM: pip install vllm；用 python -m vllm.entrypoints.openai.api_server --model <path> --port 8000 启动 OpenAI 兼容服务；显存不足时调小 --gpu-memory-utilization、--max-model-len，或加 --quantization。注意 CUDA 与 PyTorch 版本匹配。
- Ollama: curl -fsSL https://ollama.com/install.sh | sh 安装；ollama serve 启动；ollama run <model>。
- Docker: 检查 docker 是否安装与可用；GPU 需要 nvidia-container-toolkit 并加 --gpus all。
- CUDA/GPU: 用 nvidia-smi 确认驱动与显存；"CUDA out of memory" 要减小 batch/模型或量化；驱动与 CUDA runtime 不匹配是常见坑。
- 启动服务后，用 curl 验证端口和接口确实可用，再报告完成。
`)
