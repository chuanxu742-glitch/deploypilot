package banner

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestShortTerminalUsesStaticOutput(t *testing.T) {
	for _, width := range []int{30, 60, 80} {
		opts := Options{Color: true, Blocks: true, Width: width, Version: "0.4.9"}
		var static bytes.Buffer
		Render(&static, opts)
		// One leading and one trailing newline are outside the banner rows.
		rows := strings.Count(static.String(), "\n") - 2
		for _, height := range []int{1, rows, rows + 1, rows + 2} {
			opts.Animate, opts.Height = true, height
			var out bytes.Buffer
			Render(&out, opts)
			if height <= rows+1 {
				if out.String() != static.String() {
					t.Fatalf("%dx%d: insufficient height must use complete static banner", width, height)
				}
			} else {
				assertFinishedScreen(t, out.String(), static.String(), width)
			}
		}
	}
}

func TestIntroCancellationAndStopFinishTheScreen(t *testing.T) {
	for _, action := range []string{"cancel", "stop"} {
		t.Run(action, func(t *testing.T) {
			opts := Options{Color: true, Animate: true, Blocks: true, Width: 80, Height: 24, Version: "0.4.9", FrameDelay: time.Hour}
			var out, static bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := Play(ctx, &out, opts)
			if action == "cancel" {
				cancel()
			} else {
				in.Stop()
				in.Stop()
			}
			select {
			case <-in.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("intro did not finish after cancellation")
			}
			in.Wait()
			opts.Animate = false
			Render(&static, opts)
			assertFinishedScreen(t, out.String(), static.String(), 80)
		})
	}
}

func TestProgressWaitsForIntroAndLeavesCompletedScreen(t *testing.T) {
	opts := Options{Color: true, Animate: true, Blocks: true, Width: 80, Height: 24, Version: "0.4.9", FrameDelay: time.Hour}
	var out, static bytes.Buffer
	in := Play(context.Background(), &out, opts)
	p := in.StartProgress("FIRST-STAGE")
	p.Set("LATEST-STAGE")
	in.Stop()
	p.FinishSuccess("READY-FINAL")
	p.Set("AFTER-FINISH")
	screen := readScreen(t, out.String(), 80)
	opts.Animate = false
	Render(&static, opts)
	// The final empty cursor row is where the following progress line belongs.
	want := strings.TrimSuffix(readScreen(t, static.String(), 80).text(), "\n")
	if !strings.HasPrefix(screen.text(), want) || !strings.Contains(screen.text(), "√ READY-FINAL") || strings.Contains(screen.text(), "AFTER-FINISH") {
		t.Fatalf("progress overwrote intro or failed to finish:\n%s", screen.text())
	}
	if !screen.visible || screen.hidden != 2 || screen.restored != 2 {
		t.Fatalf("intro/progress cursor lifecycle: %+v", screen)
	}
}

func TestPlainProgressIsOrderedAndHasNoTerminalControls(t *testing.T) {
	var out bytes.Buffer
	p := StartProgress(&out, Options{}, "FIRST\r\nSTAGE")
	p.Set("SECOND\tSTAGE")
	p.FinishCancelled("CANCELLED")
	text := out.String()
	if strings.ContainsAny(text, "\x1b\r\t\b") {
		t.Fatalf("plain progress emitted controls: %q", text)
	}
	first, second, final := strings.Index(text, "FIRST  STAGE"), strings.Index(text, "SECOND STAGE"), strings.Index(text, "CANCELLED")
	if first < 0 || second <= first || final <= second || strings.ContainsAny(text, "√×+") {
		t.Fatalf("plain stage order or cancellation result wrong: %q", text)
	}
}

func TestTextBannerVersionNormalization(t *testing.T) {
	for _, version := range []string{"", "0.4.9", " v0.4.9 ", "0.4.9-rc.1"} {
		var out bytes.Buffer
		Render(&out, Options{Width: 120, Version: version})
		text := out.String()
		if !strings.Contains(text, Name) || !strings.Contains(text, Tagline) {
			t.Fatalf("text identity lost: %q", text)
		}
		if v := strings.TrimSpace(version); v != "" {
			if !strings.Contains(text, "v"+strings.TrimPrefix(v, "v")) || strings.Contains(text, "vv") {
				t.Fatalf("invalid version display: %q", text)
			}
		} else if strings.Contains(text, " v") {
			t.Fatalf("empty version produced a label: %q", text)
		}
	}
}

func assertFinishedScreen(t *testing.T, animated, static string, width int) {
	t.Helper()
	actual, expected := readScreen(t, animated, width), readScreen(t, static, width)
	if actual.text() != expected.text() || actual.y != expected.y || actual.x != expected.x {
		t.Fatalf("animation does not end at the static screen/cursor:\nactual (%d,%d):\n%s\nexpected (%d,%d):\n%s", actual.x, actual.y, actual.text(), expected.x, expected.y, expected.text())
	}
	if !actual.visible || actual.hidden != 1 || actual.restored != 1 {
		t.Fatalf("cursor was not restored exactly once: hide=%d show=%d visible=%v", actual.hidden, actual.restored, actual.visible)
	}
}

// terminalScreen interprets the cursor/erase operations the public renderer
// emits. Unlike removing ANSI codes, this detects overwritten rows, drifting
// frames and writes into the final column (which can wrap on real consoles).
// Rows are unbounded so static output may legitimately scroll a short terminal.
type terminalScreen struct {
	rows                   map[int]map[int]rune
	x, y, hidden, restored int
	visible                bool
}

func readScreen(t *testing.T, out string, width int) *terminalScreen {
	t.Helper()
	s := &terminalScreen{rows: map[int]map[int]rune{}, visible: true}
	runes := []rune(out)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\x1b':
			if i+1 >= len(runes) || runes[i+1] != '[' {
				t.Fatal("unexpected escape")
			}
			j := i + 2
			for j < len(runes) && (runes[j] < '@' || runes[j] > '~') {
				j++
			}
			if j >= len(runes) {
				t.Fatal("incomplete CSI")
			}
			args, op := string(runes[i+2:j]), runes[j]
			n := 1
			if args != "" && args[0] != '?' && op != 'm' {
				var err error
				n, err = strconv.Atoi(args)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch op {
			case 'm': // SGR changes presentation, not cells or position.
			case 'A':
				s.y -= n
			case 'B':
				s.y += n
			case 'C':
				s.x += n
			case 'K':
				if args != "2" {
					t.Fatalf("unsupported erase %q", args)
				}
				delete(s.rows, s.y)
			case 'l':
				if args != "?25" {
					t.Fatal("unexpected mode")
				}
				s.visible = false
				s.hidden++
			case 'h':
				if args != "?25" {
					t.Fatal("unexpected mode")
				}
				s.visible = true
				s.restored++
			default:
				t.Fatalf("unsupported CSI %q", string(runes[i:j+1]))
			}
			if s.y < 0 || s.x < 0 || s.x >= width {
				t.Fatalf("cursor outside banner area: (%d,%d), width %d", s.x, s.y, width)
			}
			i = j
		case '\r':
			s.x = 0
		case '\n':
			s.y++
			s.x = 0 // ONLCR is the terminal's normal output mode.
		default:
			w := displayWidthTest(string(r))
			if s.x+w >= width {
				t.Fatalf("write reaches reserved final column: x=%d glyph=%q width=%d", s.x, r, width)
			}
			if s.rows[s.y] == nil {
				s.rows[s.y] = map[int]rune{}
			}
			s.rows[s.y][s.x] = r
			for k := 1; k < w; k++ {
				s.rows[s.y][s.x+k] = 0
			}
			s.x += w
		}
	}
	return s
}

func (s *terminalScreen) text() string {
	var out strings.Builder
	for y := 0; y <= s.y; y++ {
		last := -1
		for x := range s.rows[y] {
			last = max(last, x)
		}
		var line strings.Builder
		for x := 0; x <= last; x++ {
			if r, ok := s.rows[y][x]; ok {
				if r != 0 {
					line.WriteRune(r)
				}
			} else {
				line.WriteByte(' ')
			}
		}
		fmt.Fprintln(&out, strings.TrimRight(line.String(), " "))
	}
	return out.String()
}
