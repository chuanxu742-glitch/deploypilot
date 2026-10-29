package agent

import (
	"encoding/json"
	"fmt"
	"io"
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
	case toolFinish:
		var a finishArgs
		if err := decodeToolArgs(input, &a, "summary", "success"); err != nil {
			return err
		}
		return nonempty(a.Summary, "summary")
	default:
		return fmt.Errorf("unknown tool %q", name)
	}
	return nil
}
