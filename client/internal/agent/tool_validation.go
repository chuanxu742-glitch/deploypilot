package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

func decodeToolArgs(input string, out any, required ...string) error {
	if len(input) > 512*1024 {
		return fmt.Errorf("arguments exceed 512 KiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &fields); err != nil || fields == nil {
		return fmt.Errorf("arguments must be a JSON object")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing required field %q", key)
		}
	}
	for key, value := range fields {
		if string(value) == "null" {
			return fmt.Errorf("field %q must not be null", key)
		}
	}
	d := json.NewDecoder(strings.NewReader(input))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("arguments contain trailing data")
	}
	return nil
}

func nonempty(value, name string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be nonempty and contain no NUL", name)
	}
	return nil
}

// Validate before execution: missing content is not an instruction to truncate
// a file, and an empty old_string must never insert text at every position.
func validateToolArgs(name, input string) error {
	switch name {
	case toolRunCommand:
		var a runCommandArgs
		if err := decodeToolArgs(input, &a, "command"); err != nil {
			return err
		}
		if err := nonempty(a.Command, "command"); err != nil {
			return err
		}
		if a.TimeoutSeconds < 0 || a.TimeoutSeconds > 3600 {
			return fmt.Errorf("timeout_seconds must be 0 (default) or 1..3600")
		}
	case toolReadFile:
		var a readFileArgs
		if err := decodeToolArgs(input, &a, "path"); err != nil {
			return err
		}
		if err := nonempty(a.Path, "path"); err != nil {
			return err
		}
		if a.Offset < 0 || a.MaxBytes < 0 || a.MaxBytes > maxReadBytes {
			return fmt.Errorf("offset must be >= 0; max_bytes must be 0..%d", maxReadBytes)
		}
	case toolWriteFile:
		var a writeFileArgs
		if err := decodeToolArgs(input, &a, "path", "content"); err != nil {
			return err
		}
		return nonempty(a.Path, "path")
	case toolAppendFile:
		var a appendFileArgs
		if err := decodeToolArgs(input, &a, "path", "content"); err != nil {
			return err
		}
		return nonempty(a.Path, "path")
	case toolEditFile:
		var a editFileArgs
		if err := decodeToolArgs(input, &a, "path", "old_string", "new_string"); err != nil {
			return err
		}
		if err := nonempty(a.Path, "path"); err != nil {
			return err
		}
		if a.OldString == "" {
			return fmt.Errorf("old_string must not be empty")
		}
	case toolWaitFor:
		var a waitForArgs
		if err := decodeToolArgs(input, &a, "kind", "target"); err != nil {
			return err
		}
		if err := nonempty(a.Target, "target"); err != nil {
			return err
		}
		if a.TimeoutSeconds < 0 || a.TimeoutSeconds > 3600 {
			return fmt.Errorf("timeout_seconds must be 0 (default) or 1..3600")
		}
		if a.IntervalSeconds < 0 || a.IntervalSeconds > 60 {
			return fmt.Errorf("interval_seconds must be omitted (default) or 1..60")
		}
		// An omitted interval decodes as zero and selects the default. An
		// explicitly supplied zero is rejected to keep the advertised range.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(input), &fields)
		if _, ok := fields["interval_seconds"]; ok && a.IntervalSeconds == 0 {
			return fmt.Errorf("interval_seconds must be 1..60 when provided")
		}
		switch a.Kind {
		case "url":
			u, err := url.Parse(a.Target)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("url target must be an absolute http or https URL")
			}
			if a.Contains != "" {
				return fmt.Errorf("contains is only valid for kind=log")
			}
		case "log":
			if err := nonempty(a.Contains, "contains"); err != nil {
				return fmt.Errorf("kind=log requires contains: %w", err)
			}
		case "process":
			pid, err := strconv.ParseInt(a.Target, 10, 64)
			if err != nil || pid <= 0 {
				return fmt.Errorf("process target must be a positive integer PID")
			}
			if a.Contains != "" {
				return fmt.Errorf("contains is only valid for kind=log")
			}
		default:
			return fmt.Errorf("kind must be url, log, or process")
		}
	case toolFinish:
		var a finishArgs
		if err := decodeToolArgs(input, &a, "summary", "success"); err != nil {
			return err
		}
		if err := nonempty(a.Summary, "summary"); err != nil {
			return err
		}
		switch a.Outcome {
		case "", "needs_input":
		case "completed":
			if !a.Success {
				return fmt.Errorf("outcome=completed requires success=true")
			}
		case "failed":
			if a.Success {
				return fmt.Errorf("outcome=failed requires success=false")
			}
		default:
			return fmt.Errorf("outcome must be completed, failed, needs_input, or omitted")
		}
		if a.Outcome == "needs_input" {
			if a.Success {
				return fmt.Errorf("outcome=needs_input requires success=false")
			}
			if len(a.VerificationCallIDs) > 0 {
				return fmt.Errorf("outcome=needs_input must not reference verification_call_ids")
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown tool %q", name)
	}
	return nil
}
