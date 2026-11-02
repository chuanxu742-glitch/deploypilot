package tunnel

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

const (
	maxDiagnosticBytes = 1024 * 1024
	maxDiagnosticLine  = 8 * 1024
	maxDiagnosticHint  = 400
)

var (
	ansiSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	bearerSecret = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[a-z0-9._~+/=-]+`)
	fieldSecret  = regexp.MustCompile(`(?i)\b(token|authorization|password|secret|api[_-]?key)(\s*[:=]\s*)("[^"]*"|'[^']*'|\[[^\]]*\]|[^\s&,]+)`)
	urlSecret    = regexp.MustCompile(`(?i)([#?&](?:t|token|key|secret|password|api[_-]?key)=)[^&\s"']+`)
	urlUserInfo  = regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`)
)

// diagnosticLog drains the complete process output even after finding its URL.
// Disk and partial-line buffers are bounded; a failed log write must not break
// the tunnel. The most recent error remains available after the file fills.
type diagnosticLog struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	writeErr  error
	written   int
	limited   bool
	line      []byte
	truncated bool
	lastLine  string
	lastError string
}

func newDiagnosticLog() *diagnosticLog {
	d := &diagnosticLog{}
	home, err := os.UserHomeDir()
	if err == nil {
		dir := filepath.Join(home, ".deploypilot", "logs")
		err = os.MkdirAll(dir, 0o700)
		if err == nil {
			d.file, err = os.CreateTemp(dir, "tunnel-*.log")
			if err == nil {
				d.path = d.file.Name()
			}
		}
	}
	d.writeErr = err
	return d
}

func (d *diagnosticLog) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		part := p
		if end >= 0 {
			part = p[:end]
		}
		keep := min(len(part), maxDiagnosticLine-len(d.line))
		d.line = append(d.line, part[:keep]...)
		d.truncated = d.truncated || keep < len(part)
		if end < 0 {
			break
		}
		d.flushLine()
		p = p[end+1:]
	}
	return n, nil
}

func (d *diagnosticLog) flushLine() {
	line := sanitizeDiagnosticLine(string(d.line))
	if d.truncated {
		line += " [line truncated]"
	}
	d.line = d.line[:0]
	d.truncated = false
	if line == "" {
		return
	}
	d.lastLine = diagnosticHint(line)
	if strings.Contains(line, " ERR ") || strings.Contains(line, "level=error") {
		d.lastError = d.lastLine
	}
	if d.file == nil || d.limited || d.writeErr != nil {
		return
	}
	// Reserve room for the marker, so the file never exceeds its stated limit.
	const marker = "[diagnostic log truncated at 1 MiB; recent errors remain in the failure summary]\n"
	if d.written+len(line)+1 > maxDiagnosticBytes-len(marker) {
		d.limited = true
		_, d.writeErr = d.file.WriteString(marker)
		return
	}
	var count int
	count, d.writeErr = d.file.WriteString(line + "\n")
	d.written += count
}

func (d *diagnosticLog) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.line) > 0 {
		d.flushLine()
	}
	if d.file != nil {
		if err := d.file.Close(); err != nil && d.writeErr == nil {
			d.writeErr = err
		}
		d.file = nil
	}
}

func (d *diagnosticLog) wrap(cause error) error {
	if d == nil {
		return cause
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	hint := d.lastError
	if hint == "" {
		hint = d.lastLine
	}
	detail := ""
	if hint != "" {
		detail += "; cloudflared: " + hint
	}
	if d.path != "" {
		detail += "; log: " + d.path
	}
	if d.writeErr != nil {
		detail += fmt.Sprintf("; diagnostic log unavailable: %v", d.writeErr)
	}
	return fmt.Errorf("%w%s", cause, detail)
}

func diagnosticHint(line string) string {
	runes := []rune(line)
	if len(runes) > maxDiagnosticHint {
		return string(runes[:maxDiagnosticHint]) + "…"
	}
	return line
}

func sanitizeDiagnosticLine(line string) string {
	line = ansiSequence.ReplaceAllString(line, "")
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(line, "�"))
	// cloudflared prints its inherited configuration at info level. Omitting
	// these entire records also covers future credential field names.
	if strings.Contains(line, "Settings:") || strings.Contains(line, "Environment variables") {
		return "[cloudflared configuration omitted]"
	}
	line = bearerSecret.ReplaceAllString(line, "$1 [REDACTED]")
	line = fieldSecret.ReplaceAllString(line, "$1$2[REDACTED]")
	line = urlSecret.ReplaceAllString(line, "$1[REDACTED]")
	return urlUserInfo.ReplaceAllString(line, "$1[REDACTED]@")
}
