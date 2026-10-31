package banner

import (
	"strings"
	"testing"
	"time"

	"github.com/deploypilot/deploypilot/internal/tunnel"
)

// realLink has the length of a real pairing link: a four-word quick-tunnel
// host and a 32-byte base64url token (84 characters).
const realLink = "https://regulation-relief-hearing-baker.trycloudflare.com/#t=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func samplePairing(link string) Pairing {
	qr, cols := tunnel.QR(link, true)
	return Pairing{
		Heading: "用手机扫码，打开工作台", QR: qr, QRColumns: cols, QRHalfBlocks: true, Link: link,
		Notes: []string{"10 分钟内有效，仅限配对一次", "同一时间只能一台设备接入", "", "保持此窗口开启", "按 Ctrl+C 结束连接"},
	}
}

func TestPairingPutsNotesBesideTheCodeWhenThereIsRoom(t *testing.T) {
	p := samplePairing(realLink)
	out := stripANSI(RenderPairing(Options{Color: true, Blocks: true, Width: 80}, p))
	lines := strings.Split(out, "\n")
	qrStart := 1 // after the leading blank line
	heading := lines[qrStart+panelTop]
	if !strings.Contains(heading, p.Heading) || !strings.ContainsAny(heading, "█▀▄") {
		t.Fatalf("heading is not beside the first module row: %q", heading)
	}
	col := displayWidthTest(heading[:strings.Index(heading, p.Heading)])
	if want := len(margin) + p.QRColumns + panelGap; col != want {
		t.Fatalf("panel starts at column %d, want %d", col, want)
	}
	for _, n := range p.Notes {
		if n != "" && !strings.Contains(out, n) {
			t.Fatalf("note %q missing:\n%s", n, out)
		}
	}
	if strings.Count(out, p.Heading) != 1 {
		t.Fatal("heading printed twice")
	}
	// 84 columns do not fit in 80: start at column 0 so the wrap lines up.
	if !strings.Contains(out, "\n"+realLink+"\n") {
		t.Fatal("long link is not printed whole, flush left, on its own line")
	}
	wide := stripANSI(RenderPairing(Options{Color: true, Blocks: true, Width: 120}, p))
	if !strings.Contains(wide, "\n"+margin+realLink+"\n") {
		t.Fatal("link that fits is not indented to the margin")
	}
}

func TestTextColourFollowsTheTerminalTheme(t *testing.T) {
	if got := Heading(Options{Color: true}, "标题"); !strings.HasPrefix(got, margin+"\x1b[1;39m") {
		t.Fatalf("heading should use the default foreground: %q", got)
	}
	if got := mix(unlitColor, textColor, 0.5); got == defaultFG || got[0] < 0 {
		t.Fatalf("fade towards the default foreground must stay a real colour: %v", got)
	}
	if got := mix(unlitColor, textColor, 1); got != defaultFG {
		t.Fatalf("fade must end on the default foreground: %v", got)
	}
}

func TestPairingStacksWhenNarrow(t *testing.T) {
	p := samplePairing(realLink)
	out := stripANSI(RenderPairing(Options{Color: true, Blocks: true, Width: 60}, p))
	lines := strings.Split(out, "\n")
	if strings.TrimSpace(lines[1]) != p.Heading {
		t.Fatalf("narrow layout should start with the heading: %q", lines[1])
	}
	link, last := strings.Index(out, realLink), strings.Index(out, "按 Ctrl+C 结束连接")
	if link < 0 || last < link {
		t.Fatalf("narrow layout should end with the notes after the link:\n%s", out)
	}
}

func TestPairingNeverWrapsExceptTheLink(t *testing.T) {
	for _, link := range []string{"https://abc-def-ghi.trycloudflare.com/#t=x", realLink} {
		p := samplePairing(link)
		for _, width := range []int{120, 80, 79, 72, 60, 50, 45, 41} {
			if p.QRColumns > width {
				continue
			}
			for _, color := range []bool{true, false} {
				out := RenderPairing(Options{Color: color, Blocks: true, Width: width}, p)
				for _, line := range strings.Split(stripANSI(out), "\n") {
					if strings.Contains(line, link) {
						continue // a link wider than the window wraps; it is never cut
					}
					if got := displayWidthTest(line); got > width || (got == width && !strings.ContainsAny(line, "█▀▄ ")) {
						t.Errorf("width %d color %v: line is %d columns: %q", width, color, got, line)
					}
				}
			}
		}
	}
}

func TestPairingQRIsDarkOnLightInEveryTheme(t *testing.T) {
	p := samplePairing(realLink)
	out := RenderPairing(Options{Color: true, Blocks: true, Width: 80}, p)
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.ContainsAny(stripANSI(line), "█▀▄") {
			rows++
			if !strings.Contains(line, qrStyle) {
				t.Fatalf("QR row without explicit colours: %q", line)
			}
		}
	}
	if rows == 0 {
		t.Fatal("no QR rows")
	}
	plain := RenderPairing(Options{Blocks: true, Width: 80}, p)
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("plain pairing contains ANSI controls")
	}
}

func TestPairingNoticeReplacesTheCode(t *testing.T) {
	out := stripANSI(RenderPairing(Options{Color: true, Width: 40}, Pairing{Heading: "扫码", Notice: "二维码无法显示", Link: realLink, Notes: []string{"保持此窗口开启"}}))
	if !strings.Contains(out, "二维码无法显示") || !strings.Contains(out, realLink) || !strings.Contains(out, "保持此窗口开启") {
		t.Fatalf("notice layout incomplete:\n%s", out)
	}
}

func TestStatusLines(t *testing.T) {
	at := time.Date(2026, 10, 30, 14, 32, 0, 0, time.Local)
	got := stripANSI(status(Options{Color: true, Blocks: true}, ToneWarning, "隧道暂不可用，2s 后重试", "exit status 1", at))
	if got != "  ! 隧道暂不可用，2s 后重试  14:32\n    exit status 1\n" {
		t.Fatalf("warning status: %q", got)
	}
	if got := status(Options{}, ToneSuccess, "隧道已重新建立", "", at); got != "  + 隧道已重新建立  14:32\n" {
		t.Fatalf("plain success status: %q", got)
	}
	if got := status(Options{Color: true, Blocks: true}, ToneFailure, "失败", "", at); !strings.Contains(got, "×") {
		t.Fatalf("failure marker missing: %q", got)
	}
}
