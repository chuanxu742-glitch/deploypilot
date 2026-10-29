package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/deploypilot/deploypilot/internal/pty"
)

type terminalSnapshot struct {
	Cwd    string   `json:"cwd"`
	Chunks [][]byte `json:"chunks"`
}

// The shell belongs to the application, not a WebSocket. Both manual input and
// Agent commands use this PTY; disconnecting a viewer only removes its subscription.
type sharedShell struct {
	mu           sync.Mutex
	runMu        sync.Mutex
	inputMu      sync.Mutex
	session      pty.Session
	dir          string
	cwd          string
	chunks       [][]byte
	bytes        int
	subscribers  map[chan []byte]struct{}
	output       func(string)
	marker       string
	markerTail   string
	completed    chan struct{}
	dead         chan struct{}
	closeOnce    sync.Once
	persistTimer *time.Timer
	readyPath    string
	readyEnabled bool
}

func newSharedShell(dir string) (*sharedShell, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	shell := &sharedShell{dir: dir, cwd: cwd, subscribers: make(map[chan []byte]struct{}), dead: make(chan struct{})}
	var old terminalSnapshot
	if data, err := os.ReadFile(filepath.Join(dir, "terminal.json")); err == nil {
		if err = json.Unmarshal(data, &old); err != nil {
			return nil, fmt.Errorf("read terminal history: %w", err)
		}
		if info, err := os.Stat(old.Cwd); err == nil && info.IsDir() {
			shell.cwd = old.Cwd
		}
		for _, chunk := range old.Chunks {
			shell.keepLocked(chunk)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cfg := pty.Config{Rows: 24, Cols: 100}
	if runtime.GOOS == "linux" {
		cfg.Shell = "/bin/bash"
	}
	session, err := pty.Start(cfg)
	if err != nil {
		return nil, err
	}
	shell.session = session
	restoreCwd := shell.cwd
	go shell.read()
	shell.readyPath = filepath.Join(dir, "shell-ready")
	_ = os.Remove(shell.readyPath)
	bootstrap := "PROMPT_COMMAND=" + shellQuote("printf '%s' \"$PWD\" > "+shellQuote(shell.readyPath))
	if runtime.GOOS == "windows" {
		bootstrap = "function global:prompt { [IO.File]::WriteAllText(" + psQuote(shell.readyPath) + ",(Get-Location).Path); 'PS '+(Get-Location).Path+'> ' }"
		bootstrap += "; function global:dp { . ([ScriptBlock]::Create([IO.File]::ReadAllText(" + psQuote(filepath.Join(dir, "command.ps1")) + "))) }"
	}
	initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, _, initErr := shell.Run(initCtx, bootstrap, nil)
	initCancel()
	if initErr != nil {
		shell.Close()
		return nil, initErr
	}
	shell.readyEnabled = true
	if restoreCwd != cwd {
		command := "cd " + shellQuote(restoreCwd)
		if runtime.GOOS == "windows" {
			command = "Set-Location -LiteralPath " + psQuote(restoreCwd)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, err = shell.Run(ctx, command, nil)
		cancel()
		if err != nil {
			shell.Close()
			return nil, fmt.Errorf("restore terminal directory: %w", err)
		}
	}
	return shell, nil
}

func (s *sharedShell) keepLocked(data []byte) {
	chunk := append([]byte(nil), data...)
	s.chunks = append(s.chunks, chunk)
	s.bytes += len(chunk)
	for s.bytes > 2*1024*1024 && len(s.chunks) > 1 {
		s.bytes -= len(s.chunks[0])
		s.chunks = s.chunks[1:]
	}
}
func (s *sharedShell) read() {
	defer close(s.dead)
	buffer := make([]byte, 16*1024)
	for {
		n, err := s.session.Read(buffer)
		if n > 0 {
			data := append([]byte(nil), buffer[:n]...)
			s.mu.Lock()
			output := s.output
			s.mu.Unlock()
			if output != nil {
				output(string(data))
			}
			s.mu.Lock()
			s.keepLocked(data)
			if s.completed != nil {
				s.markerTail += string(data)
				if strings.Contains(s.markerTail, s.marker) {
					close(s.completed)
					s.completed = nil
				}
				if len(s.markerTail) > 4096 {
					s.markerTail = s.markerTail[len(s.markerTail)-4096:]
				}
			}
			for ch := range s.subscribers {
				select {
				case ch <- data:
				default:
					delete(s.subscribers, ch)
					close(ch)
				}
			}
			if s.persistTimer == nil {
				s.persistTimer = time.AfterFunc(250*time.Millisecond, func() { s.mu.Lock(); defer s.mu.Unlock(); s.persistLocked(); s.persistTimer = nil })
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}
func (s *sharedShell) persistLocked() {
	if data, err := os.ReadFile(s.readyPath); err == nil && len(data) > 0 {
		s.cwd = string(data)
	}
	data, err := json.Marshal(terminalSnapshot{Cwd: s.cwd, Chunks: s.chunks})
	if err == nil {
		_ = atomicPrivateWrite(filepath.Join(s.dir, "terminal.json"), data)
	}
}
func atomicPrivateWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func (s *sharedShell) Subscribe() ([][]byte, <-chan []byte, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := append([][]byte(nil), s.chunks...)
	ch := make(chan []byte, 256)
	s.subscribers[ch] = struct{}{}
	return history, ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
	}
}
func (s *sharedShell) Write(data []byte) (int, error) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	select {
	case <-s.dead:
		return 0, errors.New("terminal session ended")
	default:
		// Focus and terminal capability replies do not edit the command line.
		if !terminalReply.Match(data) {
			_ = os.Remove(s.readyPath)
		}
		return s.session.Write(data)
	}
}
func (s *sharedShell) Resize(rows, cols uint16) error { return s.session.Resize(rows, cols) }
func (s *sharedShell) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, err := os.ReadFile(s.readyPath); err == nil && len(data) > 0 {
		s.cwd = string(data)
	}
	return s.cwd
}
func (s *sharedShell) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.session.Close()
		<-s.dead
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.persistTimer != nil {
			s.persistTimer.Stop()
		}
		s.persistLocked()
		for ch := range s.subscribers {
			close(ch)
			delete(s.subscribers, ch)
		}
	})
	return err
}
func psQuote(s string) string    { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

var terminalEscapes = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07]*(?:\x07|\x1b\\\\))")
var terminalReply = regexp.MustCompile("^(?:\x1b\\[(?:[IO]|[?>=]?[0-9;]+[cRnt]))+$")

// Dot-source a temporary script in the existing shell. Completion is written to
// a private side channel, so a command's ordinary stdout cannot spoof its exit status.
func (s *sharedShell) Run(ctx context.Context, command string, onOutput func(string)) (int, string, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.readyEnabled {
		wait := time.NewTicker(20 * time.Millisecond)
		defer wait.Stop()
		for {
			if data, err := os.ReadFile(s.readyPath); err == nil && len(data) > 0 {
				break
			}
			select {
			case <-ctx.Done():
				return -1, "", fmt.Errorf("terminal is busy or has unfinished input: %w", ctx.Err())
			case <-s.dead:
				return -1, "", errors.New("terminal session ended")
			case <-wait.C:
			}
		}
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return -1, "", err
	}
	id := hex.EncodeToString(token)
	resultPath := filepath.Join(s.dir, "result-"+id+".json")
	scriptPath := filepath.Join(s.dir, "run-"+id)
	defer os.Remove(resultPath)
	defer os.Remove(scriptPath)
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	var script, input string
	if runtime.GOOS == "windows" {
		scriptPath += ".ps1"
		if s.readyEnabled {
			scriptPath = filepath.Join(s.dir, "command.ps1")
		}
		defer os.Remove(scriptPath)
		code := "__dp_code_" + id
		script = fmt.Sprintf(`$%s=@{code=0; truncated=$false; output=[Text.StringBuilder]::new()}
$global:LASTEXITCODE=0
try {
 . ([ScriptBlock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s')))) 2>&1 | ForEach-Object {
  if ($_ -is [System.Management.Automation.ErrorRecord]) { $%s.code=1 }
  [void]$%s.output.Append(($_ | Out-String))
  if ($%s.output.Length -gt 16384) { [void]$%s.output.Remove(8192,$%s.output.Length-16384); $%s.truncated=$true }
  $_
 } | Out-Default
 if ($LASTEXITCODE -ne 0) { $%s.code=$LASTEXITCODE }
} catch { $%s.code=1; [void]$%s.output.Append($_.ToString()); Write-Output $_ } finally {
 [IO.File]::WriteAllText(%s,(@{code=$%s.code;cwd=(Get-Location).Path;output=$%s.output.ToString();truncated=$%s.truncated}|ConvertTo-Json -Compress))
 Remove-Variable %s -ErrorAction SilentlyContinue
}
`, code, encoded, code, code, code, code, code, code, code, code, code, psQuote(resultPath), code, code, code, code)
		input = ". ([ScriptBlock]::Create([IO.File]::ReadAllText(" + psQuote(scriptPath) + ")))\r"
		if s.readyEnabled {
			label := strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return ' '
				}
				return r
			}, command)
			labelRunes := []rune(label)
			if len(labelRunes) > 180 {
				label = string(labelRunes[:180]) + "..."
			}
			input = ". dp # " + label + "\r"
		}
	} else {
		code := "__dp_code_" + id
		script = "eval \"$(printf %s '" + encoded + "' | base64 -d)\"\n" + code + "=$?\nprintf '%s\\n%s' \"$" + code + "\" \"$PWD\" > " + shellQuote(resultPath) + "\nunset " + code + "\n"
		input = ". " + shellQuote(scriptPath) + "; printf '\\033]777;" + id + "\\007'\n"
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		return -1, "", err
	}
	var captured strings.Builder
	var captureMu sync.Mutex
	completed := make(chan struct{})
	s.mu.Lock()
	s.marker, s.markerTail, s.completed = "\x1b]777;"+id+"\x07", "", completed
	s.output = func(data string) {
		captureMu.Lock()
		defer captureMu.Unlock()
		if captured.Len() < 128*1024 {
			captured.WriteString(data)
		}
		if onOutput != nil && runtime.GOOS != "windows" {
			onOutput(terminalEscapes.ReplaceAllString(data, ""))
		}
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.output = nil; s.mu.Unlock() }()
	if _, err := s.Write([]byte(input)); err != nil {
		return -1, "", err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var cancelled error
	var grace <-chan time.Time
	ctxDone := ctx.Done()
	outputComplete := false
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			if cancelled == nil {
				cancelled = ctx.Err()
				_, _ = s.Write([]byte{3})
				grace = time.After(2 * time.Second)
			}
		case <-grace:
			// If the shell does not acknowledge cancellation, close it rather than grant
			// manual input while an unknown foreground program may still be running.
			_ = s.session.Close()
			return -1, "", fmt.Errorf("command cancelled; terminal session closed: %w", cancelled)
		case <-s.dead:
			return -1, "", errors.New("terminal session ended while executing command")
		case <-completed:
			outputComplete = true
			completed = nil
		case <-ticker.C:
			if runtime.GOOS != "windows" && !outputComplete && cancelled == nil {
				continue
			}
			data, err := os.ReadFile(resultPath)
			if err != nil {
				continue
			}
			result := struct {
				Code      int    `json:"code"`
				Cwd       string `json:"cwd"`
				Output    string `json:"output"`
				Truncated bool   `json:"truncated"`
			}{}
			if runtime.GOOS == "windows" {
				if json.Unmarshal(data, &result) != nil {
					continue
				}
			} else {
				parts := strings.SplitN(string(data), "\n", 2)
				if len(parts) != 2 {
					continue
				}
				if _, err := fmt.Sscanf(parts[0], "%d", &result.Code); err != nil {
					continue
				}
				result.Cwd = parts[1]
			}
			s.mu.Lock()
			s.cwd = result.Cwd
			s.persistLocked()
			captureMu.Lock()
			output := captured.String()
			captureMu.Unlock()
			s.mu.Unlock()
			output = terminalEscapes.ReplaceAllString(output, "")
			if runtime.GOOS == "windows" {
				output = result.Output
				if result.Truncated {
					chars := []rune(output)
					middle := len(chars) / 2
					output = string(chars[:middle]) + "\n[earlier output truncated]\n" + string(chars[middle:])
				}
				if onOutput != nil && output != "" {
					onOutput(output)
				}
			}
			if cancelled != nil {
				return -1, output, fmt.Errorf("command cancelled: %w", cancelled)
			}
			return result.Code, output, nil
		}
	}
}
