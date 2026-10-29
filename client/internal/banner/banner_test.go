package banner

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderPlain(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Color: false, Animate: false, Version: "1.2.3"})
	out := buf.String()

	if strings.Contains(out, "\x1b[") {
		t.Fatalf("plain output must not contain ANSI escape codes, got: %q", out)
	}
	if !strings.Contains(out, Tagline) {
		t.Fatalf("output missing tagline: %q", out)
	}
	if !strings.Contains(out, "version 1.2.3") {
		t.Fatalf("output missing version footer: %q", out)
	}
	// Logo should contribute several lines.
	if n := strings.Count(out, "\n"); n < 6 {
		t.Fatalf("expected multi-line banner, got %d lines", n)
	}
}

func TestRenderColor(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Color: true, Animate: false, Version: ""})
	out := buf.String()

	if !strings.Contains(out, "\x1b[38;5;") {
		t.Fatalf("color output must contain ANSI color codes, got: %q", out)
	}
	if strings.Contains(out, "version ") {
		t.Fatalf("empty version must not render footer, got: %q", out)
	}
}

func TestDefaultOptionsNonTTY(t *testing.T) {
	var buf bytes.Buffer
	opts := DefaultOptions(&buf, "dev")
	if opts.Color || opts.Animate {
		t.Fatalf("non-tty writer must disable color and animation: %+v", opts)
	}
}
