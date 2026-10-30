package tunnel

import (
	"strings"
	"unicode/utf8"

	qrterminal "github.com/mdp/qrterminal/v3"
)

// QR renders link as a terminal QR code and reports how many columns its
// widest row needs. Half blocks (▀ ▄ █) keep it compact; otherwise each module
// is two spaces with an ANSI background colour, which stays aligned on
// consoles that draw block characters two cells wide.
func QR(link string, halfBlocks bool) (text string, columns int) {
	var b strings.Builder
	cfg := qrterminal.Config{
		Level:     qrterminal.M,
		Writer:    &b,
		QuietZone: 1,
	}
	if halfBlocks {
		cfg.HalfBlocks = true
		cfg.BlackChar = qrterminal.BLACK_BLACK
		cfg.WhiteChar = qrterminal.WHITE_WHITE
		cfg.BlackWhiteChar = qrterminal.BLACK_WHITE
		cfg.WhiteBlackChar = qrterminal.WHITE_BLACK
	} else {
		cfg.BlackChar = qrterminal.BLACK
		cfg.WhiteChar = qrterminal.WHITE
		cfg.QuietZone = 2
	}
	qrterminal.GenerateWithConfig(link, cfg)
	text = b.String()
	for _, row := range strings.Split(text, "\n") {
		width := utf8.RuneCountInString(row)
		if !halfBlocks {
			width = strings.Count(row, " ") // escapes carry no spaces; each cell is two
		}
		columns = max(columns, width)
	}
	return text, columns
}
