package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/llm"
)

func TestWaitForValidatesKindsAndFinishVerificationIDs(t *testing.T) {
	invalid := []string{
		`{"kind":"url","target":"ftp://example.com"}`,
		`{"kind":"log","target":"app.log"}`,
		`{"kind":"process","target":"0"}`,
		`{"kind":"url","target":"http://example.com","interval_seconds":0}`,
	}
	for _, input := range invalid {
		if err := validateToolArgs(toolWaitFor, input); err == nil {
			t.Errorf("accepted invalid wait_for args: %s", input)
		}
	}
	if err := validateToolArgs(toolWaitFor, `{"kind":"log","target":"app.log","contains":"ready","timeout_seconds":300,"interval_seconds":2}`); err != nil {
		t.Fatalf("valid wait_for rejected: %v", err)
	}
	if err := validateToolArgs(toolFinish, `{"summary":"done","success":true,"verification_call_ids":["run-1","wait-1"]}`); err != nil {
		t.Fatalf("finish verification IDs rejected: %v", err)
	}
	if err := validateToolArgs(toolFinish, `{"summary":"cannot proceed","success":false}`); err != nil {
		t.Fatalf("success=false without evidence rejected: %v", err)
	}
}

func TestWaitForLogMatchesAcrossReadPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	content := strings.Repeat("a", waitReadPageBytes-1) + "XYZ"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := waitFor(context.Background(), NewShellExecutor(), waitForArgs{
		Kind: "log", Target: path, Contains: "XYZ", TimeoutSeconds: 1, IntervalSeconds: 1,
	})
	if err != nil || !strings.Contains(got, `contains "XYZ"`) {
		t.Fatalf("waitFor log = %q, %v", got, err)
	}
}

func TestWaitForURLWaitsThroughUnavailableStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	got, err := waitFor(context.Background(), newFakeExec(), waitForArgs{
		Kind: "url", Target: server.URL, TimeoutSeconds: 3, IntervalSeconds: 1,
	})
	if err != nil || !strings.Contains(got, fmt.Sprint(http.StatusNoContent)) || calls.Load() < 2 {
		t.Fatalf("waitFor URL = %q, %v, calls=%d", got, err, calls.Load())
	}
}

func TestWaitForProgressReportsInitialStateChangesAndSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var elapsed []time.Duration
	var observations []string
	_, err := waitFor(context.Background(), newFakeExec(), waitForArgs{
		Kind: "url", Target: server.URL, TimeoutSeconds: 3, IntervalSeconds: 1,
	}, func(after time.Duration, state string) {
		elapsed = append(elapsed, after)
		observations = append(observations, state)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 3 || observations[0] != "尚未进行首次检查" || !strings.Contains(observations[1], "503") || !strings.Contains(observations[2], "204") {
		t.Fatalf("unexpected progress updates: %q", observations)
	}
	if elapsed[1] < elapsed[0] || elapsed[2] < elapsed[1] {
		t.Fatalf("progress elapsed time moved backward: %v", elapsed)
	}
}

func TestWaitForProgressCallbackCanCancelImmediately(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reports atomic.Int32
	started := time.Now()
	_, err := waitFor(ctx, newFakeExec(), waitForArgs{
		Kind: "url", Target: server.URL, TimeoutSeconds: 60, IntervalSeconds: 1,
	}, func(_ time.Duration, _ string) {
		if reports.Add(1) == 1 {
			cancel()
		}
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") || requests.Load() != 0 || time.Since(started) > time.Second {
		t.Fatalf("cancellation after initial progress = %v, requests=%d elapsed=%s", err, requests.Load(), time.Since(started))
	}
}

func TestWaitForEmitsCorrelatedActivityAndOutputBeforeDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	var events []Event
	a := New(Config{Executor: newFakeExec(), Emit: func(e Event) { events = append(events, e) }})
	_, msg := a.runTool(context.Background(), 9, llm.ToolCall{
		ID: "wait-call", Function: llm.FunctionCall{Name: toolWaitFor, Arguments: `{"kind":"url","target":"` + server.URL + `","timeout_seconds":2}`},
	})
	if strings.HasPrefix(msg.Content, "error:") {
		t.Fatal(msg.Content)
	}
	activity, output, done := -1, -1, -1
	for i, event := range events {
		if event.Type == EventActivity && event.State == "waiting" {
			activity = i
			if event.CallID != "wait-call" || event.Step != 9 || !strings.Contains(event.Text, "等待网址") {
				t.Fatalf("waiting activity lost correlation or description: %+v", event)
			}
		}
		if event.Type == EventOutput && strings.Contains(event.Text, "HTTP 204") {
			output = i
		}
		if event.Type == EventToolDone && event.Tool == toolWaitFor {
			done = i
		}
	}
	if activity < 0 || output < 0 || done < 0 || output >= done {
		t.Fatalf("expected waiting activity and output before tool_done, events=%+v", events)
	}
}

func TestWaitForReturnsExplicitTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	started := time.Now()
	_, err := waitFor(context.Background(), newFakeExec(), waitForArgs{
		Kind: "url", Target: server.URL, TimeoutSeconds: 1, IntervalSeconds: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(started) > 3*time.Second {
		t.Fatalf("expected bounded timeout, got %v after %s", err, time.Since(started))
	}
	if !errors.Is(err, context.DeadlineExceeded) || toolTerminationState(err) != "timeout" {
		t.Fatalf("timeout must retain its cause and UI state, got %v (%q)", err, toolTerminationState(err))
	}
}

func TestPollProcessUsesIndependentProbeAndDistinguishesExit(t *testing.T) {
	currentPID := fmt.Sprint(os.Getpid())
	done, observation, err := pollProcess(context.Background(), currentPID)
	if err != nil || done || !strings.Contains(observation, "still running") {
		t.Fatalf("current process probe = done %v, %q, %v", done, observation, err)
	}
	done, observation, err = pollProcess(context.Background(), "2147483647")
	if err != nil || !done || !strings.Contains(observation, "has exited") {
		t.Fatalf("missing process probe = done %v, %q, %v", done, observation, err)
	}
}
