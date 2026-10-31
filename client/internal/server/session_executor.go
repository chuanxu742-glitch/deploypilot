package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/deploypilot/deploypilot/internal/agent"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func (s *Server) sharedTerminal() (*sharedShell, error) {
	s.shellMu.Lock()
	defer s.shellMu.Unlock()
	replacing := false
	if s.shell != nil {
		select {
		case <-s.shell.dead:
			_ = s.shell.Close()
			s.shell = nil
			replacing = true
		default:
			return s.shell, nil
		}
	}
	dir := s.historyDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "deploypilot-session-")
		if err != nil {
			return nil, err
		}
	}
	shell, err := newSharedShell(dir)
	if err != nil {
		return nil, err
	}
	// The next agent command reports the restart once, so the model knows
	// variables and other state from the old shell are gone.
	shell.restarted.Store(replacing)
	s.shell = shell
	return shell, nil
}

type sessionExecutor struct {
	server *Server
	files  agent.Executor
}

func (e *sessionExecutor) WorkingDirectory() string {
	shell, err := e.server.sharedTerminal()
	if err != nil {
		return ""
	}
	return shell.Cwd()
}
func (e *sessionExecutor) Run(ctx context.Context, command string, timeout time.Duration, output func(string)) (int, string, error) {
	shell, err := e.server.sharedTerminal()
	if err != nil {
		return -1, "", err
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	code, out, err := shell.Run(ctx, command, output)
	if errors.Is(err, errShellEndedBeforeStart) {
		// A command right after `exit` can reach the old shell in the few
		// milliseconds before its exit is noticed. Nothing ran; use a new one.
		if shell, err = e.server.sharedTerminal(); err != nil {
			return -1, "", err
		}
		code, out, err = shell.Run(ctx, command, output)
	}
	if shell.restarted.CompareAndSwap(true, false) {
		out = shellRestartNote + "\n" + out
	}
	return code, out, err
}

// shellRestartNote prefixes the first result from a replacement terminal.
const shellRestartNote = "[note: the previous terminal shell exited (for example after `exit`) and a new one was started in the last working directory; variables, functions and other session state from before are gone]"

func (e *sessionExecutor) path(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(e.WorkingDirectory(), path)
}
func (e *sessionExecutor) ReadFile(path string, offset, limit int64) (agent.ReadResult, error) {
	return e.files.ReadFile(e.path(path), offset, limit)
}
func (e *sessionExecutor) WriteFile(path, content string) error {
	return e.files.WriteFile(e.path(path), content)
}
func (e *sessionExecutor) AppendFile(path, content string) error {
	return e.files.AppendFile(e.path(path), content)
}
func (e *sessionExecutor) EditFile(path, old, new string, all bool) (int, error) {
	return e.files.EditFile(e.path(path), old, new, all)
}
func (e *sessionExecutor) MachineInfo(ctx context.Context) string {
	command := "pwd"
	if runtime.GOOS == "windows" {
		command = "(Get-Location).Path"
	}
	_, _, err := e.Run(ctx, command, 10*time.Second, nil)
	host, _ := os.Hostname()
	if err != nil {
		return fmt.Sprintf("OS: %s; host: %s; terminal unavailable: %v", runtime.GOOS, host, err)
	}
	// Hardware and toolchain probes run as separate read-only processes so
	// they do not clutter the operator's terminal.
	return fmt.Sprintf("OS: %s; architecture: %s; host: %s; working directory: %s. Commands share the persistent interactive terminal. Directory and environment changes persist.\n%s", runtime.GOOS, runtime.GOARCH, host, e.WorkingDirectory(), e.files.MachineInfo(ctx))
}
