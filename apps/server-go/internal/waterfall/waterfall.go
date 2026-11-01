// Package waterfall renders activity-log events as a "verb waterfall": a
// spectrogram-like grid whose columns are verb groups and whose rows are 8 s
// time slices, newest on top. Output is a slice of terminal lines, optionally
// colored with 24-bit SGR escapes.
//
// Rendering is a pure function of its inputs: no I/O, no clock reads.
package waterfall

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// BucketMs is the duration of one time slice, in milliseconds.
const BucketMs = 8_000

// Palette maps theme color names (text, subtext0, overlay0, base, red, ...)
// to "#rrggbb" strings. A nil or empty palette selects plain output.
type Palette map[string]string

const (
	glyphBell  = "\U000F0D59" // waiting bell
	glyphError = ""          // error cross
	glyphEmpty = "·"

	// Widest a cell grows; leftover width beyond this stays blank.
	maxCellWidth = 8
	// Narrowest cell at which the right-hand gutter is still shown.
	minCellWidthWithGutter = 3
	// gutterWidth is one separating space plus a label of up to three cells.
	gutterWidth = 4

	// heatCap is the event count in one column and slice that fills the
	// darkest shade. Active agents emit roughly 0 to 6 tool events per verb in
	// 8 s, so a cap of 8 spreads that range across all four shades while a
	// burst beyond it stretches the scale to the visible maximum.
	heatCap = 8
)

var heatGlyphs = [...]string{"░", "▒", "▓", "█"}

// column describes one verb group on screen.
type column struct {
	label string
	hue   string // palette key
}

// columns run left to right roughly in the order a turn unfolds: think, look
// at the code, change it, run it, reach outside the repo, hand work to other
// agents.
var columns = [...]column{
	{"think", "lavender"},
	{"read", "blue"},
	{"edit", "peach"},
	{"run", "green"},
	{"ext", "teal"},
	{"agents", "mauve"},
}

// verbColumn maps an activity verb to its index in columns. Verbs absent from
// the table (error, misc, untagged) have no column.
var verbColumn = map[string]int{
	"thinking": 0,
	"read":     1,
	"list":     1,
	"search":   1,
	"edit":     2,
	"run":      3,
	"web":      4,
	"tool":     4,
	"task":     5,
	"skill":    5,
}

// fallbackColors fill in palette keys a theme does not define.
var fallbackColors = map[string]string{
	"red":    "#f38ba8",
	"yellow": "#f9e2af",
	"base":   "#1e1e2e",
}

// BucketRow returns the row (0 = newest) holding timestamp ts in a grid of
// rows time slices, or -1 when ts is older than the grid. Slices are aligned
// to multiples of BucketMs; row 0 spans from the current slice boundary
// onward, so future timestamps land there. For a now on a slice boundary this
// agrees with the sidebar seismograph's bucketIndex (mirrored: row =
// rows-1-bucketIndex).
func BucketRow(ts, now int64, rows int) int {
	if rows <= 0 {
		return -1
	}
	anchor := now - floorMod(now, BucketMs) + BucketMs - 1 // last ms of the current slice
	age := anchor - ts
	if age < 0 {
		return 0
	}
	if age >= int64(rows)*BucketMs {
		return -1
	}
	return int(age / BucketMs)
}

func floorMod(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// cell holds the aggregate for one column in one time slice.
type cell struct {
	count int
	err   bool
}

// Render returns exactly height lines (none when height <= 0), each at most
// width display cells wide: a header of column labels followed by one line per
// time slice, newest first. waiting is the number of sessions waiting on the
// user.
//
// Narrow widths degrade in order: the gutter is dropped, cell widths shrink
// toward 1, then rightmost columns are dropped.
func Render(logs []wire.MetadataLogEntry, now int64, width, height, waiting int, pal Palette) []string {
	if height <= 0 {
		return nil
	}
	lines := make([]string, 0, height)
	st := newStyler(pal)

	nCols, widths, gutter := layout(width)
	rows := height - 1
	if nCols == 0 {
		for len(lines) < height {
			lines = append(lines, "")
		}
		return lines
	}

	grid := make([][]cell, rows)
	for i := range grid {
		grid[i] = make([]cell, nCols)
	}
	gutterErr := make([]bool, rows)
	visibleMax := 0
	for _, l := range logs {
		r := BucketRow(l.TS, now, rows)
		if r < 0 {
			continue
		}
		col, ok := verbColumn[l.Verb]
		if ok && col >= nCols {
			continue
		}
		if !ok {
			if l.Tone == "error" {
				gutterErr[r] = true
			}
			continue
		}
		c := &grid[r][col]
		c.count++
		if l.Tone == "error" {
			c.err = true
		}
		if c.count > visibleMax {
			visibleMax = c.count
		}
	}
	heatMax := max(heatCap, visibleMax)

	lines = append(lines, headerLine(nCols, widths, gutter, st))

	minusMinute := BucketRow(now-60_000, now, rows)
	for r := 0; r < rows; r++ {
		var b strings.Builder
		for col := 0; col < nCols; col++ {
			if col > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(cellText(grid[r][col], col, widths[col], heatMax, st))
		}
		if gutter {
			b.WriteByte(' ')
			b.WriteString(gutterText(r, minusMinute, gutterErr[r], waiting, st))
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

// layout picks how many columns to show, their widths, and whether the gutter
// fits. The gutter shows only when every column keeps at least
// minCellWidthWithGutter cells. When every label fits, each column starts at
// its label's width; otherwise all start at zero and labels are cut. Spare
// cells then go one at a time to the narrowest column, the rightmost among
// equals, up to maxCellWidth.
func layout(width int) (nCols int, widths []int, gutter bool) {
	n := len(columns)
	avail := width
	if width-gutterWidth >= n*minCellWidthWithGutter+(n-1) {
		gutter = true
		avail = width - gutterWidth
	} else {
		n = min(n, (width+1)/2)
	}
	if n <= 0 {
		return 0, nil, false
	}
	cells := avail - (n - 1)
	widths = make([]int, n)
	labels := 0
	for i := range widths {
		labels += len(columns[i].label)
	}
	if labels <= cells {
		for i := range widths {
			widths[i] = len(columns[i].label)
		}
		cells -= labels
	}
	for ; cells > 0; cells-- {
		narrowest := n - 1
		for i := n - 2; i >= 0; i-- {
			if widths[i] < widths[narrowest] {
				narrowest = i
			}
		}
		if widths[narrowest] >= maxCellWidth {
			break
		}
		widths[narrowest]++
	}
	return n, widths, gutter
}

func headerLine(nCols int, widths []int, gutter bool, st styler) string {
	var b strings.Builder
	for col := 0; col < nCols; col++ {
		if col > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(st.paint(centered(columns[col].label, widths[col]), st.header()))
	}
	if gutter {
		b.WriteString(strings.Repeat(" ", gutterWidth))
	}
	return strings.TrimRight(b.String(), " ")
}

func centered(s string, w int) string {
	if len(s) > w {
		return s[:w]
	}
	left := (w - len(s)) / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", w-len(s)-left)
}

// heat returns the shade index 0..3 for a non-zero count and its 0..1
// intensity, on a square-root scale against heatMax.
func heat(count, heatMax int) (int, float64) {
	t := math.Sqrt(float64(count) / float64(heatMax))
	t = math.Min(t, 1)
	level := int(math.Round(4 * t))
	return min(max(level, 1), 4) - 1, t
}

func cellText(c cell, col, w, heatMax int, st styler) string {
	if c.count == 0 {
		return st.paint(strings.Repeat(glyphEmpty, w), st.color("overlay0"))
	}
	level, t := heat(c.count, heatMax)
	text := strings.Repeat(heatGlyphs[level], w)
	if c.err {
		return st.paint(text, st.color("red"))
	}
	return st.paint(text, st.heatColor(columns[col].hue, t))
}

// gutterText returns the three-cell right-hand label for row r. Priority:
// waiting bell on the top row, then the error mark for an error with no
// column, then "now" on the top row or "-1m" on the minute-old row.
func gutterText(r, minusMinute int, hasErr bool, waiting int, st styler) string {
	switch {
	case r == 0 && waiting > 0:
		return st.paint(glyphBell, st.color("yellow"))
	case hasErr:
		return st.paint(glyphError, st.color("red"))
	case r == 0:
		return st.paint("now", st.color("overlay0"))
	case r == minusMinute:
		return st.paint("-1m", st.color("overlay0"))
	}
	return ""
}

// styler turns palette entries into SGR-wrapped text; the zero styler emits
// plain text.
type styler struct {
	rgb map[string][3]int
}

func newStyler(pal Palette) styler {
	st := styler{rgb: map[string][3]int{}}
	if len(pal) == 0 {
		return st
	}
	for k, v := range fallbackColors {
		if c, ok := parseHex(v); ok {
			st.rgb[k] = c
		}
	}
	for k, v := range pal {
		if c, ok := parseHex(v); ok {
			st.rgb[k] = c
		}
	}
	return st
}

func parseHex(s string) ([3]int, bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return [3]int{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return [3]int{}, false
	}
	return [3]int{int(v >> 16), int(v >> 8 & 0xff), int(v & 0xff)}, true
}

type sgr struct {
	rgb [3]int
	ok  bool
}

func (st styler) color(key string) sgr {
	c, ok := st.rgb[key]
	return sgr{c, ok}
}

func (st styler) header() sgr {
	if c := st.color("subtext0"); c.ok {
		return c
	}
	return st.color("overlay0")
}

// heatColor blends the column hue toward the base color: intensity t=0 gives
// 35% hue, t=1 the full hue. A missing hue falls back to text; a missing base
// leaves the hue unblended.
func (st styler) heatColor(hue string, t float64) sgr {
	h := st.color(hue)
	if !h.ok {
		h = st.color("text")
	}
	if !h.ok {
		return h
	}
	base, ok := st.rgb["base"]
	if !ok {
		return h
	}
	k := 0.35 + 0.65*t
	var out [3]int
	for i := range out {
		out[i] = int(math.Round(float64(base[i]) + (float64(h.rgb[i])-float64(base[i]))*k))
	}
	return sgr{out, true}
}

func (st styler) paint(text string, c sgr) string {
	if !c.ok {
		return text
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", c.rgb[0], c.rgb[1], c.rgb[2], text)
}
