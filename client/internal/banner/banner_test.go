package banner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/deploypilot/deploypilot/internal/console"
)

func TestRenderPlainIsReadableAndHasNoTerminalControls(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Blocks: true, Version: "0.4.9"})
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("plain output contains ANSI controls: %q", out)
	}
	for _, want := range []string{houseMark[0], productMark[0], Tagline, "v0.4.9"} {
		if !strings.Contains(out, strings.TrimSpace(want)) {
			t.Errorf("banner missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("plain line has trailing spaces: %q", line)
		}
	}
}

func TestBrandNameIsSpelledInFullOnEveryTier(t *testing.T) {
	if got := spell("BAILU"); !reflect.DeepEqual(got, houseMark) {
		t.Fatalf("house mark mismatch: %q", got)
	}
	for _, c := range []struct {
		width int
		want  []string
	}{
		{120, []string{houseMark[0], productMark[0]}},
		{80, []string{houseMark[0], productMark[0]}},
		{50, []string{houseLabel, productMark[0]}},
		{30, []string{Name}},
	} {
		var buf bytes.Buffer
		Render(&buf, Options{Color: true, Blocks: true, Width: c.width, Version: "0.4.9"})
		out := stripANSI(buf.String())
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("width %d: missing %q:\n%s", c.width, want, out)
			}
		}
	}
	var buf bytes.Buffer
	Render(&buf, Options{Color: true, Width: 120, Version: "0.4.9"})
	if out := stripANSI(buf.String()); !strings.Contains(out, "BaiLu DeployPilot") {
		t.Fatalf("text banner lacks the brand name: %q", out)
	}
}

func TestRenderWithoutNarrowBlocksUsesTextOnly(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Color: true, Width: 120, Version: "0.4.9"})
	out := stripANSI(buf.String())
	if strings.ContainsAny(out, "█▀▄") {
		t.Fatalf("block art drawn on a console with wide block glyphs: %q", out)
	}
	if !strings.Contains(out, Name) || !strings.Contains(out, Tagline) || !strings.Contains(out, "v0.4.9") {
		t.Fatalf("text banner incomplete: %q", out)
	}
}

func TestAnimatedIntroEndsOnTheStaticScreen(t *testing.T) {
	for _, width := range []int{100, 80, 50} {
		opts := Options{Color: true, Animate: true, Blocks: true, Width: width, Version: "0.4.9"}
		var animated bytes.Buffer
		Render(&animated, opts)
		out := animated.String()
		if strings.Count(out, "\x1b[?25l") != 1 || strings.Count(out, "\x1b[?25h") != 1 {
			t.Fatalf("width %d: cursor hide/restore mismatch", width)
		}
		if strings.Contains(out, "\x1b[0B") || strings.Contains(out, "\x1b[0A") || strings.Contains(out, "\x1b[0C") {
			t.Fatalf("width %d: zero-length cursor move (terminals treat it as 1)", width)
		}
		staticOpts := opts
		staticOpts.Animate = false
		var static bytes.Buffer
		Render(&static, staticOpts)
		got, want := emulate(out), emulate(static.String())
		if !reflect.DeepEqual(got.cells, want.cells) || got.row != want.row {
			t.Fatalf("width %d: animated end screen differs from static:\n%s\n---\n%s", width, got, want)
		}
		// Only changed cells are redrawn, so the whole intro stays small.
		if animated.Len() > 48*1024 {
			t.Errorf("width %d: intro is %d bytes", width, animated.Len())
		}
	}
}

func TestIntroStopJumpsToTheFinishedScreen(t *testing.T) {
	opts := Options{Color: true, Animate: true, Blocks: true, Width: 80, Version: "0.4.9", FrameDelay: time.Second}
	var buf lockedBuffer
	start := time.Now()
	intro := Play(context.Background(), &buf, opts)
	intro.Stop()
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("Stop took %v", took)
	}
	staticOpts := opts
	staticOpts.Animate = false
	var static bytes.Buffer
	Render(&static, staticOpts)
	if got, want := emulate(buf.String()), emulate(static.String()); !reflect.DeepEqual(got.cells, want.cells) {
		t.Fatalf("stopped intro is incomplete:\n%s", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var cancelled lockedBuffer
	in := Play(ctx, &cancelled, opts)
	cancel()
	select {
	case <-in.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelling the context did not end the intro")
	}
}

func TestIntroIsStaticWhenTheWindowIsTooShort(t *testing.T) {
	var buf bytes.Buffer
	Render(&buf, Options{Color: true, Animate: true, Blocks: true, Width: 80, Height: 6, Version: "0.4.9"})
	if strings.Contains(buf.String(), "\x1b[?25l") || regexp.MustCompile(`\x1b\[\d+A`).MatchString(buf.String()) {
		t.Fatalf("short window must not redraw in place: %q", buf.String())
	}
}

func TestProgressWaitsForTheIntro(t *testing.T) {
	var buf lockedBuffer
	opts := Options{Color: true, Animate: true, Blocks: true, Width: 80, Version: "0.4.9", FrameDelay: 5 * time.Millisecond}
	intro := Play(context.Background(), &buf, opts)
	p := intro.StartProgress("正在准备连接组件")
	p.FinishSuccess("安全隧道已就绪")
	out := buf.String()
	introEnd := strings.Index(out, "\x1b[?25h")
	// The shimmer splits the message with colour codes; search the text.
	after := stripANSI(out[max(introEnd, 0):])
	if introEnd < 0 || strings.Contains(stripANSI(out[:max(introEnd, 0)]), "正在") ||
		!strings.Contains(after, "正在准备连接组件") || !strings.Contains(after, "安全隧道已就绪") {
		t.Fatalf("progress drawn before the intro finished: %q", out)
	}
	if strings.Count(out, "\x1b[?25l") != strings.Count(out, "\x1b[?25h") {
		t.Fatalf("cursor left hidden: %q", out)
	}
}

func TestEveryLayoutFitsTheTerminal(t *testing.T) {
	for _, blocks := range []bool{true, false} {
		for _, width := range []int{120, 80, 64, 63, 50, 44, 43, 30, 20, 11, 1} {
			for _, color := range []bool{false, true} {
				var buf bytes.Buffer
				Render(&buf, Options{Color: color, Blocks: blocks, Width: width, Version: "0.4.9"})
				for _, line := range strings.Split(strings.ReplaceAll(stripANSI(buf.String()), "\r", ""), "\n") {
					if got := displayWidthTest(line); got >= max(width, 2) {
						t.Errorf("blocks=%v color=%v: line is %d columns (limit %d): %q", blocks, color, got, width, line)
					}
				}
			}
		}
	}
}

func TestLayoutTiers(t *testing.T) {
	if lineColumns != 61 || wideColumns != 63 || stackColumns != 43 {
		t.Fatalf("unexpected name widths: line %d wide %d stack %d", lineColumns, wideColumns, stackColumns)
	}
	if rows := layout(Options{Blocks: true, Width: 80}); len(rows) != 5 {
		t.Fatalf("80 columns should show the name on one line, got %d rows", len(rows))
	}
	if rows := layout(Options{Blocks: true, Width: wideColumns}); len(rows) != 6 {
		t.Fatalf("one-line name must leave the last column free, got %d rows", len(rows))
	}
	if rows := layout(Options{Blocks: true, Width: 50}); len(rows) != 6 || !strings.Contains(rowString(rows[0]), houseLabel) {
		t.Fatalf("50 columns should put the BAILU label over DEPLOYPILOT, got %d rows", len(rows))
	}
	if rows := layout(Options{Blocks: true, Width: stackColumns}); len(rows) != 1 {
		t.Fatalf("%d columns should show one text line, got %d rows", stackColumns, len(rows))
	}
}

func TestWideLayoutAlignsNameAndDetails(t *testing.T) {
	rows := layout(Options{Blocks: true, Width: 100, Version: "0.4.9"})
	productX := len(margin) + houseColumns + wordGap
	for i := range productMark {
		line := rowString(rows[i])
		if !strings.HasPrefix(line, margin+houseMark[i]) {
			t.Fatalf("row %d does not start with BAILU: %q", i, line)
		}
		index := strings.Index(line, productMark[i])
		if index < 0 || displayWidthTest(line[:index]) != productX {
			t.Fatalf("row %d: DEPLOYPILOT not at column %d: %q", i, productX, line)
		}
	}
	rule := rows[len(productMark)]
	if rowString(rule) != margin+strings.Repeat("─", lineColumns) {
		t.Fatalf("rule is not as wide as the name: %q", rowString(rule))
	}
	info := rows[len(rows)-1]
	if !strings.HasPrefix(rowString(info), margin+Tagline) || rowWidth(info) != wideColumns {
		t.Fatalf("version is not right-aligned to the name: %q width %d, want %d", rowString(info), rowWidth(info), wideColumns)
	}
}

func TestStackLayoutAlignsNameAndDetails(t *testing.T) {
	rows := layout(Options{Blocks: true, Width: 50, Version: "0.4.9"})
	want := []string{margin + houseLabel}
	for _, line := range productMark {
		want = append(want, margin+line)
	}
	want = append(want, margin+strings.Repeat("─", productColumns))
	for i, w := range want {
		if got := rowString(rows[i]); got != w {
			t.Fatalf("row %d = %q, want %q", i, got, w)
		}
	}
	if info := rows[len(rows)-1]; rowWidth(info) != stackColumns {
		t.Fatalf("version is not right-aligned to DEPLOYPILOT: width %d, want %d", rowWidth(info), stackColumns)
	}
}

func TestRuleIsDrawnByTheBeamAndSettles(t *testing.T) {
	sc := newScene(layout(Options{Blocks: true, Width: 80, Version: "0.4.9"}))
	ruleY := len(productMark)
	first, last := len(margin), len(margin)+lineColumns-1
	start, end := sc.times[ruleY][first][0], sc.times[ruleY][last][0]
	if start >= end {
		t.Fatalf("rule must be drawn left to right: %v then %v", start, end)
	}
	if got := sc.unit(ruleY, first, start); got.style != sgr(accentColor, false) {
		t.Fatalf("rule should flash in the accent colour first, got %q", got.style)
	}
	if got := sc.unit(ruleY, first, forever); got.style != sgr(ruleColor, false) {
		t.Fatalf("rule should settle to a hairline, got %q", got.style)
	}
	// The tagline waits for the beam rather than appearing with the rule.
	if tagline := sc.times[ruleY+1][len(margin)][0]; tagline < start {
		t.Fatalf("tagline at %v appears before the rule at %v", tagline, start)
	}
}

func TestDefaultOptionsRequireColorTerminal(t *testing.T) {
	plain := DefaultOptions(console.Caps{Terminal: true, Width: 80, NarrowBlocks: true}, "0.4.9")
	if plain.Color || plain.Animate || !plain.Blocks {
		t.Fatalf("terminal without ANSI support should be static plain: %+v", plain)
	}
	color := DefaultOptions(console.Caps{Terminal: true, Color: true, Width: 80, Height: 30}, "0.4.9")
	if !color.Color || !color.Animate || color.Blocks || color.Width != 80 || color.Height != 30 || color.FrameDelay <= 0 {
		t.Fatalf("color terminal missing intro effects: %+v", color)
	}
}

func TestStyleHelpersRespectColor(t *testing.T) {
	plain := Options{}
	if Heading(plain, "标题") != "  标题" || Hint(plain, "提示") != "  提示" || Link(plain, "https://x") != "  https://x" {
		t.Fatal("plain helpers must only indent")
	}
	color := Options{Color: true}
	if got := Link(color, "https://x"); !strings.Contains(got, "\x1b[4m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("link not underlined and reset: %q", got)
	}
}

func TestProgressFinishesAndRestoresCursorOnce(t *testing.T) {
	var buf lockedBuffer
	opts := Options{Color: true, Animate: true, Blocks: true, Width: 32, FrameDelay: time.Millisecond}
	p := StartProgress(&buf, opts, "正在建立安全隧道")
	p.Set("连接暂不可用，1 秒后重试")
	p.FinishSuccess("安全隧道已就绪")
	p.Finish("ignored duplicate")
	out := buf.String()
	if strings.Count(out, "\x1b[?25l") != 1 || strings.Count(out, "\x1b[?25h") != 1 {
		t.Fatalf("cursor hide/restore mismatch: %q", out)
	}
	if !strings.Contains(out, "安全隧道已就绪") || strings.Contains(out, "ignored duplicate") || !strings.Contains(out, "√") {
		t.Fatalf("progress finish did not run once: %q", out)
	}
}

func TestProgressAnimatesWhileWaiting(t *testing.T) {
	var buf lockedBuffer
	p := StartProgress(&buf, Options{Color: true, Animate: true, Blocks: true, Width: 80}, "正在建立安全隧道")
	time.Sleep(5 * progressTick)
	p.FinishSuccess("安全隧道已就绪")
	out := buf.String()
	if redraws := strings.Count(out, "\r\x1b[2K"); redraws < 3 {
		t.Fatalf("progress line redrawn %d times while waiting", redraws)
	}
	if !strings.Contains(out, "●") {
		t.Fatalf("pulse indicator missing: %q", out)
	}
}

func TestProgressFailureDoesNotPrintSuccessCheck(t *testing.T) {
	var buf lockedBuffer
	p := StartProgress(&buf, Options{Color: true, Animate: true, Blocks: true, Width: 40, FrameDelay: time.Millisecond}, "正在连接")
	p.FinishFailure("连接失败")
	out := buf.String()
	if strings.Contains(out, "√") || !strings.Contains(out, "255;107;107m×\x1b[0m ") || !strings.Contains(out, "连接失败") {
		t.Fatalf("failure result has wrong tone: %q", out)
	}

	var neutral lockedBuffer
	StartProgress(&neutral, Options{}, "检查状态").Finish("等待重试")
	if strings.Contains(neutral.String(), "√") || strings.Contains(neutral.String(), "+") {
		t.Fatalf("neutral result displayed success: %q", neutral.String())
	}
	if !strings.Contains(neutral.String(), "检查状态…") {
		t.Fatalf("plain progress lost its stage line: %q", neutral.String())
	}
}

func TestNarrowProgressDoesNotWrap(t *testing.T) {
	for _, width := range []int{16, 24, 30} {
		var buf lockedBuffer
		progress := StartProgress(&buf, Options{Color: true, Animate: true, Blocks: true, Width: width}, "正在建立安全隧道")
		progress.Set("网络暂不可用，等待重试")
		progress.FinishSuccess("安全隧道已就绪")
		for _, line := range strings.Split(stripANSI(buf.String()), "\n") {
			line = line[strings.LastIndex(line, "\r")+1:]
			if got := displayWidthTest(line); got >= width {
				t.Errorf("width %d: progress line is %d columns: %q", width, got, line)
			}
		}
	}
}

// TestWritePreview records the real startup output with timestamps for the
// preview renderer in .probe-tmp/terminal-motion-20261030/.
func TestWritePreview(t *testing.T) {
	path := os.Getenv("DP_BANNER_PREVIEW_OUT")
	if path == "" {
		t.Skip("preview output is opt-in")
	}
	width, _ := strconv.Atoi(os.Getenv("DP_BANNER_PREVIEW_COLS"))
	if width == 0 {
		width = 80
	}
	rec := &recorder{start: time.Now()}
	opts := Options{Color: true, Animate: true, Blocks: true, Width: width, Version: "0.4.9", FrameDelay: frameInterval}
	intro := Play(context.Background(), rec, opts)
	progress := intro.StartProgress("正在准备连接组件")
	time.Sleep(1800 * time.Millisecond)
	progress.Set("正在建立安全隧道")
	time.Sleep(2200 * time.Millisecond)
	progress.FinishSuccess("安全隧道已就绪")
	// Mirror main.printLink with a placeholder link of real length; no real
	// token is shown.
	link := "https://preview-only-sample-host.trycloudflare.com/#t=PREVIEW-PLACEHOLDER-TOKEN-NOT-REAL-000000"
	p := samplePairing(link)
	if os.Getenv("DP_BANNER_PREVIEW_EVENTS") != "" {
		_, _ = rec.Write([]byte(RenderPairing(opts, p)))
		time.Sleep(600 * time.Millisecond)
		_, _ = rec.Write([]byte("\n" + Status(opts, ToneWarning, "隧道暂不可用，1s 后重试", "cloudflared exited: exit status 1")))
		time.Sleep(600 * time.Millisecond)
		_, _ = rec.Write([]byte("\n" + Status(opts, ToneSuccess, "隧道已重新建立", "网址已更换，旧会话已失效；中断的任务不会自动重跑")))
		p.Heading = "扫码重新配对"
	}
	_, _ = rec.Write([]byte(RenderPairing(opts, p)))
	buf, err := json.Marshal(map[string]any{"cols": width, "chunks": rec.chunks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

type chunk struct {
	T    float64 `json:"t"` // milliseconds since start
	Data string  `json:"data"`
}

type recorder struct {
	mu     sync.Mutex
	start  time.Time
	chunks []chunk
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks = append(r.chunks, chunk{float64(time.Since(r.start).Microseconds()) / 1000, string(p)})
	return len(p), nil
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// screen is a minimal terminal: enough of VT100 to replay the banner.
type screen struct {
	cells    map[[2]int]styled
	row, col int
}

type styled struct {
	r     rune
	style string
}

var csiPattern = regexp.MustCompile(`^\x1b\[([0-9;?]*)([A-Za-z])`)

func emulate(s string) *screen {
	sc := &screen{cells: map[[2]int]styled{}}
	pen := ""
	for len(s) > 0 {
		if m := csiPattern.FindStringSubmatch(s); m != nil {
			s = s[len(m[0]):]
			n, err := strconv.Atoi(m[1])
			if err != nil || n == 0 {
				n = 1
			}
			switch m[2] {
			case "A":
				sc.row -= n
			case "B":
				sc.row += n
			case "C":
				sc.col += n
			case "K":
				for k := range sc.cells {
					if k[0] == sc.row && (m[1] == "2" || k[1] >= sc.col) {
						delete(sc.cells, k)
					}
				}
			case "m":
				pen = m[1]
				if pen == "0" {
					pen = ""
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		switch r {
		case '\r':
			sc.col = 0
		case '\n':
			sc.row++
			sc.col = 0
		case ' ':
			delete(sc.cells, [2]int{sc.row, sc.col})
			sc.col++
		default:
			sc.cells[[2]int{sc.row, sc.col}] = styled{r, pen}
			sc.col += displayWidthTest(string(r))
		}
	}
	return sc
}

func (sc *screen) String() string {
	maxRow, maxCol := 0, 0
	for k := range sc.cells {
		maxRow, maxCol = max(maxRow, k[0]), max(maxCol, k[1])
	}
	var b strings.Builder
	for y := 0; y <= maxRow; y++ {
		for x := 0; x <= maxCol; x++ {
			if c, ok := sc.cells[[2]int{y, x}]; ok {
				b.WriteRune(c.r)
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func rowString(r row) string {
	var b strings.Builder
	for _, c := range r {
		b.WriteRune(c.r)
	}
	return b.String()
}

func displayWidthTest(s string) int {
	width := 0
	for _, r := range s {
		if r >= 0x2e80 {
			width += 2
		} else {
			width++
		}
	}
	return width
}
