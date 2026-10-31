//go:build windows

package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPSReadLineResize(t *testing.T) {
	base := t.TempDir()
	// Match the observed 83-character cwd, or use a longer one if the host's
	// temporary root already exceeds it; editing must remain safe in either case.
	padding := max(1, 83-len(base)-1)
	dir := filepath.Join(base, strings.Repeat("x", padding))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	failures := 0
	for attempt := 1; attempt <= 3; attempt++ {
		shell, err := newSharedShell(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer shell.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			time.Sleep(150 * time.Millisecond) // Let PSReadLine cache the wide prompt.
			_, _ = shell.Write([]byte("\x1b[8;24;80t"))
			if err := shell.Resize(52, 80); err != nil {
				t.Fatal(err)
			}
			_, _ = shell.Write([]byte("."))
			time.Sleep(30 * time.Millisecond)
			_, _ = shell.Write([]byte{3})
			promptCommand := "Write-Output ('PSRL_VERSION='+(Get-Module PSReadLine).Version); $probePrompt=[string](& prompt); $probeLine=($probePrompt -split [char]10)[-1].TrimEnd([char]13); Write-Output ('INPUT_START='+$host.UI.RawUI.LengthInBufferCells($probeLine)); Write-Output ('FULL_PROMPT='+$probePrompt)"
			code, out, err := shell.Run(ctx, promptCommand, nil)
			t.Logf("attempt=%d resized=100x24->80x52 code=%d err=%v output=%q", attempt, code, err, out)
			if err != nil || code != 0 || !regexp.MustCompile(`PSRL_VERSION=\d+`).MatchString(out) {
				t.Errorf("PSReadLine command failed: %v %d", err, code)
			}
			column := regexp.MustCompile(`INPUT_START=(\d+)`).FindStringSubmatch(out)
			if len(column) != 2 {
				t.Errorf("could not inspect the prompt's editing origin: %q", out)
			} else if cells, _ := strconv.Atoi(column[1]); cells >= 32 {
				t.Errorf("prompt editing origin %d cannot survive contraction to a 32-column terminal", cells)
			}
			if !strings.Contains(out, dir) {
				t.Error("full working directory disappeared from prompt")
			}
			if err := shell.Resize(24, 32); err != nil {
				t.Fatal(err)
			}
			_, _ = shell.Write([]byte(".")) // Force the incomplete-command error prompt.
			time.Sleep(30 * time.Millisecond)
			_, _ = shell.Write([]byte{3})
			// Real key input still has to edit, execute and recall a command.
			marker := filepath.Join(base, fmt.Sprintf("edited-%d", attempt))
			command := "[IO.File]::WriteAllText(" + psQuote(marker) + ",'EDIX"
			if _, err := shell.Write([]byte(command + "\x7fT')\r")); err != nil {
				t.Fatal(err)
			}
			waitEdited := func(action string) {
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if data, err := os.ReadFile(marker); err == nil && string(data) == "EDIT" {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				t.Errorf("manual %s command did not execute", action)
			}
			waitEdited("backspace/edit")
			_ = os.WriteFile(marker, []byte("reset"), 0600)
			_, _ = shell.Write([]byte("\x1b[A\r"))
			waitEdited("history recall")
			if err := os.WriteFile(filepath.Join(dir, "zzcompletion-fixture.txt"), []byte("EDIT"), 0600); err != nil {
				t.Fatal(err)
			}
			_ = os.WriteFile(marker, []byte("reset"), 0600)
			completion := "[IO.File]::WriteAllText(" + psQuote(marker) + ",(Get-Content .\\zzcompletion"
			_, _ = shell.Write([]byte(completion + "\t))\r"))
			waitEdited("Tab completion")
			if code, _, err := shell.Run(ctx, "Write-Output 'AFTER-MANUAL-EDIT'", nil); err != nil || code != 0 {
				t.Errorf("Agent command failed after manual editing: code=%d err=%v", code, err)
			}
			history, _, unsubscribe := shell.Subscribe()
			unsubscribe()
			text := string(bytes.Join(history, nil))
			if outputDir := os.Getenv("DP_PSR_EVIDENCE"); outputDir != "" {
				label := os.Getenv("DP_PSR_LABEL")
				_ = os.WriteFile(filepath.Join(outputDir, fmt.Sprintf("psreadline-%s-attempt-%d.terminal.log", label, attempt)), []byte(text), 0600)
			}
			if strings.Contains(text, "PSConsoleReadLine.") || strings.Contains(text, "Oops, something went wrong") {
				failures++
				t.Errorf("attempt=%d PSReadLine crashed after terminal resize", attempt)
			}
		}()
	}
	t.Logf("PSReadLine resize crashes=%d/3", failures)
}
