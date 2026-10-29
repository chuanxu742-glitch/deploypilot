package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSharedShellKeepsDirectoryEnvironmentAndManualInput(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	dir := t.TempDir()
	var change, query, manual string
	marker := filepath.Join(t.TempDir(), "manual-ready")
	if runtime.GOOS == "windows" {
		change = "Set-Location '" + strings.ReplaceAll(dir, "'", "''") + "'; $env:DP_SESSION_TEST='shared'"
		query = "(Get-Location).Path; $env:DP_SESSION_TEST"
		manual = "Set-Location '" + strings.ReplaceAll(filepath.Dir(dir), "'", "''") + "'; [IO.File]::WriteAllText('" + strings.ReplaceAll(marker, "'", "''") + "','ready')\r"
	} else {
		change = "cd '" + dir + "'; export DP_SESSION_TEST=shared"
		query = "pwd; printf '%s\\n' \"$DP_SESSION_TEST\""
		manual = "cd '" + filepath.Dir(dir) + "'; printf ready > '" + marker + "'\n"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if code, out, err := shell.Run(ctx, change, nil); err != nil || code != 0 {
		t.Fatalf("change code=%d err=%v out=%s", code, err, out)
	}
	// XTerm sends focus-out when the user clicks the Agent composer.
	if _, err := shell.Write([]byte("\x1b[O")); err != nil {
		t.Fatal(err)
	}
	if code, out, err := shell.Run(ctx, query, nil); err != nil || code != 0 || !strings.Contains(out, dir) || !strings.Contains(out, "shared") {
		t.Fatalf("persistent state code=%d err=%v out=%s", code, err, out)
	}
	if _, err := shell.Write([]byte(manual)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if code, out, err := shell.Run(ctx, query, nil); err != nil || code != 0 || !strings.Contains(out, filepath.Dir(dir)) {
		t.Fatalf("manual state code=%d err=%v out=%s", code, err, out)
	}
	if shell.Cwd() != filepath.Dir(dir) {
		t.Fatalf("cwd=%q", shell.Cwd())
	}
}

func TestSharedShellRestoresDirectoryAndHistory(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	storage := t.TempDir()
	target := t.TempDir()
	shell, err := newSharedShell(storage)
	if err != nil {
		t.Fatal(err)
	}
	command := "cd " + shellQuote(target) + "; echo persisted-output"
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(target) + "; Write-Output 'persisted-output'"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, _, err = shell.Run(ctx, command, nil); err != nil {
		t.Fatal(err)
	}
	// ConPTY may deliver the screen update after the execution result side channel.
	time.Sleep(150 * time.Millisecond)
	shell.Close()
	restored, err := newSharedShell(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.Cwd() != target {
		t.Fatalf("cwd %q", restored.Cwd())
	}
	history, _, unsubscribe := restored.Subscribe()
	defer unsubscribe()
	var output strings.Builder
	for _, chunk := range history {
		output.Write(chunk)
	}
	if !strings.Contains(output.String(), "persisted-output") {
		t.Fatal("terminal history lost")
	}
}

func TestSharedShellExitStatusAndCancellation(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fail, ok, wait := "false", "printf success", "sleep 30"
	if runtime.GOOS == "windows" {
		fail = "cmd /c exit 7"
		ok = "Write-Output success"
		wait = "Start-Sleep -Seconds 30"
	}
	if code, _, err := shell.Run(ctx, fail, nil); err != nil || code == 0 {
		t.Fatalf("failure: %d %v", code, err)
	}
	if code, out, err := shell.Run(ctx, ok, nil); err != nil || code != 0 || !strings.Contains(out, "success") {
		t.Fatalf("next command: %d %v %s", code, err, out)
	}
	short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, _, err := shell.Run(short, wait, nil); err == nil {
		t.Fatal("cancel returned success")
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("cancellation did not finish")
	}
}

func TestSessionExecutorRelativeFilesFollowShell(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("PTY unavailable")
	}
	srv := New(Options{HistoryDir: t.TempDir()})
	defer srv.Shutdown(context.Background())
	executor := srv.newExecutor()
	target := t.TempDir()
	command := "cd " + shellQuote(target)
	if runtime.GOOS == "windows" {
		command = "Set-Location -LiteralPath " + psQuote(target)
	}
	if _, _, err := executor.Run(context.Background(), command, 10*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if err := executor.WriteFile("relative.txt", "correct directory"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "relative.txt"))
	if err != nil || string(data) != "correct directory" {
		t.Fatal("relative write missed shared cwd", err)
	}
}
