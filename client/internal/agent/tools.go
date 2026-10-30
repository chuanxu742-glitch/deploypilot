package agent

import (
	"encoding/json"
	"runtime"

	"github.com/deploypilot/deploypilot/internal/llm"
)

// Tool names exposed to the model.
const (
	toolRunCommand = "run_command"
	toolReadFile   = "read_file"
	toolWriteFile  = "write_file"
	toolEditFile   = "edit_file"
	toolAppendFile = "append_file"
	toolWaitFor    = "wait_for"
	toolFinish     = "finish"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// toolSet returns the tool declarations advertised to the model.
func toolSet() []llm.Tool {
	shell := "bash on Linux"
	if runtime.GOOS == "windows" {
		shell = "Windows PowerShell (powershell.exe); use PowerShell syntax, not bash"
	}
	return []llm.Tool{
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolRunCommand,
				Description: "Run a shell command using " + shell + " in the operator's persistent terminal (cd and exported variables persist between calls). Returns exit code and combined stdout/stderr (long output keeps its head and tail); the returned call_id can be referenced as verification evidence. Commands cannot receive keyboard input: use non-interactive flags, and start long-running services in the background with logs instead of in the foreground. Commands run sequentially.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"The shell command to execute."},
						"timeout_seconds":{"type":"integer","minimum":0,"maximum":3600,"description":"Max seconds before the command is killed. 0 or omitted means 600."}
					},
					"required":["command"]
				}`),
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolReadFile,
				Description: "Read a text file. Returns a slice of the file plus its total size and the next offset. Only for text (configs, logs, scripts) — never model weights or binaries. For big logs prefer run_command with grep/tail. To read a large text file in parts, call again with offset=next_offset.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"path":{"type":"string"},
						"offset":{"type":"integer","description":"Byte offset to start at. Default 0."},
						"max_bytes":{"type":"integer","description":"Max bytes to return (default 16384, hard cap 262144)."}
					},
					"required":["path"]
				}`),
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolWriteFile,
				Description: "Create a new file or overwrite an existing one with the given content (creates parent directories); an automatic .bak-* backup is made when overwriting an existing file. Use for new scripts/configs/compose files. To change part of an existing file, prefer edit_file.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"path":{"type":"string"},
						"content":{"type":"string"}
					},
					"required":["path","content"]
				}`),
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolEditFile,
				Description: "Edit an existing text file in place by replacing old_string with new_string; an automatic .bak-* backup is made before editing. By default old_string must match exactly once (include enough surrounding context to be unique); set replace_all to replace every occurrence. Does not load the whole file into the reply, so it works on files larger than read_file's limit.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"path":{"type":"string"},
						"old_string":{"type":"string","description":"Exact text to replace."},
						"new_string":{"type":"string","description":"Replacement text."},
						"replace_all":{"type":"boolean","description":"Replace all occurrences. Default false."}
					},
					"required":["path","old_string","new_string"]
				}`),
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolAppendFile,
				Description: "Append content to the end of a file (creates it if missing). Use for adding lines to configs, env files, or logs without rewriting the whole file.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"path":{"type":"string"},
						"content":{"type":"string"}
					},
					"required":["path","content"]
				}`),
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolWaitFor,
				Description: "Wait until a URL returns 2xx/3xx, a log contains text, or a process exits. Returns the observed condition for verification.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"kind":{"type":"string","enum":["url","log","process"]},
						"target":{"type":"string","description":"HTTP(S) URL, log file path, or positive process ID, depending on kind."},
						"contains":{"type":"string","description":"Required text to find when kind is log."},
						"timeout_seconds":{"type":"integer","minimum":0,"maximum":3600,"description":"Wait limit. 0 or omitted means 300 seconds."},
						"interval_seconds":{"type":"integer","minimum":1,"maximum":60,"description":"Polling interval. Omitted means 2 seconds; provided values must be 1..60."}
					},
					"required":["kind","target"]
				}`),
			},
		},
		{
			Type: "function",
			Function: llm.FunctionDefinition{
				Name:        toolFinish,
				Description: "For an already started task, call finish to report explicitly verified deployment success or a real failure. After reading files or checking the environment, you may explain results in a normal natural-language reply without finish. If an operator answer is required, use outcome=needs_input with success=false and ask a concrete question in summary. On success, cite real successful run_command or wait_for call IDs in verification_call_ids; run the final verification immediately before finish, with no later changes.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"summary":{"type":"string","description":"What was accomplished, how to use/verify the result, and any follow-ups."},
						"success":{"type":"boolean"},
						"outcome":{"type":"string","enum":["needs_input"],"description":"For an in-progress task blocked on information only the operator can provide. Requires success=false and must not include verification_call_ids."},
						"verification_call_ids":{"type":"array","items":{"type":"string"},"description":"Successful run_command or wait_for call IDs supporting success=true."}
					},
					"required":["summary","success"]
				}`),
			},
		},
	}
}

// Argument shapes for decoding tool-call arguments.

type runCommandArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type readFileArgs struct {
	Path     string `json:"path"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"max_bytes"`
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type appendFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type waitForArgs struct {
	Kind            string `json:"kind"`
	Target          string `json:"target"`
	Contains        string `json:"contains,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
}

type finishArgs struct {
	Summary             string   `json:"summary"`
	Success             bool     `json:"success"`
	Outcome             string   `json:"outcome,omitempty"`
	VerificationCallIDs []string `json:"verification_call_ids,omitempty"`
}
