package banner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/deploypilot/deploypilot/internal/console"
)

func TestRenderPlain(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Color: false, Animate: false, Version: "1.2.3"})
	out := buf.String()
	if strings.Contains(out, "·") {
		t.Fatal("startup copy must not contain middle-dot separators")
	}

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

func TestDefaultOptionsWithoutColorDisablesAnimation(t *testing.T) {
	opts := DefaultOptions(console.Caps{Terminal: true, Width: 120}, "dev")
	if opts.Color || opts.Animate {
		t.Fatalf("a console without ANSI support must get plain, static output: %+v", opts)
	}
}

func TestNarrowTerminalGetsOneLineTitle(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Width: 40, Version: "1.2.3"})
	out := buf.String()
	if strings.Contains(out, "|____/") {
		t.Fatalf("a 40-column window must not get the wrapped ASCII logo: %q", out)
	}
	if !strings.Contains(out, "DeployPilot  "+Tagline) || !strings.Contains(out, "version 1.2.3") {
		t.Fatalf("one-line title must keep name, tagline and version: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		columns := 0
		for _, r := range line {
			if r >= 0x2E80 { // CJK characters take two columns
				columns += 2
			} else {
				columns++
			}
		}
		if columns > 40 {
			t.Fatalf("line %q is %d columns, wider than 40", line, columns)
		}
	}
}

func TestWideTerminalGetsLogo(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Width: 80})
	if !strings.Contains(buf.String(), "|____/") {
		t.Fatalf("an 80-column window gets the ASCII logo: %q", buf.String())
	}
}
