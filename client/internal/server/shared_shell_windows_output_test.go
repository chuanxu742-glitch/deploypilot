//go:build windows

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedShellWindowsFormattingPipeline(t *testing.T) {
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "format-sample.txt"), []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, output, err := shell.Run(ctx, "Get-ChildItem -LiteralPath "+psQuote(dir)+" | Format-Table Name,Length", nil)
	if err != nil || code != 0 || !strings.Contains(output, "format-sample.txt") || !strings.Contains(output, "Length") {
		t.Fatalf("valid formatted pipeline: code=%d err=%v output=%q", code, err, output)
	}
	code, output, err = shell.Run(ctx, "Get-ChildItem -LiteralPath "+psQuote(dir)+" | Format-List Name,Length", nil)
	if err != nil || code != 0 || !strings.Contains(output, "format-sample.txt") || !strings.Contains(output, "Length") {
		t.Fatalf("valid formatted list: code=%d err=%v output=%q", code, err, output)
	}
}

func TestSharedShellWindowsMarkerInvisibleAfterCompletionAndResize(t *testing.T) {
	for attempt := 1; attempt <= 3; attempt++ {
		shell, err := newSharedShell(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer shell.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			title := func() string {
				code, output, err := shell.Run(ctx, "Write-Output ('TITLE-CHECK='+[Console]::Title)", nil)
				if err != nil || code != 0 {
					t.Fatalf("completion protocol failed: code=%d err=%v output=%q", code, err, output)
				}
				for _, line := range strings.Split(output, "\n") {
					if strings.HasPrefix(line, "TITLE-CHECK=") {
						return strings.TrimSpace(strings.TrimPrefix(line, "TITLE-CHECK="))
					}
				}
				t.Fatalf("console title observation missing: %q", output)
				return ""
			}
			before := title()
			for _, cols := range []uint16{80, 32, 100, 32} {
				if err := shell.Resize(37, cols); err != nil {
					t.Fatal(err)
				}
				time.Sleep(60 * time.Millisecond)
			}
			if after := title(); before != after {
				t.Errorf("completion marker changed Console.Title: before=%q after=%q", before, after)
			}
			_ = shell.Resize(24, 80)
			time.Sleep(80 * time.Millisecond)
			history, _, unsubscribe := shell.Subscribe()
			unsubscribe()
			raw := bytes.Join(history, nil)
			visible := terminalEscapes.ReplaceAllString(string(raw), "")
			if strings.Contains(visible, "DP_OUTPUT_DONE_") {
				t.Errorf("attempt=%d completed internal marker leaked after real ConPTY redraw", attempt)
			}
			if outputDir := os.Getenv("DP_MARKER_EVIDENCE"); outputDir != "" {
				label := os.Getenv("DP_MARKER_LABEL")
				_ = os.WriteFile(filepath.Join(outputDir, fmt.Sprintf("marker-%s-attempt-%d.terminal.log", label, attempt)), raw, 0600)
			}
		}()
	}
}

func TestSharedShellWindowsMarkerFollowsFullRawOutput(t *testing.T) {
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, output, err := shell.Run(ctx, "[Console]::Write('RAW-START'+('r'*40000)+'RAW-END')", nil)
	if err != nil || code != 0 {
		t.Fatalf("raw output completion: code=%d err=%v output=%q", code, err, output)
	}
	const prefix = "[full output: "
	index := strings.LastIndex(output, prefix)
	if index < 0 {
		t.Fatalf("raw output has no complete log: %q", output)
	}
	path := strings.TrimSuffix(output[index+len(prefix):], "]")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := terminalEscapes.ReplaceAllString(string(data), "")
	text = strings.NewReplacer("\r", "", "\n", "").Replace(text)
	frame := regexp.MustCompile(`\x1b\]777;([0-9a-f]{24})\x07`).FindSubmatch(data)
	if len(frame) != 2 {
		t.Fatal("raw completion log is missing its complete invisible control frame")
	}
	if strings.Contains(text, string(frame[1])) {
		t.Fatal("completion nonce appeared outside its invisible control frame")
	}
	if len(data) < 40000 || !strings.Contains(text, "RAW-START") || !strings.Contains(text, "RAW-END") {
		t.Fatalf("completion acknowledged before raw output was logged: bytes=%d hasStart=%v hasEnd=%v", len(data), strings.Contains(text, "RAW-START"), strings.Contains(text, "RAW-END"))
	}
}

func TestSharedShellWindowsStreamsRenderedOutputBeforeCompletion(t *testing.T) {
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var streamed strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		code, output, err := shell.Run(ctx, "Write-Output 'STREAM-FIRST'; Start-Sleep -Seconds 2; Write-Output 'STREAM-SECOND'", func(chunk string) {
			mu.Lock()
			defer mu.Unlock()
			streamed.WriteString(chunk)
			if strings.Contains(streamed.String(), "STREAM-FIRST") {
				once.Do(func() { close(first) })
			}
		})
		if err != nil || code != 0 || !strings.Contains(output, "STREAM-SECOND") {
			t.Errorf("stream command: code=%d err=%v output=%q", code, err, output)
		}
	}()
	select {
	case <-first:
	case <-time.After(1200 * time.Millisecond):
		t.Error("first rendered output did not arrive during the two-second command")
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	text := streamed.String()
	if strings.Count(text, "STREAM-FIRST") != 1 || strings.Count(text, "STREAM-SECOND") != 1 || strings.Contains(text, "DP_OUTPUT_DONE_") || strings.Contains(text, ". dp #") {
		t.Errorf("stream output duplicated or contains control/echo text: %q", text)
	}
}

func TestSharedShellWindowsNativeStderrIsNotExitFailure(t *testing.T) {
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, output, err := shell.Run(ctx, `cmd.exe /d /c "echo native-progress 1>&2"`, nil)
	if err != nil || code != 0 || !strings.Contains(output, "native-progress") {
		t.Fatalf("successful native stderr: code=%d err=%v output=%q", code, err, output)
	}
	code, output, err = shell.Run(ctx, `cmd.exe /d /c "echo native-failure 1>&2 & exit /b 7"`, nil)
	if err != nil || code != 7 || !strings.Contains(output, "native-failure") {
		t.Fatalf("failed native stderr: code=%d err=%v output=%q", code, err, output)
	}
	code, output, err = shell.Run(ctx, `Write-Error 'powershell-error'`, nil)
	if err != nil || code == 0 || !strings.Contains(output, "powershell-error") {
		t.Fatalf("PowerShell error must still fail: code=%d err=%v output=%q", code, err, output)
	}
}

func TestSharedShellWindowsCancelledOutputRemainsVisible(t *testing.T) {
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	var streamed strings.Builder
	_, output, err := shell.Run(ctx, "Write-Output 'CANCELLED-PROGRESS'; Start-Sleep -Seconds 30", func(chunk string) { streamed.WriteString(chunk) })
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(streamed.String(), "CANCELLED-PROGRESS") || !strings.Contains(output, "[full output: ") {
		t.Fatalf("cancelled output: err=%v streamed=%q output=%q", err, streamed.String(), output)
	}
	if paths, _ := filepath.Glob(filepath.Join(shell.dir, "result-*.output")); len(paths) != 0 {
		t.Fatalf("temporary output streams were not removed: %v", paths)
	}
}

func TestSharedShellWindowsOutputStreamKeepsUTF8AcrossWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte{'a', 0xe4}, 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	offset, err := readWindowsCommandOutput(path, 0, func(chunk string) { output.WriteString(chunk) })
	if err != nil || output.String() != "a" {
		t.Fatalf("partial UTF-8 write: offset=%d err=%v output=%q", offset, err, output.String())
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write([]byte{0xbd, 0xa0})
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("complete UTF-8 write: %v %v", writeErr, closeErr)
	}
	_, err = readWindowsCommandOutput(path, offset, func(chunk string) { output.WriteString(chunk) })
	if err != nil || output.String() != "a你" {
		t.Fatalf("completed UTF-8 write: err=%v output=%q", err, output.String())
	}
}
