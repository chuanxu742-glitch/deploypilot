// Package banner renders the startup welcome screen and connection progress.
package banner

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/deploypilot/deploypilot/internal/console"
)

// Tagline is shown under the application name.
const Tagline = "远程部署助手"

// Name is the plain-text product name used where block art does not fit.
const Name = "BaiLu DeployPilot"

// margin is the left indent shared by the banner, progress and hints, so the
// whole startup screen lines up on one column.
const margin = "  "

// face is a 3×6-pixel capital alphabet in half blocks. It only holds the
// letters of the name; the pointed A echoes the triangle of the BaiLu mark.
var face = map[rune][3]string{
	'A': {"▄▀▄", "█▀█", "█ █"},
	'B': {"█▀▄", "█▀▄", "█▄▀"},
	'D': {"█▀▄", "█ █", "█▄▀"},
	'E': {"█▀▀", "█▀▀", "█▄▄"},
	'I': {"█", "█", "█"},
	'L': {"█  ", "█  ", "█▄▄"},
	'O': {"█▀█", "█ █", "█▄█"},
	'P': {"█▀█", "█▀▀", "█  "},
	'T': {"▀█▀", " █ ", " █ "},
	'U': {"█ █", "█ █", "█▄█"},
	'Y': {"█ █", "▀█▀", " █ "},
}

// wordGap is the space between BAILU and DEPLOYPILOT on one line.
const wordGap = 3

var (
	houseMark      = spell("BAILU")
	productMark    = spell("DEPLOYPILOT")
	houseColumns   = artWidth(houseMark)
	productColumns = artWidth(productMark)
	// lineColumns is BAILU DEPLOYPILOT side by side (61 columns).
	lineColumns = houseColumns + wordGap + productColumns
	// wideColumns and stackColumns include the left margin.
	wideColumns  = len(margin) + lineColumns
	stackColumns = len(margin) + max(houseColumns, productColumns)
)

// spell sets word in face with one blank column between letters.
func spell(word string) []string {
	var lines [3]strings.Builder
	for n, r := range []rune(word) {
		glyph, ok := face[r]
		if !ok {
			panic("banner: no glyph for " + string(r))
		}
		for k := range lines {
			if n > 0 {
				lines[k].WriteByte(' ')
			}
			lines[k].WriteString(glyph[k])
		}
	}
	out := make([]string, len(lines))
	for k := range lines {
		out[k] = strings.TrimRight(lines[k].String(), " ")
	}
	return out
}

func artWidth(lines []string) int {
	width := 0
	for _, line := range lines {
		width = max(width, displayWidth(line))
	}
	return width
}

type rgb [3]int

// defaultFG stands for the terminal's default foreground colour (SGR 39).
var defaultFG = rgb{-1, -1, -1}

// Brand palette. The BaiLu blue is #0081FC; DEPLOYPILOT runs from a lighter
// cyan on the left to a deeper blue on the right. BAILU is a muted blue in the
// same family so it reads as the maker, with DEPLOYPILOT as the product.
var (
	gradientStart = rgb{90, 214, 255}
	brandBlue     = rgb{0, 129, 252}
	gradientEnd   = rgb{40, 86, 236}
	houseColor    = rgb{140, 168, 208}
	ruleColor     = rgb{64, 76, 98} // a hairline on dark and light themes
	// textColor is the terminal's own foreground, so primary text stays
	// readable in light themes. Fades approach it through mutedColor.
	textColor    = defaultFG
	mutedColor   = rgb{122, 136, 160}
	accentColor  = rgb{64, 196, 255}
	successColor = rgb{52, 211, 153}
	failureColor = rgb{255, 107, 107}
)

// Motion colours.
var (
	beamColor  = rgb{232, 250, 255} // leading edge of the scan
	unlitColor = rgb{30, 38, 54}    // text before it fades in
	pulseDim   = rgb{22, 78, 160}   // low point of the progress pulse
)

// Intro timing. One diagonal scan reveals the name; pixels behind the beam
// cool from near-white to their brand colour and text fades up from dark.
const (
	frameInterval = 33 * time.Millisecond
	scanDuration  = 680 * time.Millisecond
	coolDuration  = 200 * time.Millisecond
	fadeDuration  = 260 * time.Millisecond
	// The rule under the name is drawn by the beam in the accent colour and
	// then dims to a hairline.
	ruleCool = 420 * time.Millisecond
	// The tagline starts once the beam has passed most of the name.
	detailDelay   = 520 * time.Millisecond
	detailStagger = 4 * time.Millisecond // per column, left to right
	// Colours move in a few discrete steps so a cell is redrawn only a
	// handful of times, which keeps the intro small over slow SSH links.
	heatSteps = 5
	rampSteps = 24
)

// Progress timing.
const (
	progressTick  = 66 * time.Millisecond
	pulsePeriod   = 1200 * time.Millisecond
	shimmerPeriod = 2 * time.Second
	shimmerBand   = 3.0 // half-width of the highlight, in characters
	shimmerRest   = 6.0 // characters of travel off-screen between sweeps
)

// forever is a time after every intro transition has settled.
const forever = time.Duration(math.MaxInt64)

// Options controls the banner and animated progress rendering.
type Options struct {
	// Color enables 24-bit ANSI color styling.
	Color bool
	// Animate enables the intro scan and the live progress line.
	Animate bool
	// Blocks reports that ▀ ▄ █ occupy one cell each. Without it the block
	// letters would be stretched, so only text is drawn.
	Blocks bool
	// Width is the terminal width; zero means unknown.
	Width int
	// Height is the terminal height; zero means unknown. The intro redraws in
	// place, so it only plays when the whole banner fits on screen.
	Height int
	// FrameDelay is the time between intro frames. Zero renders every frame
	// without sleeping, which is useful for deterministic previews and tests.
	FrameDelay time.Duration
	// Version is printed alongside the application name.
	Version string
}

// DefaultOptions enables effects only on a color-capable terminal.
func DefaultOptions(caps console.Caps, version string) Options {
	interactive := caps.Terminal && caps.Color
	return Options{
		Color:      caps.Color,
		Animate:    interactive,
		Blocks:     caps.NarrowBlocks,
		Width:      caps.Width,
		Height:     caps.Height,
		FrameDelay: frameInterval,
		Version:    version,
	}
}

// cell is one character of the banner with the colour it is drawn in.
type cell struct {
	r     rune
	color rgb
	bold  bool
	art   bool // half-block letter; revealed pixel by pixel
	swept bool // text revealed by the beam with the letters
	rule  bool // hairline drawn by the beam; flashes, then dims
}

type row []cell

// Intro is the welcome screen being drawn. The animation runs in the
// background so startup work can continue; anything printed after it must
// wait for Done, which the progress line from Intro.StartProgress does.
type Intro struct {
	w    io.Writer
	opts Options
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Render draws the welcome screen and returns once it is complete.
func Render(w io.Writer, opts Options) {
	Play(context.Background(), w, opts).Wait()
}

// Play starts drawing the welcome screen. A colour terminal gets a diagonal
// scan of under a second; other writers get the finished screen at once.
// Cancelling ctx jumps to the finished screen.
func Play(ctx context.Context, w io.Writer, opts Options) *Intro {
	if ctx == nil {
		ctx = context.Background()
	}
	in := &Intro{w: w, opts: opts, stop: make(chan struct{}), done: make(chan struct{})}
	rows := layout(opts)
	switch {
	case !opts.Color:
		_, _ = io.WriteString(w, plainText(rows))
	// The redraw needs the rows plus a blank line above and the cursor line below.
	case !opts.Animate || (opts.Height > 0 && opts.Height <= len(rows)+1):
		_, _ = io.WriteString(w, staticText(rows))
	default:
		go in.animate(ctx, newScene(rows))
		return in
	}
	close(in.done)
	return in
}

// Wait blocks until the welcome screen is complete.
func (in *Intro) Wait() { <-in.done }

// Done is closed once the welcome screen is complete.
func (in *Intro) Done() <-chan struct{} { return in.done }

// Stop jumps to the finished screen and waits for it to be drawn.
func (in *Intro) Stop() {
	in.once.Do(func() { close(in.stop) })
	<-in.done
}

// StartProgress creates the status line below the welcome screen. It returns
// immediately; the line appears when the intro has finished.
func (in *Intro) StartProgress(message string) *Progress {
	return startProgress(in.w, in.opts, message, in.done)
}

func (in *Intro) animate(ctx context.Context, sc *scene) {
	defer close(in.done)
	p := &painter{w: in.w, sc: sc}
	p.begin()
	defer p.finish()
	interval := in.opts.FrameDelay
	realtime := interval > 0
	if !realtime {
		interval = frameInterval
	}
	// stopped waits for wait (or not at all when it is nil) and reports
	// whether the intro was cut short meanwhile.
	stopped := func(wait <-chan time.Time) bool {
		if wait == nil {
			select {
			case <-ctx.Done():
				return true
			case <-in.stop:
				return true
			default:
				return false
			}
		}
		select {
		case <-wait:
			return false
		case <-ctx.Done():
			return true
		case <-in.stop:
			return true
		}
	}
	// Every exit path draws the final frame.
	defer p.frame(forever)
	start := time.Now()
	for k := 0; ; k++ {
		t := time.Duration(k) * interval
		if realtime {
			if wait := time.Until(start.Add(t)); wait > 0 {
				timer := time.NewTimer(wait)
				cut := stopped(timer.C)
				timer.Stop()
				if cut {
					return
				}
			}
			// A slow terminal drops frames instead of stretching the intro.
			t = time.Since(start)
			k = int(t / interval)
		} else if stopped(nil) {
			return
		}
		if t >= sc.end {
			return
		}
		p.frame(t)
	}
}

// scene holds when each part of the banner appears. The scan runs along
// x + y in half-block pixels: a pixel is about square, so the beam is at 45°.
type scene struct {
	rows  []row
	cols  [][]int              // display column of every cell
	times [][][2]time.Duration // letters: top and bottom half; text: [0]
	end   time.Duration
}

func newScene(rows []row) *scene {
	sc := &scene{rows: rows, cols: make([][]int, len(rows)), times: make([][][2]time.Duration, len(rows))}
	type mark struct {
		y, i, half int
		key        float64
	}
	var marks []mark
	for y, r := range rows {
		sc.cols[y] = make([]int, len(r))
		sc.times[y] = make([][2]time.Duration, len(r))
		col := 0
		for i, c := range r {
			sc.cols[y][i] = col
			switch {
			case c.r == ' ':
			case c.art:
				if inkTop(c.r) {
					marks = append(marks, mark{y, i, 0, float64(col + 2*y)})
				}
				if inkBottom(c.r) {
					marks = append(marks, mark{y, i, 1, float64(col + 2*y + 1)})
				}
			default:
				marks = append(marks, mark{y, i, 0, float64(col + 2*y + 1)})
			}
			col += displayWidth(string(c.r))
		}
	}
	// When there are block letters the beam reveals them, the BAILU label and
	// the rule; the tagline and version fade in afterwards, left to right, so
	// the name is read first. A text-only banner is scanned as a whole.
	hasArt := false
	for _, m := range marks {
		hasArt = hasArt || sc.rows[m.y][m.i].art
	}
	beam := func(m mark) bool {
		c := sc.rows[m.y][m.i]
		return !hasArt || c.art || c.swept || c.rule
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, m := range marks {
		if beam(m) {
			lo, hi = math.Min(lo, m.key), math.Max(hi, m.key)
		}
	}
	for _, m := range marks {
		if !beam(m) {
			sc.times[m.y][m.i][0] = detailDelay + time.Duration(sc.cols[m.y][m.i])*detailStagger
			continue
		}
		v := 0.0
		if hi > lo {
			v = (m.key - lo) / (hi - lo)
		}
		// Inverse of ease-in-out-sine: the beam starts and lands gently.
		sc.times[m.y][m.i][m.half] = time.Duration(float64(scanDuration) * math.Acos(1-2*v) / math.Pi)
	}
	sc.end = 0
	for y := range sc.times {
		for _, at := range sc.times[y] {
			sc.end = max(sc.end, at[0]+max(fadeDuration, ruleCool), at[1]+coolDuration)
		}
	}
	return sc
}

func inkTop(r rune) bool    { return r == '█' || r == '▀' }
func inkBottom(r rune) bool { return r == '█' || r == '▄' }

// unit is what one cell shows in a frame: its glyph (or blanks of the cell's
// width) and the SGR style, empty when blank.
type unit struct {
	text  string
	style string
}

func (sc *scene) unit(y, i int, t time.Duration) unit {
	c := sc.rows[y][i]
	blank := unit{text: strings.Repeat(" ", displayWidth(string(c.r)))}
	if c.r == ' ' {
		return blank
	}
	at := sc.times[y][i]
	if !c.art {
		if t < at[0] {
			return blank
		}
		if c.rule {
			return unit{string(c.r), sgr(mix(accentColor, c.color, settle(t-at[0], ruleCool)), false)}
		}
		return unit{string(c.r), sgr(mix(unlitColor, c.color, settle(t-at[0], fadeDuration)), c.bold)}
	}
	top := inkTop(c.r) && t >= at[0]
	bottom := inkBottom(c.r) && t >= at[1]
	switch {
	case top && bottom:
		return unit{"█", sgr(mix(heat(c.color, t-at[0]), heat(c.color, t-at[1]), 0.5), false)}
	case top:
		return unit{"▀", sgr(heat(c.color, t-at[0]), false)}
	case bottom:
		return unit{"▄", sgr(heat(c.color, t-at[1]), false)}
	}
	return blank
}

func heat(final rgb, age time.Duration) rgb {
	return mix(beamColor, final, settle(age, coolDuration))
}

// settle is a smoothstep from 0 to 1 over d, in heatSteps discrete steps.
func settle(age, d time.Duration) float64 {
	if age >= d {
		return 1
	}
	u := float64(age) / float64(d)
	u = u * u * (3 - 2*u)
	return math.Round(u*heatSteps) / heatSteps
}

// painter redraws only the cells that changed since the previous frame.
// Positions are relative to the banner's top-left corner.
type painter struct {
	w        io.Writer
	sc       *scene
	shown    [][]unit
	row, col int
}

// begin hides the cursor and reserves blank lines for the banner, scrolling
// the terminal if needed, then returns to the first of them.
func (p *painter) begin() {
	var b strings.Builder
	b.WriteString("\x1b[?25l\n")
	p.shown = make([][]unit, len(p.sc.rows))
	for y, r := range p.sc.rows {
		b.WriteString("\r\x1b[2K\n")
		p.shown[y] = make([]unit, len(r))
		for i, c := range r {
			p.shown[y][i] = unit{text: strings.Repeat(" ", displayWidth(string(c.r)))}
		}
	}
	fmt.Fprintf(&b, "\x1b[%dA\r", len(p.sc.rows))
	_, _ = io.WriteString(p.w, b.String())
}

func (p *painter) frame(t time.Duration) {
	var b strings.Builder
	for y, r := range p.sc.rows {
		next := make([]unit, len(r))
		first, last := -1, -1
		for i := range r {
			next[i] = p.sc.unit(y, i, t)
			if next[i] != p.shown[y][i] {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		if first < 0 {
			continue
		}
		p.moveTo(&b, y, p.sc.cols[y][first])
		writeUnits(&b, next[first:last+1])
		p.col = p.sc.cols[y][last] + displayWidth(string(r[last].r))
		p.shown[y] = next
	}
	if b.Len() > 0 {
		_, _ = io.WriteString(p.w, b.String())
	}
}

func (p *painter) moveTo(b *strings.Builder, y, col int) {
	if y > p.row {
		fmt.Fprintf(b, "\x1b[%dB", y-p.row)
	} else if y < p.row {
		fmt.Fprintf(b, "\x1b[%dA", p.row-y)
	}
	if col != p.col {
		b.WriteByte('\r')
		if col > 0 {
			fmt.Fprintf(b, "\x1b[%dC", col)
		}
	}
	p.row, p.col = y, col
}

// finish moves below the banner, leaves one blank line and restores the cursor.
func (p *painter) finish() {
	var b strings.Builder
	if down := len(p.sc.rows) - p.row; down > 0 {
		fmt.Fprintf(&b, "\x1b[%dB", down)
	}
	b.WriteString("\r\n\x1b[?25h")
	_, _ = io.WriteString(p.w, b.String())
}

func writeUnits(b *strings.Builder, units []unit) {
	pen := ""
	for _, u := range units {
		if u.style != pen {
			if u.style == "" {
				b.WriteString("\x1b[0m")
			} else {
				b.WriteString(u.style)
			}
			pen = u.style
		}
		b.WriteString(u.text)
	}
	if pen != "" {
		b.WriteString("\x1b[0m")
	}
}

// staticText is the finished screen in colour, for terminals without motion.
func staticText(rows []row) string {
	sc := newScene(rows)
	var b strings.Builder
	b.WriteByte('\n')
	for y, r := range rows {
		units := make([]unit, len(r))
		for i := range r {
			units[i] = sc.unit(y, i, forever)
		}
		b.WriteByte('\r')
		writeUnits(&b, units)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.String()
}

func plainText(rows []row) string {
	var out strings.Builder
	out.WriteByte('\n')
	for _, r := range rows {
		for _, c := range r {
			out.WriteRune(c.r)
		}
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	return trimLines(out.String())
}

// layout picks the richest arrangement that fits the terminal width:
// BAILU DEPLOYPILOT on one line, the two words stacked, or one text line.
func layout(opts Options) []row {
	version := ""
	if v := strings.TrimSpace(opts.Version); v != "" {
		version = "v" + strings.TrimPrefix(v, "v")
	}
	// Keep the last column free: writing into it makes some consoles wrap,
	// which would break the in-place intro redraw.
	fits := func(cols int) bool { return opts.Width == 0 || opts.Width > cols }
	switch {
	case opts.Blocks && fits(wideColumns):
		return wideLayout(version)
	case opts.Blocks && fits(stackColumns):
		return stackLayout(version)
	default:
		return []row{textRow(version, opts.Width)}
	}
}

// houseLabel is BAILU as letter-spaced text, for the stacked layout.
const houseLabel = "B A I L U"

// wideLayout is the name in block letters on one line, a hairline under it
// and the details:
//
//	BAILU DEPLOYPILOT
//	─────────────────────────
//	远程部署助手        v0.4.9
func wideLayout(version string) []row {
	productX := len(margin) + houseColumns + wordGap
	rows := make([]row, 0, len(productMark)+2)
	for i, line := range productMark {
		r := textCells(margin, textColor, false)
		if i < len(houseMark) {
			r = append(r, tintCells(houseMark[i], houseColor)...)
		}
		rows = append(rows, place(r, productX, gradientCells(line, i)))
	}
	return append(rows, ruleRow(lineColumns), infoRow(version, lineColumns))
}

// stackLayout is for windows under 64 columns: BAILU becomes a small
// letter-spaced label above the DEPLOYPILOT letters.
func stackLayout(version string) []row {
	label := textCells(margin, textColor, false)
	for _, c := range textCells(houseLabel, houseColor, true) {
		c.swept = c.r != ' '
		label = append(label, c)
	}
	rows := []row{label}
	for i, line := range productMark {
		rows = append(rows, append(textCells(margin, textColor, false), gradientCells(line, i)...))
	}
	return append(rows, ruleRow(productColumns), infoRow(version, productColumns))
}

// ruleRow is a hairline as wide as the name. It is only used with Blocks,
// where box-drawing characters are known to be one cell wide.
func ruleRow(width int) row {
	r := textCells(margin, textColor, false)
	for range width {
		r = append(r, cell{r: '─', color: ruleColor, rule: true})
	}
	return r
}

// gradientCells colours one row of DEPLOYPILOT, mostly left to right with a
// slight darkening towards the bottom.
func gradientCells(line string, y int) []cell {
	cells := make([]cell, 0, len(line))
	x := 0
	for _, r := range line {
		t := 0.82*float64(x)/float64(productColumns-1) + 0.18*float64(y)/float64(len(productMark)-1)
		cells = append(cells, cell{r: r, color: ramp(math.Round(t*rampSteps) / rampSteps), art: true})
		x++
	}
	return cells
}

func tintCells(line string, color rgb) []cell {
	cells := make([]cell, 0, len(line))
	for _, r := range line {
		cells = append(cells, cell{r: r, color: color, art: true})
	}
	return cells
}

// infoRow is "远程部署助手 … v0.4.9" with the version right-aligned to width.
func infoRow(version string, width int) row {
	r := textCells(margin, textColor, false)
	r = append(r, textCells(Tagline, textColor, true)...)
	if version != "" {
		pad := max(2, width-displayWidth(Tagline)-displayWidth(version))
		r = append(r, textCells(strings.Repeat(" ", pad), textColor, false)...)
		r = append(r, textCells(version, mutedColor, false)...)
	}
	return r
}

// textRow is the single-line fallback: "BaiLu DeployPilot  远程部署助手  v0.4.9".
func textRow(version string, width int) row {
	type part struct {
		text  string
		color rgb
		bold  bool
	}
	parts := []part{{"BaiLu", houseColor, true}, {" DeployPilot", accentColor, true}, {"  " + Tagline, textColor, false}}
	if version != "" {
		parts = append(parts, part{"  " + version, mutedColor, false})
	}
	r := textCells(margin, textColor, false)
	for _, p := range parts {
		r = append(r, textCells(p.text, p.color, p.bold)...)
	}
	if width > 0 && rowWidth(r) >= width {
		return textCells(fitCells(strings.TrimSpace(margin+Name+"  "+Tagline+"  "+version), width-1), accentColor, true)
	}
	return r
}

func textCells(s string, color rgb, bold bool) row {
	cells := make(row, 0, len(s))
	for _, r := range s {
		cells = append(cells, cell{r: r, color: color, bold: bold})
	}
	return cells
}

// place writes src into dst starting at display column x, padding with spaces.
func place(dst row, x int, src []cell) row {
	for rowWidth(dst) < x {
		dst = append(dst, cell{r: ' '})
	}
	return append(dst, src...)
}

func rowWidth(r row) int {
	width := 0
	for _, c := range r {
		width += displayWidth(string(c.r))
	}
	return width
}

func ramp(t float64) rgb {
	t = min(1, max(0, t))
	if t < 0.5 {
		return mix(gradientStart, brandBlue, t*2)
	}
	return mix(brandBlue, gradientEnd, (t-0.5)*2)
}

func mix(a, b rgb, t float64) rgb {
	// The default foreground has no known value: arrive at it only at the end
	// of a transition and use mutedColor in between.
	if b == defaultFG {
		if t >= 1 {
			return defaultFG
		}
		b = mutedColor
	}
	if a == defaultFG {
		if t <= 0 {
			return defaultFG
		}
		a = mutedColor
	}
	var out rgb
	for i := range out {
		out[i] = a[i] + int(math.Round(float64(b[i]-a[i])*t))
	}
	return out
}

func sgr(c rgb, bold bool) string {
	if c == defaultFG {
		if bold {
			return "\x1b[1;39m"
		}
		return "\x1b[39m"
	}
	if bold {
		return fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm", c[0], c[1], c[2])
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
}

func style(opts Options, c rgb, bold, underline bool, s string) string {
	if !opts.Color || s == "" {
		return s
	}
	prefix := sgr(c, bold)
	if underline {
		prefix += "\x1b[4m"
	}
	return prefix + s + "\x1b[0m"
}

// Heading formats a section title, such as the line above the QR code.
func Heading(opts Options, s string) string {
	return margin + style(opts, textColor, true, false, s)
}

// Hint formats secondary guidance text.
func Hint(opts Options, s string) string {
	return margin + style(opts, mutedColor, false, false, s)
}

// Link formats a URL so it stands out and can be copied as one token.
func Link(opts Options, s string) string {
	return margin + style(opts, accentColor, false, true, s)
}

// Progress owns one live status line: a pulsing dot, the current stage with a
// travelling highlight, and the elapsed time. The motion only runs while a
// real stage is in progress. Finish stops and joins its renderer before
// printing a final line, restoring the cursor exactly once.
type Progress struct {
	w       io.Writer
	opts    Options
	started time.Time
	ready   <-chan struct{} // closed when the line may be drawn
	active  bool
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once

	mu       sync.Mutex
	message  string
	shown    bool // the line has appeared
	hidden   bool // the cursor is hidden
	finished bool
}

type progressTone uint8

const (
	progressNeutral progressTone = iota
	progressSuccess
	progressFailure
	progressCancelled
)

// StartProgress starts an elapsed-time indicator without inventing a percent.
func StartProgress(w io.Writer, opts Options, message string) *Progress {
	return startProgress(w, opts, message, nil)
}

func startProgress(w io.Writer, opts Options, message string, ready <-chan struct{}) *Progress {
	if ready == nil {
		closed := make(chan struct{})
		close(closed)
		ready = closed
	}
	p := &Progress{
		w: w, opts: opts, started: time.Now(), ready: ready,
		message: cleanLine(message),
		active:  opts.Animate && opts.Color && (opts.Width == 0 || opts.Width >= 24),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	// Show the first stage now when nothing is drawing above it, so plain
	// output lists every stage in order.
	select {
	case <-ready:
		p.shown = true
		p.showLocked()
	default:
	}
	go p.run()
	return p
}

func (p *Progress) run() {
	defer close(p.done)
	<-p.ready
	p.mu.Lock()
	if !p.shown {
		p.shown = true
		p.showLocked()
	}
	p.mu.Unlock()
	if !p.active {
		return
	}
	ticker := time.NewTicker(progressTick)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			p.drawLocked()
			p.mu.Unlock()
		}
	}
}

// Set changes the current stage description.
func (p *Progress) Set(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.message = cleanLine(message)
	if p.shown && !p.finished {
		p.showLocked()
	}
}

func (p *Progress) showLocked() {
	if p.active {
		p.drawLocked()
		return
	}
	_, _ = fmt.Fprintln(p.w, p.fit(margin+p.message+"…"))
}

// Finish stops and joins animation, then prints an optional result line.
func (p *Progress) Finish(message string) {
	p.finish(message, progressNeutral)
}

// FinishSuccess marks a real successful stage, such as tunnel Ready.
func (p *Progress) FinishSuccess(message string) {
	p.finish(message, progressSuccess)
}

// FinishFailure marks a failed preparation or connection stage.
func (p *Progress) FinishFailure(message string) {
	p.finish(message, progressFailure)
}

// FinishCancelled ends the progress line neutrally after cancellation.
func (p *Progress) FinishCancelled(message string) {
	p.finish(message, progressCancelled)
}

func (p *Progress) finish(message string, tone progressTone) {
	p.once.Do(func() {
		close(p.stop)
		<-p.done // also waits for the intro, so lines never interleave
		p.mu.Lock()
		defer p.mu.Unlock()
		p.finished = true
		if p.active {
			_, _ = io.WriteString(p.w, "\r\x1b[2K")
		}
		if message != "" {
			_, _ = fmt.Fprintln(p.w, p.resultLine(message, tone))
		}
		if p.hidden {
			_, _ = io.WriteString(p.w, "\x1b[?25h")
			p.hidden = false
		}
	})
}

// resultLine is "  √ 安全隧道已就绪  00:04" with the marker coloured by tone.
// √ and × are in common console fonts such as Consolas, unlike ✓ and ✗.
func (p *Progress) resultLine(message string, tone progressTone) string {
	message = cleanLine(message)
	marker, color := "", mutedColor
	switch tone {
	case progressSuccess:
		marker, color = successMarker(p.opts), successColor
	case progressFailure:
		marker, color = failureMarker(p.opts), failureColor
	case progressCancelled:
		marker = "-"
	}
	took := elapsed(time.Since(p.started))
	room := p.messageRoom(took)
	if marker == "" {
		room += 2
	}
	message = fitCells(message, room)
	if !p.opts.Color {
		line := margin
		if marker != "" {
			line += marker + " "
		}
		return line + message
	}
	line := margin
	if marker != "" {
		line += sgr(color, true) + marker + "\x1b[0m "
	}
	messageColor := textColor
	if tone == progressCancelled || tone == progressNeutral {
		messageColor = mutedColor
	}
	return line + style(p.opts, messageColor, false, false, message) + "  " + style(p.opts, mutedColor, false, false, took)
}

// unknownWidthRoom is the message width when the console width is unknown.
const unknownWidthRoom = 72

// messageRoom is how many columns are left for the message on a line laid
// out as margin, a two-cell marker, the message, two spaces and took.
func (p *Progress) messageRoom(took string) int {
	if p.opts.Width <= 0 {
		return unknownWidthRoom
	}
	return max(0, p.opts.Width-1-(len(margin)+2+2+len(took)))
}

func (p *Progress) fit(s string) string {
	if p.opts.Width > 0 {
		return fitCells(s, p.opts.Width-1)
	}
	return s
}

var asciiSpinner = []string{"|", "/", "-", "\\"}

func (p *Progress) drawLocked() {
	since := time.Since(p.started)
	took := elapsed(since)
	message := fitCells(p.message, p.messageRoom(took))
	var b strings.Builder
	if !p.hidden {
		b.WriteString("\x1b[?25l")
		p.hidden = true
	}
	b.WriteString("\r\x1b[2K" + margin)
	b.WriteString(p.indicator(since))
	b.WriteByte(' ')
	b.WriteString(shimmer(message, since))
	b.WriteString("  " + style(p.opts, mutedColor, false, false, took))
	_, _ = io.WriteString(p.w, b.String())
}

// indicator is a dot breathing between dim and bright blue. Consoles that
// draw symbols two cells wide get an ASCII spinner instead.
func (p *Progress) indicator(since time.Duration) string {
	if !p.opts.Blocks {
		return sgr(accentColor, true) + asciiSpinner[int(since/(100*time.Millisecond))%len(asciiSpinner)] + "\x1b[0m"
	}
	k := (1 - math.Cos(2*math.Pi*float64(since%pulsePeriod)/float64(pulsePeriod))) / 2
	return sgr(mix(pulseDim, accentColor, math.Round(k*8)/8), false) + "●\x1b[0m"
}

// shimmer draws s in the text colour with a soft blue highlight travelling
// left to right; blue reads on both dark and light themes.
func shimmer(s string, since time.Duration) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	span := float64(len(runes)) + 2*shimmerBand + shimmerRest
	center := -shimmerBand + span*float64(since%shimmerPeriod)/float64(shimmerPeriod)
	var b strings.Builder
	last := ""
	for i, r := range runes {
		k := 0.0
		if d := math.Abs(float64(i) - center); d < shimmerBand {
			k = 1 - d/shimmerBand
			k = k * k * (3 - 2*k)
		}
		st := sgr(textColor, false)
		if k = math.Round(k*4) / 4; k > 0 {
			st = sgr(mix(mutedColor, accentColor, 0.5+0.5*k), false)
		}
		if st != last {
			b.WriteString(st)
			last = st
		}
		b.WriteRune(r)
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func elapsed(d time.Duration) string {
	seconds := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}

func fitCells(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displayWidth(s) <= maxWidth {
		return s
	}
	var out strings.Builder
	width := 0
	limit := maxWidth - 1 // reserve one cell for the truncation mark
	for _, r := range s {
		cellWidth := displayWidth(string(r))
		if width+cellWidth > limit {
			break
		}
		out.WriteRune(r)
		width += cellWidth
	}
	return strings.TrimRight(out.String(), " ") + "…"
}

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func displayWidth(s string) int {
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
