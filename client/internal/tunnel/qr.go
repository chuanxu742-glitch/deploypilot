package tunnel

import (
	"io"

	qrterminal "github.com/mdp/qrterminal/v3"
)

// ParseTunnelURL returns the first trycloudflare URL found in text, or "".
func ParseTunnelURL(text string) string {
	return urlRe.FindString(text)
}

// PrintQR renders link as a QR code to w using half-block characters so it
// stays compact in a terminal.
func PrintQR(w io.Writer, link string) {
	cfg := qrterminal.Config{
		Level:          qrterminal.M,
		Writer:         w,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		QuietZone:      1,
	}
	qrterminal.GenerateWithConfig(link, cfg)
}
