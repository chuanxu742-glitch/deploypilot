package tunnel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setDiagnosticTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestStartTimeoutReportsStageAndUnderlyingError(t *testing.T) {
	for _, tc := range []struct{ mode, stage, reason string }{
		{"diagnostic-no-url", "waiting for tunnel URL", "quick tunnel API request failed"},
		{"diagnostic-timeout", "waiting for first edge connection; tunnel URL received", "TLS handshake with edge error: EOF"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			setDiagnosticTestHome(t)
			t.Setenv("DP_TEST_TUNNEL_PROCESS", tc.mode)
			exe, _ := os.Executable()
			_, err := startWithTimeout(context.Background(), exe, 12345, time.Second)
			if err == nil {
				t.Fatal("unconnected process must time out")
			}
			for _, want := range []string{tc.stage, tc.reason, "; log: "} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q in %v", want, err)
				}
			}
		})
	}
}

func TestDiagnosticLogRedactsAcrossWritesAndFlushesPartialLine(t *testing.T) {
	setDiagnosticTestHome(t)
	d := newDiagnosticLog()
	defer d.Close()
	for _, part := range []string{
		"INF Settings: map[token:config-", "credential]\n",
		"INF Environment variables map[TUNNEL_TOKEN:env-credential]\n",
		"ERR authorization=Bearer bearer-", "credential\n",
		"ERR password=secret-password api_key=secret-key\n",
		"ERR https://user:secret-pass@example.com/#t=pairing-secret\n",
		"\x1b[31mERR token=last-secret TLS handshake failed\x1b[0m\r",
	} {
		_, _ = d.Write([]byte(part))
	}
	d.Close()
	data, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	summary := d.wrap(errors.New("startup failed")).Error()
	for _, secret := range []string{"config-credential", "env-credential", "bearer-credential", "secret-password", "secret-key", "secret-pass", "pairing-secret", "last-secret", "\x1b", "\r"} {
		if strings.Contains(string(data), secret) || strings.Contains(summary, secret) {
			t.Fatalf("secret/control sequence %q was retained", secret)
		}
	}
	if !strings.Contains(string(data), "TLS handshake failed") || !strings.Contains(summary, "TLS handshake failed") {
		t.Fatal("partial final error was lost")
	}
}

func TestDiagnosticLogBoundsOutputAndKeepsLatestError(t *testing.T) {
	setDiagnosticTestHome(t)
	d := newDiagnosticLog()
	defer d.Close()
	_, _ = d.Write([]byte(strings.Repeat("x", 2*1024*1024)))
	if len(d.line) > maxDiagnosticLine {
		t.Fatal("partial-line memory exceeded its limit")
	}
	_, _ = d.Write([]byte("\n" + strings.Repeat("INF normal output\n", 100000)))
	_, _ = d.Write([]byte("ERR latest handshake failure\n"))
	d.Close()
	data, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxDiagnosticBytes || !strings.Contains(string(data), "diagnostic log truncated") {
		t.Fatalf("file limit/marker missing: size=%d", len(data))
	}
	if !strings.Contains(d.wrap(errors.New("timeout")).Error(), "latest handshake failure") {
		t.Fatal("the full disk log hid the most recent error")
	}
}

func TestDiagnosticLogFailureDoesNotStopOutputCapture(t *testing.T) {
	dir := setDiagnosticTestHome(t)
	if err := os.WriteFile(filepath.Join(dir, ".deploypilot"), []byte("blocks log directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newDiagnosticLog()
	defer d.Close()
	line := []byte("ERR connection refused\n")
	if n, err := d.Write(line); n != len(line) || err != nil {
		t.Fatalf("log failure must not stop pipe draining: %d, %v", n, err)
	}
	want := errors.New("startup failed")
	got := d.wrap(want)
	if !errors.Is(got, want) || !strings.Contains(got.Error(), "connection refused") || !strings.Contains(got.Error(), "diagnostic log unavailable") {
		t.Fatalf("missing cause/log failure: %v", got)
	}
}

func TestDiagnosticLogSeparatesProcesses(t *testing.T) {
	setDiagnosticTestHome(t)
	first, second := newDiagnosticLog(), newDiagnosticLog()
	defer first.Close()
	defer second.Close()
	if first.path == "" || second.path == "" || first.path == second.path {
		t.Fatalf("logs must have distinct paths: %q, %q", first.path, second.path)
	}
}
