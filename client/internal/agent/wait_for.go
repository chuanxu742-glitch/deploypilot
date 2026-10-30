package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultWaitTimeout  = 300 * time.Second
	defaultWaitInterval = 2 * time.Second
	maxLogScanPerPoll   = 1 << 20
	waitReadPageBytes   = 256 << 10
)

// waitFor polls the requested condition until it is observed, the context is
// canceled, or timeout_seconds elapses. It returns a concise observation that
// callers can retain as verification evidence.
// An optional synchronous callback reports initial state and changes, with
// unchanged observations throttled to ten-second intervals between polls.
// Reporting progress does not create extra model calls.
func waitFor(ctx context.Context, executor Executor, args waitForArgs, progress ...func(time.Duration, string)) (string, error) {
	if executor == nil {
		return "", fmt.Errorf("wait_for requires an executor")
	}
	if err := validateWaitForArgs(args); err != nil {
		return "", err
	}
	timeout := defaultWaitTimeout
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	interval := defaultWaitInterval
	if args.IntervalSeconds > 0 {
		interval = time.Duration(args.IntervalSeconds) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	var report func(time.Duration, string)
	if len(progress) > 0 {
		report = progress[0]
	}
	lastReportAt := time.Time{}
	lastReportedState := ""
	reportProgress := func(state string, force bool) {
		if report == nil || (!force && state == lastReportedState && time.Since(lastReportAt) < 10*time.Second) {
			return
		}
		elapsed := time.Since(started)
		report(elapsed, state)
		lastReportAt = time.Now()
		lastReportedState = state
	}
	lastObservation := "尚未进行首次检查"
	reportProgress(lastObservation, true)
	if waitCtx.Err() != nil {
		return "", waitContextError(ctx, waitCtx, timeout)
	}

	var client *http.Client
	var logOffset int64
	var logCarry string
	logStarted := false
	for {
		var done bool
		var observation string
		var err error
		switch args.Kind {
		case "url":
			done, observation, err = pollURL(waitCtx, client, args.Target)
		case "log":
			done, observation, logOffset, logCarry, logStarted, err = pollLog(executor, args.Target, args.Contains, logOffset, logCarry, logStarted)
		case "process":
			done, observation, err = pollProcess(waitCtx, args.Target)
		}
		if err != nil {
			if waitCtx.Err() != nil {
				reportProgress(lastObservation, true)
				return "", waitContextError(ctx, waitCtx, timeout)
			}
			reportProgress("检测失败："+err.Error(), true)
			return "", fmt.Errorf("wait_for %s: %w", args.Kind, err)
		}
		if observation != "" {
			lastObservation = observation
			reportProgress(lastObservation, false)
		}
		if done {
			return observation, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			reportProgress(lastObservation, true)
			return "", fmt.Errorf("%w (%s)", waitContextError(ctx, waitCtx, timeout), lastObservation)
		case <-timer.C:
		}
	}
}

func validateWaitForArgs(args waitForArgs) error {
	if err := nonempty(args.Target, "target"); err != nil {
		return err
	}
	if args.TimeoutSeconds < 0 || args.TimeoutSeconds > 3600 {
		return fmt.Errorf("timeout_seconds must be 0 (default) or 1..3600")
	}
	if args.IntervalSeconds < 0 || args.IntervalSeconds > 60 {
		return fmt.Errorf("interval_seconds must be omitted (default) or 1..60")
	}
	switch args.Kind {
	case "url":
		u, err := url.Parse(args.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("url target must be an absolute http or https URL")
		}
		if args.Contains != "" {
			return fmt.Errorf("contains is only valid for kind=log")
		}
	case "log":
		if err := nonempty(args.Contains, "contains"); err != nil {
			return fmt.Errorf("kind=log requires contains: %w", err)
		}
	case "process":
		pid, err := strconv.ParseInt(args.Target, 10, 64)
		if err != nil || pid <= 0 {
			return fmt.Errorf("process target must be a positive integer PID")
		}
		if args.Contains != "" {
			return fmt.Errorf("contains is only valid for kind=log")
		}
	default:
		return fmt.Errorf("kind must be url, log, or process")
	}
	return nil
}

func waitForActivityText(args waitForArgs, elapsed time.Duration, observation string) string {
	timeout := args.TimeoutSeconds
	if timeout == 0 {
		timeout = int(defaultWaitTimeout / time.Second)
	}
	var waiting string
	switch args.Kind {
	case "url":
		waiting = "等待网址 " + args.Target + " 可访问"
	case "log":
		waiting = fmt.Sprintf("等待日志 %s 出现文本 %q", args.Target, args.Contains)
	case "process":
		waiting = "等待进程 " + args.Target + " 退出"
	default:
		waiting = "等待条件满足"
	}
	return fmt.Sprintf("%s：已等 %d 秒，超时上限 %d 秒；最近观察：%s", waiting, int(elapsed.Seconds()), timeout, observation)
}

func waitContextError(parent, waitCtx context.Context, timeout time.Duration) error {
	if parent.Err() != nil {
		return fmt.Errorf("wait_for cancelled: %w", parent.Err())
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("wait_for timed out after %s", timeout)
	}
	return fmt.Errorf("wait_for cancelled: %w", waitCtx.Err())
}

func pollURL(ctx context.Context, client *http.Client, target string) (bool, string, error) {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		// Startup often includes a period when the listener is not available.
		return false, "URL not ready: " + err.Error(), nil
	}
	defer resp.Body.Close()
	// Consume only a small amount so repeated polls do not retain large bodies.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	obs := fmt.Sprintf("URL %s returned HTTP %d", target, resp.StatusCode)
	return resp.StatusCode >= 200 && resp.StatusCode < 400, obs, nil
}

func pollLog(executor Executor, path, contains string, offset int64, carry string, started bool) (bool, string, int64, string, bool, error) {
	readBudget := int64(maxLogScanPerPoll)
	if !started {
		// Start at the recent tail so a large historical log cannot turn the
		// initial wait into an unbounded read. Matches split across pages survive
		// through carry below.
		first, err := executor.ReadFile(path, 0, 1)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, "log file not present yet", 0, carry, false, nil
			}
			return false, "", offset, carry, false, err
		}
		if first.Size > readBudget {
			offset = first.Size - readBudget
		}
		started = true
	} else {
		// If the file was truncated or rotated, restart from its beginning.
		info, err := executor.ReadFile(path, 0, 1)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, "log file not present yet", offset, carry, started, nil
			}
			return false, "", offset, carry, started, err
		}
		if info.Size < offset {
			offset, carry = 0, ""
		}
	}
	for readBudget > 0 {
		pageSize := min(int64(waitReadPageBytes), readBudget)
		page, err := executor.ReadFile(path, offset, pageSize)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, "log file not present yet", offset, carry, started, nil
			}
			return false, "", offset, carry, started, err
		}
		if strings.Contains(carry+page.Content, contains) {
			return true, fmt.Sprintf("log %s contains %q", path, contains), page.NextOffset, "", started, nil
		}
		consumed := len(page.Content)
		if consumed == 0 {
			if page.EOF || page.NextOffset >= page.Size {
				return false, "log does not contain requested text yet", offset, carry, started, nil
			}
			return false, "", offset, carry, started, fmt.Errorf("read_file made no progress at byte %d", offset)
		}
		combined := carry + page.Content
		keep := len(contains) - 1
		if keep > len(combined) {
			keep = len(combined)
		}
		if keep > 0 {
			carry = combined[len(combined)-keep:]
		} else {
			carry = ""
		}
		offset = page.NextOffset
		readBudget -= int64(consumed)
		if page.EOF || page.NextOffset >= page.Size {
			return false, "log does not contain requested text yet", offset, carry, started, nil
		}
	}
	return false, "log scan budget exhausted; continuing from last byte", offset, carry, started, nil
}

func pollProcess(ctx context.Context, pidText string) (bool, string, error) {
	pid, err := strconv.ParseInt(pidText, 10, 64)
	if err != nil || pid <= 0 {
		return false, "", fmt.Errorf("process target must be a positive integer PID")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = fmt.Sprintf("try { Get-Process -Id %d -ErrorAction Stop | Out-Null; exit 0 } catch { if ($_.CategoryInfo.Category -eq 'ObjectNotFound') { exit 1 }; [Console]::Error.WriteLine($_); exit 2 }", pid)
	} else {
		cmd = fmt.Sprintf("msg=$(LC_ALL=C kill -0 %d 2>&1); rc=$?; if [ $rc -eq 0 ]; then exit 0; fi; case \"$msg\" in *'No such process'*) exit 1;; *) printf 'process check failed: %%s\\n' \"$msg\" >&2; exit 2;; esac", pid)
	}
	name, args := shellCommand(cmd)
	probe := exec.CommandContext(checkCtx, name, args...)
	outputBytes, err := probe.CombinedOutput()
	output := string(outputBytes)
	if err != nil {
		if checkCtx.Err() != nil {
			return false, "", fmt.Errorf("process detection cancelled: %w", checkCtx.Err())
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, "", fmt.Errorf("process detection failed: %w", err)
		}
		if exitErr.ExitCode() == 1 {
			return true, fmt.Sprintf("process %d has exited", pid), nil
		}
		return false, "", fmt.Errorf("process detection failed (exit %d): %s", exitErr.ExitCode(), strings.TrimSpace(output))
	}
	if checkCtx.Err() != nil {
		return false, "", fmt.Errorf("process detection cancelled: %w", checkCtx.Err())
	}
	if probe.ProcessState == nil {
		return false, "", fmt.Errorf("process detection failed: probe has no process state")
	}
	if !probe.ProcessState.Success() {
		return false, "", fmt.Errorf("process detection failed: %s", strings.TrimSpace(output))
	}
	return false, fmt.Sprintf("process %d is still running", pid), nil
}
