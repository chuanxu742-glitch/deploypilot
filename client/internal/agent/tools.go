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
				Description: "Run a shell command using " + shell + ". Returns exit code and combined stdout/stderr. Prefer non-interactive commands; commands run sequentially.",
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
				Description: "Create a new file or overwrite an existing one with the given content (creates parent directories). Use for new scripts/configs/compose files. To change part of an existing file, prefer edit_file.",
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
				Description: "Edit an existing text file in place by replacing old_string with new_string. By default old_string must match exactly once (include enough surrounding context to be unique); set replace_all to replace every occurrence. Does not load the whole file into the reply, so it works on files larger than read_file's limit.",
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
				Name:        toolFinish,
				Description: "Call when the task is complete (or cannot proceed) to end the session with a summary for the operator.",
				Parameters: raw(`{
					"type":"object",
					"properties":{
						"summary":{"type":"string","description":"What was accomplished, how to use/verify the result, and any follow-ups."},
						"success":{"type":"boolean"}
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

type finishArgs struct {
	Summary string `json:"summary"`
	Success bool   `json:"success"`
}
