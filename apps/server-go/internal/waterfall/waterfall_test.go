package waterfall

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const now = int64(1_000_000_000_000 - 1_000_000_000_000%BucketMs) // on a slice boundary

var sgrRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return sgrRe.ReplaceAllString(s, "") }

func log(verb string, ts int64) wire.MetadataLogEntry {
	return wire.MetadataLogEntry{Message: verb, Verb: verb, TS: ts}
}

func errLog(verb string, ts int64) wire.MetadataLogEntry {
	l := log(verb, ts)
	l.Tone = "error"
	return l
}

func repeat(l wire.MetadataLogEntry, n int) []wire.MetadataLogEntry {
	out := make([]wire.MetadataLogEntry, n)
	for i := range out {
		out[i] = l
	}
	return out
}

func width(s string) int { return utf8.RuneCountInString(plain(s)) }

func TestEmptyState(t *testing.T) {
	got := Render(nil, now, 37, 10, 0, nil)
	if len(got) != 10 {
		t.Fatalf("lines = %d, want 10", len(got))
	}
	if !strings.Contains(got[0], "edit") || !strings.Contains(got[0], "think") {
		t.Errorf("header = %q", got[0])
	}
	for i, l := range got[1:] {
		if strings.ContainsAny(l, "░▒▓█") {
			t.Errorf("row %d has heat: %q", i, l)
		}
	}
	if !strings.HasSuffix(got[1], " now") {
		t.Errorf("top row = %q, want now gutter", got[1])
	}
	if !strings.HasSuffix(got[9], " -1m") {
		t.Errorf("last row = %q, want -1m gutter", got[9])
	}
	for _, l := range got[2:9] {
		if strings.HasSuffix(l, "now") || strings.HasSuffix(l, "-1m") {
			t.Errorf("unexpected gutter label: %q", l)
		}
	}
}

func TestGolden37x10(t *testing.T) {
	logs := append(repeat(log("run", now+10), 9), repeat(log("edit", now+20), 4)...)
	logs = append(logs, log("read", now-1), log("grep", now-1), log("search", now-9_000), log("web", now-17_000))
	logs = append(logs, errLog("edit", now-26_000), errLog("error", now-34_000))
	got := strings.Join(Render(logs, now, 37, 10, 1, nil), "\n")
	want := strings.Join([]string{
		"edit read  run   web  task  think",
		"▓▓▓▓ ···· █████ ····· ····· ····· \U000F0D59",
		"···· ░░░░ ····· ····· ····· ·····",
		"···· ░░░░ ····· ····· ····· ·····",
		"···· ···· ····· ░░░░░ ····· ·····",
		"░░░░ ···· ····· ····· ····· ·····",
		"···· ···· ····· ····· ····· ····· \uF00D",
		"···· ···· ····· ····· ····· ·····",
		"···· ···· ····· ····· ····· ·····",
		"···· ···· ····· ····· ····· ····· -1m",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBurstInRun(t *testing.T) {
	got := Render(repeat(log("run", now+5), 8), now, 37, 4, 0, nil)
	runCol := strings.Fields(got[1])[2]
	if !strings.HasPrefix(runCol, "█") {
		t.Errorf("run cell = %q, want full block; row %q", runCol, got[1])
	}
	if strings.ContainsAny(got[2], "░▒▓█") {
		t.Errorf("older row should be empty: %q", got[2])
	}
}

func TestHeatScaleStretchesOnBurst(t *testing.T) {
	logs := append(repeat(log("run", now), 40), repeat(log("edit", now), 10)...)
	got := Render(logs, now, 37, 2, 0, nil)
	cells := strings.Fields(got[1])
	if !strings.HasPrefix(cells[0], "▓") && !strings.HasPrefix(cells[0], "▒") {
		t.Errorf("edit(10) against stretched cap 40 should be mid shade: %q", got[1])
	}
	if !strings.HasPrefix(cells[2], "█") {
		t.Errorf("run(40) should be full: %q", got[1])
	}
}

func TestHeatLevels(t *testing.T) {
	cases := []struct{ count, shade int }{{1, 0}, {2, 1}, {4, 2}, {8, 3}, {20, 3}}
	for _, c := range cases {
		if got, _ := heat(c.count, max(heatCap, c.count)); c.count <= heatCap && got != c.shade {
			t.Errorf("heat(%d) = %d, want %d", c.count, got, c.shade)
		}
	}
}

func TestErrorInColumnIsRedAndUntaggedErrorMarksGutter(t *testing.T) {
	pal := testPalette()
	logs := []wire.MetadataLogEntry{errLog("run", now), errLog("error", now-1)}
	lines := Render(logs, now, 37, 4, 0, pal)
	red := "\x1b[38;2;243;139;168m"
	if !strings.Contains(lines[1], red+"░") {
		t.Errorf("error cell not red: %q", lines[1])
	}
	if !strings.Contains(lines[2], red+"") {
		t.Errorf("untagged error gutter not marked: %q", lines[2])
	}
	plainLines := Render(logs, now, 37, 4, 0, nil)
	if !strings.HasSuffix(plainLines[2], "\uF00D") {
		t.Errorf("plain gutter = %q", plainLines[2])
	}
}

func TestWaitingBellWinsOverNow(t *testing.T) {
	got := Render(nil, now, 37, 3, 2, nil)
	if !strings.Contains(got[1], "\U000F0D59") || strings.Contains(got[1], "now") {
		t.Errorf("top row = %q", got[1])
	}
	got = Render(nil, now, 37, 3, 0, nil)
	if strings.Contains(got[1], "\U000F0D59") {
		t.Errorf("bell without waiting: %q", got[1])
	}
}

func TestFutureTimestampClampsToTopRow(t *testing.T) {
	got := Render([]wire.MetadataLogEntry{log("edit", now+3_600_000)}, now, 37, 4, 0, nil)
	if !strings.HasPrefix(got[1], "░") {
		t.Errorf("future log not in top row: %q", got[1])
	}
}

func TestAgedOutLogsDropped(t *testing.T) {
	rows := 3
	// rows 0..2 cover ages up to rows*8s before the end of the current slice.
	in := log("edit", now-int64(rows-1)*BucketMs)
	out := log("edit", now-int64(rows)*BucketMs)
	got := Render([]wire.MetadataLogEntry{in, out}, now, 37, rows+1, 0, nil)
	filled := 0
	for _, l := range got[1:] {
		if strings.ContainsAny(l, "░▒▓█") {
			filled++
		}
	}
	if filled != 1 || !strings.ContainsAny(got[3], "░") {
		t.Errorf("filled rows = %d, lines:\n%s", filled, strings.Join(got, "\n"))
	}
}

// seismographBucketIndex is activity.ts bucketIndex.
func seismographBucketIndex(ts, now int64, cells int) int {
	age := now - ts
	if age < 0 {
		return cells - 1
	}
	if age >= int64(cells)*BucketMs {
		return -1
	}
	return cells - 1 - int(age/BucketMs)
}

func TestBucketRowMatchesSeismographAtBoundaries(t *testing.T) {
	rows := 9
	for k := int64(-2); k <= int64(rows)+1; k++ {
		for _, d := range []int64{-1, 0, 1} {
			ts := now - k*BucketMs + d
			want := seismographBucketIndex(ts, now+BucketMs-1, rows)
			if want >= 0 {
				want = rows - 1 - want
			}
			if got := BucketRow(ts, now, rows); got != want {
				t.Errorf("ts=now-%d*8s%+d: row %d, want %d", k, d, got, want)
			}
		}
	}
	// On the boundary, the seismograph with now itself agrees for past logs.
	for k := int64(1); k < int64(rows); k++ {
		ts := now - k*BucketMs
		if got, want := BucketRow(ts, now, rows), rows-1-seismographBucketIndex(ts, now, rows); got != want {
			t.Errorf("aligned now: k=%d got %d want %d", k, got, want)
		}
	}
}

func TestBucketsAlignToSliceBoundary(t *testing.T) {
	n := now + 5_000 // 5 s into the slice
	if r := BucketRow(now, n, 5); r != 0 {
		t.Errorf("slice start row = %d, want 0", r)
	}
	if r := BucketRow(now-1, n, 5); r != 1 {
		t.Errorf("just before slice row = %d, want 1", r)
	}
}

func TestGeometry(t *testing.T) {
	logs := append(repeat(log("run", now), 5), errLog("error", now-9_000), log("think", now))
	for _, w := range []int{37, 24, 12, 5, 3, 1} {
		for _, h := range []int{10, 3, 1} {
			for _, pal := range []Palette{nil, testPalette()} {
				lines := Render(logs, now, w, h, 1, pal)
				if len(lines) != h {
					t.Fatalf("w=%d h=%d: %d lines", w, h, len(lines))
				}
				for i, l := range lines {
					if width(l) > w {
						t.Errorf("w=%d h=%d line %d is %d cells: %q", w, h, i, width(l), plain(l))
					}
				}
			}
		}
	}
}

func TestDegradation(t *testing.T) {
	cases := []struct {
		w          int
		cols       int
		gutter     bool
		minCellWid int
	}{
		{37, 6, true, 4},
		{27, 6, true, 3},
		{26, 6, false, 3},
		{24, 6, false, 3},
		{12, 6, false, 1},
		{11, 6, false, 1},
		{10, 5, false, 1},
		{5, 3, false, 1},
	}
	for _, c := range cases {
		n, widths, gutter := layout(c.w)
		if n != c.cols || gutter != c.gutter || widths[0] < c.minCellWid {
			t.Errorf("w=%d: cols=%d gutter=%v widths=%v", c.w, n, gutter, widths)
		}
	}
	lines := Render(nil, now, 5, 3, 0, nil)
	if lines[0] != "e r r" || lines[1] != "· · ·" {
		t.Errorf("width 5: %q", lines)
	}
	lines = Render(nil, now, 12, 2, 0, nil)
	if lines[1] != "· · · · · ··" {
		t.Errorf("width 12: %q", lines)
	}
	lines = Render(nil, now, 24, 2, 0, nil)
	if lines[0] != "edit read run web task thi" && width(lines[0]) > 24 {
		t.Errorf("width 24 header: %q", lines[0])
	}
}

func TestColumnsAreUniformWidthPerColumn(t *testing.T) {
	logs := []wire.MetadataLogEntry{log("edit", now), log("list", now), log("skill", now-9_000)}
	lines := Render(logs, now, 37, 5, 0, nil)
	ref := fieldWidths(lines[2])
	for _, l := range lines[1:] {
		got := fieldWidths(l[:len(l)-len(" now")*boolToInt(strings.HasSuffix(l, " now"))])
		_ = got
		if w := fieldWidths(l); len(w) < 6 {
			t.Errorf("row %q has %d cells", l, len(w))
		} else if fmt.Sprint(w[:6]) != fmt.Sprint(ref[:6]) {
			t.Errorf("widths differ: %v vs %v", w[:6], ref[:6])
		}
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fieldWidths(s string) []int {
	var out []int
	for _, f := range strings.Fields(s) {
		out = append(out, utf8.RuneCountInString(f))
	}
	return out
}

func TestVerbMapping(t *testing.T) {
	cases := map[string]int{
		"edit": 0, "read": 1, "list": 1, "search": 1, "run": 2, "web": 3, "task": 4, "skill": 4, "thinking": 5,
	}
	for v, want := range cases {
		if got, ok := verbColumn[v]; !ok || got != want {
			t.Errorf("verb %q -> %d, %v; want %d", v, got, ok, want)
		}
	}
	for _, v := range []string{"", "error", "misc", "bogus"} {
		if _, ok := verbColumn[v]; ok {
			t.Errorf("verb %q should have no column", v)
		}
	}
}

func testPalette() Palette {
	return Palette{
		"text": "#cdd6f4", "subtext0": "#a6adc8", "overlay0": "#6c7086", "base": "#1e1e2e",
		"blue": "#89b4fa", "green": "#a6e3a1", "red": "#f38ba8", "yellow": "#f9e2af",
		"peach": "#fab387", "teal": "#94e2d5", "mauve": "#cba6f7", "lavender": "#b4befe",
	}
}

func TestColorOutput(t *testing.T) {
	logs := repeat(log("run", now), 6)
	lines := Render(logs, now, 37, 10, 1, testPalette())
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "\x1b[38;2;") || !strings.Contains(all, "\x1b[0m") {
		t.Fatalf("no SGR codes: %q", all)
	}
	for _, l := range lines {
		if width(l) > 37 {
			t.Errorf("line too wide: %q", plain(l))
		}
	}
	// Plain content is identical with and without a palette.
	nilLines := Render(logs, now, 37, 10, 1, nil)
	for i := range lines {
		if plain(lines[i]) != nilLines[i] {
			t.Errorf("line %d differs: %q vs %q", i, plain(lines[i]), nilLines[i])
		}
	}
	for _, l := range nilLines {
		if strings.Contains(l, "\x1b") {
			t.Errorf("nil palette emitted escape: %q", l)
		}
	}
}

func TestSparsePaletteFallsBackGracefully(t *testing.T) {
	lines := Render(repeat(log("edit", now), 3), now, 37, 3, 1, Palette{"text": "#ffffff"})
	if !strings.Contains(strings.Join(lines, ""), "\x1b[38;2;") {
		t.Error("expected some color from sparse palette")
	}
	lines = Render(repeat(log("edit", now), 3), now, 37, 3, 1, Palette{"bogus": "nothex"})
	for _, l := range lines {
		if strings.Contains(l, "\x1b[38;2;") && width(l) > 37 {
			t.Errorf("too wide: %q", l)
		}
	}
}

func TestHeightZero(t *testing.T) {
	if got := Render(nil, now, 37, 0, 0, nil); len(got) != 0 {
		t.Errorf("height 0: %q", got)
	}
}
