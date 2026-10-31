//go:build windows

package server

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func TestSharedShellWindowsCancellationCleansOnlyNewDescendants(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "stop"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			shell, err := newSharedShell(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer shell.Close()
			dir := t.TempDir()
			oldPID, childPID, grandPID := filepath.Join(dir, "old.pid"), filepath.Join(dir, "child.pid"), filepath.Join(dir, "grand.pid")
			for _, path := range []string{oldPID, childPID, grandPID} {
				path := path
				t.Cleanup(func() { killRecordedReviewProcess(path) })
			}
			oldCommand := "$old=Start-Process -WindowStyle Hidden powershell.exe -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 60' -PassThru; " + writeReviewProcessIdentity(oldPID, "old")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if code, out, err := shell.Run(ctx, oldCommand, nil); err != nil || code != 0 {
				t.Fatalf("start prior background service: code=%d err=%v output=%q", code, err, out)
			}
			old, err := readReviewPID(oldPID)
			if err != nil || !reviewProcessAlive(old) {
				t.Fatalf("normally completed background service not alive: pid=%d err=%v", old, err)
			}
			grandCommand := "$grand=Start-Process -WindowStyle Hidden powershell.exe -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 60' -PassThru; " + writeReviewProcessIdentity(grandPID, "grand") + "; Start-Sleep -Seconds 60"
			encoded := reviewEncodedPowerShell(grandCommand)
			command := "$child=Start-Process -WindowStyle Hidden powershell.exe -ArgumentList '-NoProfile','-EncodedCommand'," + psQuote(encoded) + " -PassThru; " + writeReviewProcessIdentity(childPID, "child") + "; Start-Sleep -Seconds 60"
			var runCtx context.Context
			var stop context.CancelFunc
			if timeout {
				runCtx, stop = context.WithTimeout(ctx, 3*time.Second)
			} else {
				runCtx, stop = context.WithCancel(ctx)
				go func() {
					for runCtx.Err() == nil {
						if _, err := readReviewPID(grandPID); err == nil {
							stop()
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
				}()
			}
			defer stop()
			_, _, runErr := shell.Run(runCtx, command, nil)
			if runErr == nil {
				t.Fatal("cancelled command returned success")
			}
			for _, path := range []string{childPID, grandPID} {
				pid, err := readReviewPID(path)
				if err != nil {
					t.Fatalf("new descendant was not started: %s: %v", path, err)
				}
				if reviewProcessAlive(pid) {
					t.Errorf("new descendant %d still alive after %s returned", pid, name)
				}
			}
			if !reviewProcessAlive(old) {
				t.Errorf("%s killed prior command's background service %d", name, old)
			}
		})
	}
}

func TestSharedShellWindowsCancellationPreservesExistingServiceChildren(t *testing.T) {
	shell, err := newSharedShell(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer shell.Close()
	dir := t.TempDir()
	gate, oldPID, newPID := filepath.Join(dir, "spawn"), filepath.Join(dir, "service.pid"), filepath.Join(dir, "service-child.pid")
	for _, path := range []string{oldPID, newPID} {
		path := path
		t.Cleanup(func() { killRecordedReviewProcess(path) })
	}
	service := "while (-not (Test-Path -LiteralPath " + psQuote(gate) + ")) { Start-Sleep -Milliseconds 20 }; $child=Start-Process -WindowStyle Hidden powershell.exe -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 60' -PassThru; " + writeReviewProcessIdentity(newPID, "child") + "; Start-Sleep -Seconds 60"
	command := "$service=Start-Process -WindowStyle Hidden powershell.exe -ArgumentList '-NoProfile','-EncodedCommand'," + psQuote(reviewEncodedPowerShell(service)) + " -PassThru; " + writeReviewProcessIdentity(oldPID, "service")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if code, out, err := shell.Run(ctx, command, nil); err != nil || code != 0 {
		t.Fatalf("start existing service: code=%d err=%v output=%q", code, err, out)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		for runCtx.Err() == nil {
			if _, err := readReviewPID(newPID); err == nil {
				stop()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	_, _, err = shell.Run(runCtx, "[IO.File]::WriteAllText("+psQuote(gate)+",'spawn'); Start-Sleep -Seconds 60", nil)
	if err == nil {
		t.Fatal("cancelled command returned success")
	}
	for _, path := range []string{oldPID, newPID} {
		pid, err := readReviewPID(path)
		if err != nil || !reviewProcessAlive(pid) {
			t.Errorf("existing service or its newly created child was killed: pid=%d err=%v", pid, err)
		}
	}
}

func readReviewPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(fields[0])
}

func writeReviewProcessIdentity(path, variable string) string {
	return "[IO.File]::WriteAllText(" + psQuote(path) + ",([string]$" + variable + ".Id+' '+[string]($" + variable + ".StartTime.ToFileTimeUtc())))"
}

// Cleanup never acts on a PID that has been reused after the assertion killed
// the test child: the file records its creation time as well as its PID.
func killRecordedReviewProcess(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return
	}
	pid, err := strconv.ParseUint(fields[0], 10, 32)
	if err != nil {
		return
	}
	identity, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(process)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(process, &created, &exited, &kernel, &user) != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != identity {
		return
	}
	_ = windows.TerminateProcess(process, 1)
	_, _ = windows.WaitForSingleObject(process, 1000)
}

func reviewProcessAlive(pid int) bool {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	var code uint32
	return windows.GetExitCodeProcess(process, &code) == nil && code == 259
}

func reviewEncodedPowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	bytes := make([]byte, len(encoded)*2)
	for i, code := range encoded {
		binary.LittleEndian.PutUint16(bytes[2*i:], code)
	}
	return base64.StdEncoding.EncodeToString(bytes)
}
