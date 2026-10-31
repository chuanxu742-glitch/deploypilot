package server

import "strings"

// defaultShellCols is the terminal width before any viewer resizes it.
const defaultShellCols = 100

// commandLabel is the one-line echo of an Agent command in the terminal. A
// label that wraps makes PSReadLine redraw the input, which prints the
// command two or three times; the full command is in the task timeline.
func commandLabel(command string, cols int) string {
	label := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, command)
	// "> . dp # " plus "..." and one spare cell.
	limit := min(180, max(16, cols-len("> . dp # ")-4))
	width, cut := 0, -1
	for i, r := range label {
		w := 1
		if runeIsWide(r) {
			w = 2
		}
		if width+w > limit {
			cut = i
			break
		}
		width += w
	}
	if cut >= 0 {
		label = label[:cut] + "..."
	}
	return label
}

// runeIsWide covers the East Asian ranges a terminal draws two cells wide.
func runeIsWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || (r >= 0x2E80 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) || (r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE4F) || (r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) || (r >= 0x20000 && r <= 0x3FFFD)
}
