// Package banner renders the startup welcome screen.
package banner

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Logo is the ASCII art shown on startup.
const Logo = `
 ____             _            ____  _ _       _
|  _ \  ___ _ __ | | ___  _  _|  _ \(_) | ___ | |_
| | | |/ _ \ '_ \| |/ _ \| || | |_) | | |/ _ \| __|
| |_| |  __/ |_) | | (_) | || |  __/| | | (_) | |_
|____/ \___| .__/|_|\___/ \_, |_|   |_|_|\___/ \__|
           |_|            |__/
`

// Tagline is shown under the logo.
const Tagline = "Remote deploy assistant · 一行命令，扫码即接入"

// Options controls how the banner is rendered.
type Options struct {
	// Color enables ANSI colors.
	Color bool
	// Animate enables the per-line reveal animation.
	Animate bool
	// LineDelay is the pause between animated lines.
	LineDelay time.Duration
	// Version is printed in the footer.
	Version string
}

// gradient is a set of ANSI 256-color codes used top-to-bottom.
var gradient = []int{39, 38, 44, 43, 49, 48}

// DefaultOptions derives options from the given writer and environment.
// Color and animation are disabled when the output is not a TTY or when
// NO_COLOR is set.
func DefaultOptions(w io.Writer, version string) Options {
	tty := isTerminal(w) && os.Getenv("NO_COLOR") == ""
	return Options{
		Color:     tty,
		Animate:   tty,
		LineDelay: 45 * time.Millisecond,
		Version:   version,
	}
}

// Render writes the banner to w according to opts.
func Render(w io.Writer, opts Options) {
	lines := strings.Split(strings.Trim(Logo, "\n"), "\n")
	for i, line := range lines {
		if opts.Color {
			code := gradient[i%len(gradient)]
			fmt.Fprintf(w, "\x1b[38;5;%dm%s\x1b[0m\n", code, line)
		} else {
			fmt.Fprintln(w, line)
		}
		if opts.Animate && opts.LineDelay > 0 {
			time.Sleep(opts.LineDelay)
		}
	}

	tagline := Tagline
	footer := Tagline
	_ = footer
	if opts.Color {
		fmt.Fprintf(w, "\x1b[1;36m%s\x1b[0m\n", tagline)
	} else {
		fmt.Fprintln(w, tagline)
	}

	if opts.Version != "" {
		ver := "version " + opts.Version
		if opts.Color {
			fmt.Fprintf(w, "\x1b[2m%s\x1b[0m\n", ver)
		} else {
			fmt.Fprintln(w, ver)
		}
	}
	fmt.Fprintln(w)
}

// isTerminal reports whether w is a terminal (an *os.File backed by a
// character device).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
