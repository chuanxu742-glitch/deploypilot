// Package console works out what the operator's terminal can display and
// prepares it. The classic Windows console (conhost) only renders ANSI colours
// after a program switches them on, otherwise the banner shows raw "←[38;5;39m"
// codes; some console fonts also draw block characters two cells wide, which
// breaks the half-block QR code.
package console

import (
	"os"

	"golang.org/x/term"
)

// Caps describes what f can display.
type Caps struct {
	// Terminal reports whether output goes to an interactive terminal.
	Terminal bool
	// Color reports whether ANSI escape sequences are interpreted.
	Color bool
	// NarrowBlocks reports whether ▀ ▄ █ occupy one cell each, which the
	// compact half-block QR code needs to line up.
	NarrowBlocks bool
	// Width is the terminal width in columns, or 0 when unknown.
	Width int
	// Height is the terminal height in rows, or 0 when unknown.
	Height int
}

// Prepare inspects f and, on Windows, enables ANSI processing for it. Call
// restore before exiting to put the console mode back.
func Prepare(f *os.File) (caps Caps, restore func()) {
	restore = func() {}
	if !term.IsTerminal(int(f.Fd())) {
		// Logs and pipes get plain text; block characters are harmless there.
		return Caps{NarrowBlocks: true}, restore
	}
	caps.Terminal = true
	if width, height, err := term.GetSize(int(f.Fd())); err == nil {
		caps.Width, caps.Height = width, height
	}
	caps.NarrowBlocks = narrowBlocks(f)
	ansi, restore := enableANSI(f)
	caps.Color = ansi && os.Getenv("NO_COLOR") == ""
	return caps, restore
}

// QRMode chooses how a QR code can be drawn on a terminal with these caps.
type QRMode int

const (
	// QRHalfBlocks draws two modules per cell with ▀ ▄ █ (compact).
	QRHalfBlocks QRMode = iota
	// QRColorCells draws each module as two spaces with an ANSI background;
	// spaces never change width, so it survives CJK consoles.
	QRColorCells
	// QRNone means neither works; show only the link.
	QRNone
)

// QR returns the QR drawing mode for c.
func (c Caps) QR() QRMode {
	switch {
	case c.NarrowBlocks:
		return QRHalfBlocks
	case c.Color:
		return QRColorCells
	default:
		return QRNone
	}
}
