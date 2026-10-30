// Package banner renders the startup welcome screen.
package banner

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/deploypilot/deploypilot/internal/console"
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
const Tagline = "远程部署助手"

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
	// Width is the terminal width in columns; 0 means unknown. A window
	// narrower than the logo gets a one-line title instead of wrapped art.
	Width int
}

// gradient is a set of ANSI 256-color codes used top-to-bottom.
var gradient = []int{39, 38, 44, 43, 49, 48}

// DefaultOptions derives options from the terminal's capabilities: colour and
// animation only where ANSI sequences are interpreted.
func DefaultOptions(caps console.Caps, version string) Options {
	return Options{
		Color:     caps.Color,
		Animate:   caps.Color,
		LineDelay: 45 * time.Millisecond,
		Version:   version,
		Width:     caps.Width,
	}
}

// Render writes the banner to w according to opts.
func Render(w io.Writer, opts Options) {
	lines := strings.Split(strings.Trim(Logo, "\n"), "\n")
	if opts.Width > 0 && opts.Width < logoWidth(lines) {
		writeStyled(w, opts.Color, "1;36", "DeployPilot  "+Tagline)
	} else {
		for i, line := range lines {
			writeStyled(w, opts.Color, fmt.Sprintf("38;5;%d", gradient[i%len(gradient)]), line)
			if opts.Animate && opts.LineDelay > 0 {
				time.Sleep(opts.LineDelay)
			}
		}
		writeStyled(w, opts.Color, "1;36", Tagline)
	}
	if opts.Version != "" {
		writeStyled(w, opts.Color, "2", "version "+opts.Version)
	}
	fmt.Fprintln(w)
}

// writeStyled prints one line, wrapped in the SGR style when color is on.
func writeStyled(w io.Writer, color bool, style, text string) {
	if color {
		fmt.Fprintf(w, "\x1b[%sm%s\x1b[0m\n", style, text)
	} else {
		fmt.Fprintln(w, text)
	}
}

func logoWidth(lines []string) int {
	width := 0
	for _, line := range lines {
		width = max(width, len(line))
	}
	return width
}
