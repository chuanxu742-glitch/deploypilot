package banner

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Pairing is the block that shows the operator how to connect: the QR code
// with a short panel of instructions beside it when the window is wide
// enough, otherwise stacked, followed by the link.
type Pairing struct {
	// Heading is the main instruction, such as 用手机扫码，打开工作台.
	Heading string
	// QR is the rendered code, one line per row, and QRColumns its width.
	// Leave QR empty and set Notice when the code cannot be drawn.
	QR        string
	QRColumns int
	// QRHalfBlocks marks a ▀ ▄ █ code. With colour on it is drawn in explicit
	// white on black so it scans the same in light and dark terminal themes.
	QRHalfBlocks bool
	// Notice replaces the QR code when it cannot be shown.
	Notice string
	// Notes are short facts and actions under the heading; "" separates
	// groups.
	Notes []string
	// Link is the pairing URL, printed whole so it copies as one token.
	Link string
}

const (
	panelGap = 4 // columns between the QR code and the panel
	panelTop = 1 // panel starts level with the first module row
)

// noteKeys are key names highlighted inside notes.
var noteKeys = []string{"Ctrl+C"}

// qrStyle paints half-block codes white on black regardless of the theme.
var qrStyle = fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm", 255, 255, 255, 0, 0, 0)

// RenderPairing formats p for the terminal described by opts.
func RenderPairing(opts Options, p Pairing) string {
	qr := qrLines(opts, p)
	indent := margin
	if opts.Width > 0 && len(margin)+p.QRColumns >= opts.Width {
		indent = "" // the code needs every column
	}
	panel := []string{style(opts, textColor, true, false, cleanLine(p.Heading)), ""}
	panelWidth := displayWidth(p.Heading)
	for _, n := range p.Notes {
		panel = append(panel, note(opts, n))
		panelWidth = max(panelWidth, displayWidth(n))
	}
	side := len(qr) > 0 && opts.Width > 0 && indent != "" &&
		len(margin)+p.QRColumns+panelGap+panelWidth < opts.Width

	var b strings.Builder
	b.WriteByte('\n')
	if side {
		pad := strings.Repeat(" ", p.QRColumns)
		for i := range max(len(qr), panelTop+len(panel)) {
			j := i - panelTop
			text := j >= 0 && j < len(panel) && panel[j] != ""
			switch {
			case i < len(qr):
				b.WriteString(indent + qr[i])
			case text:
				b.WriteString(indent + pad)
			}
			if text {
				b.WriteString(strings.Repeat(" ", panelGap) + panel[j])
			}
			b.WriteByte('\n')
		}
		writeLink(&b, opts, p.Link)
		return b.String()
	}
	b.WriteString(Heading(opts, p.Heading) + "\n")
	switch {
	case len(qr) > 0:
		for _, line := range qr {
			b.WriteString(indent + line + "\n")
		}
	case p.Notice != "":
		b.WriteString(margin + style(opts, textColor, false, false, cleanLine(p.Notice)) + "\n")
	}
	writeLink(&b, opts, p.Link)
	if len(p.Notes) > 0 {
		b.WriteByte('\n')
		for _, n := range p.Notes {
			if n != "" {
				b.WriteString(margin + note(opts, n))
			}
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// writeLink prints the link whole so it copies as one token. A link wider
// than the window starts at column 0, so the terminal's wrapped
// continuation lines up under it.
func writeLink(b *strings.Builder, opts Options, link string) {
	if link == "" {
		return
	}
	line := Link(opts, link)
	if opts.Width > 0 && len(margin)+displayWidth(link) >= opts.Width {
		line = strings.TrimPrefix(line, margin)
	}
	b.WriteString("\n" + Hint(opts, "或在浏览器中打开") + "\n" + line + "\n")
}

func qrLines(opts Options, p Pairing) []string {
	text := strings.TrimRight(p.QR, "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if p.QRHalfBlocks && opts.Color {
		for i, line := range lines {
			// The quiet-zone rows at the top and bottom are half-filled; on
			// the black background the empty half shows as a stripe, so
			// fill them.
			if edge := strings.Trim(line, "▄"); edge == "" || strings.Trim(line, "▀") == "" {
				line = strings.Repeat("█", utf8.RuneCountInString(line))
			}
			lines[i] = qrStyle + line + "\x1b[0m"
		}
	}
	return lines
}

// note is muted text with any key name in the text colour.
func note(opts Options, s string) string {
	s = cleanLine(s)
	if s == "" || !opts.Color {
		return s
	}
	for _, key := range noteKeys {
		if i := strings.Index(s, key); i >= 0 {
			return style(opts, mutedColor, false, false, s[:i]) +
				style(opts, textColor, true, false, key) +
				style(opts, mutedColor, false, false, s[i+len(key):])
		}
	}
	return style(opts, mutedColor, false, false, s)
}

// Tone colours a status line.
type Tone uint8

const (
	// ToneInfo is a neutral event, such as shutting down.
	ToneInfo Tone = iota
	// ToneSuccess is a completed step, such as the tunnel being rebuilt.
	ToneSuccess
	// ToneWarning is a recoverable problem, such as a retry.
	ToneWarning
	// ToneFailure is an error that stops the step.
	ToneFailure
)

var warningColor = rgb{245, 190, 80}

// Status formats an event after startup as "  √ message  14:32", with an
// optional muted detail line under it. Text is never cut: a long cause
// wraps rather than losing its end.
func Status(opts Options, tone Tone, message, detail string) string {
	return status(opts, tone, message, detail, time.Now())
}

func status(opts Options, tone Tone, message, detail string, now time.Time) string {
	marker, markerColor, messageColor := toneStyle(opts, tone)
	line := margin + style(opts, markerColor, true, false, marker) + " " +
		style(opts, messageColor, false, false, cleanLine(message)) + "  " +
		style(opts, mutedColor, false, false, now.Format("15:04")) + "\n"
	if detail != "" {
		line += margin + "  " + style(opts, mutedColor, false, false, cleanLine(detail)) + "\n"
	}
	return line
}

func toneStyle(opts Options, tone Tone) (marker string, markerColor, messageColor rgb) {
	switch tone {
	case ToneSuccess:
		return successMarker(opts), successColor, textColor
	case ToneWarning:
		return "!", warningColor, textColor
	case ToneFailure:
		return failureMarker(opts), failureColor, textColor
	default:
		return glyph(opts, "·", "-"), mutedColor, mutedColor
	}
}

// glyph picks the symbol when the console draws it one cell wide, and the
// ASCII fallback otherwise.
func glyph(opts Options, fancy, plain string) string {
	if opts.Blocks {
		return fancy
	}
	return plain
}

func successMarker(opts Options) string { return glyph(opts, "√", "+") }
func failureMarker(opts Options) string { return glyph(opts, "×", "x") }
