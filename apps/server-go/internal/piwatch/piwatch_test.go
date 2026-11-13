package piwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/pi/discover"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const testCwd = "/work/proj"

var procStart = time.Date(2026, 11, 13, 10, 2, 46, 0, time.UTC)

func newTestAdapter(t *testing.T, override bool) (*Adapter, string) {
	t.Helper()
	root := t.TempDir()
	a := New(root, "")
	dir := discover.SessionDirForCwd(root, testCwd)
	if override {
		dir = t.TempDir()
		a.OverrideDir = dir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a.cwdForPID = func(int) string { return testCwd }
	a.startTimeForPID = func(int) time.Time { return procStart }
	a.now = func() int64 { return 1_000 }
	return a, dir
}

func header(id string, started time.Time, cwd string) string {
	return fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":%q,"cwd":%q}`, id, started.UTC().Format(time.RFC3339Nano), cwd)
}

func userLine(text string) string {
	return fmt.Sprintf(`{"type":"message","id":"u","parentId":null,"timestamp":"2026-11-13T10:03:01Z","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, text)
}

func assistantLine(stop string, calls ...string) string {
	var blocks []string
	for i, c := range calls {
		name, args, _ := strings.Cut(c, " ")
		blocks = append(blocks, fmt.Sprintf(`{"type":"toolCall","id":"call-%s-%d","name":%q,"arguments":%s}`, stop, i, name, args))
	}
	if len(blocks) == 0 {
		blocks = append(blocks, `{"type":"text","text":"ok"}`)
	}
	return fmt.Sprintf(`{"type":"message","id":"a","parentId":"u","timestamp":"2026-11-13T10:03:09Z","message":{"role":"assistant","stopReason":%q,"content":[%s]}}`, stop, strings.Join(blocks, ","))
}

func writeSession(t *testing.T, dir, id string, started time.Time, mod time.Time, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, started.UTC().Format("2006-01-02T15-04-05-000Z")+"_"+id+".jsonl")
	body := header(id, started, testCwd) + "\n" + strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestScanPicksSessionByProcessStart(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "other", procStart.Add(-time.Hour), procStart.Add(time.Hour), userLine("old"))
	writeSession(t, dir, "mine", procStart.Add(time.Second), procStart.Add(time.Minute), userLine("fix the bug"))
	st := a.ScanStateForPid(42, "")
	if st.ThreadID != "mine" || st.ThreadName != "fix the bug" || st.Verdict != tracker.ProbeWorking {
		t.Fatalf("got %#v", st)
	}
}

func TestScanTwoSessionsOneCwdTakesOwnStart(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "mine", procStart, procStart.Add(time.Minute), userLine("a"), assistantLine("stop"))
	writeSession(t, dir, "neighbour", procStart.Add(30*time.Second), procStart.Add(2*time.Minute), userLine("b"))
	st := a.ScanStateForPid(42, "")
	if st.ThreadID != "mine" || st.Verdict != tracker.ProbeDone {
		t.Fatalf("got %#v", st)
	}
}

func TestScanFollowsResumedSession(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "older", procStart.Add(-24*time.Hour), procStart.Add(time.Minute), userLine("resumed"))
	writeSession(t, dir, "stale", procStart.Add(-48*time.Hour), procStart.Add(-time.Hour), userLine("x"))
	if st := a.ScanStateForPid(42, ""); st.ThreadID != "older" {
		t.Fatalf("got %#v", st)
	}
}

func TestScanNoSessionYet(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "stale", procStart.Add(-48*time.Hour), procStart.Add(-time.Hour), userLine("x"))
	if st := a.ScanStateForPid(42, ""); st.HasSignal() || st.ThreadID != "" {
		t.Fatalf("got %#v", st)
	}
	if v := a.ProbeLiveStatus(42, "", ""); v != tracker.ProbeNoSignal {
		t.Fatalf("probe %v", v)
	}
	if evs := a.Activity(42, "work"); evs != nil {
		t.Fatalf("activity %#v", evs)
	}
}

func TestScanFlatOverrideDir(t *testing.T) {
	a, dir := newTestAdapter(t, true)
	path := writeSession(t, dir, "mine", procStart, procStart.Add(time.Minute), userLine("a"))
	other := filepath.Join(dir, "2026-11-13T10-02-46-000Z_elsewhere.jsonl")
	if err := os.WriteFile(other, []byte(header("elsewhere", procStart, "/other")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := a.ScanStateForPid(42, ""); st.ThreadID != "mine" {
		t.Fatalf("got %#v (want %s)", st, path)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[string]tracker.ProbeVerdict{
		"stop":    tracker.ProbeDone,
		"toolUse": tracker.ProbeWorking,
		"aborted": tracker.ProbeInterrupted,
		"error":   tracker.ProbeError,
	}
	for stop, want := range cases {
		a, dir := newTestAdapter(t, false)
		writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"), assistantLine(stop))
		if st := a.ScanStateForPid(42, ""); st.Verdict != want {
			t.Errorf("%s: verdict %v, want %v", stop, st.Verdict, want)
		}
		if v := a.ProbeLiveStatus(42, "s", ""); v != want {
			t.Errorf("%s: probe %v, want %v", stop, v, want)
		}
	}
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "s", procStart, procStart.Add(time.Minute))
	if st := a.ScanStateForPid(42, ""); st.Verdict != tracker.ProbeNoSignal || st.ThreadID != "s" {
		t.Errorf("header only: %#v", st)
	}
}

func scanActivity(a *Adapter) []wire.AgentEvent {
	a.ScanStateForPid(42, "")
	return a.Activity(42, "work")
}

func TestStatusChangeEmitsOnce(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	evs := scanActivity(a)
	if len(evs) != 1 || evs[0].Status != wire.StatusRunning || evs[0].Agent != "pi" || evs[0].Session != "work" || evs[0].ThreadID != "s" || evs[0].PID != 42 {
		t.Fatalf("first: %#v", evs)
	}
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("repeat: %#v", evs)
	}
	appendLines(t, path, assistantLine("stop"))
	evs = scanActivity(a)
	if len(evs) != 1 || evs[0].Status != wire.StatusDone {
		t.Fatalf("done: %#v", evs)
	}
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("repeat done: %#v", evs)
	}
}

func TestToolCallsEmitOnceWithoutReplay(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute),
		userLine("a"), assistantLine("toolUse", `bash {"command":"old"}`))
	evs := scanActivity(a)
	for _, ev := range evs {
		if ev.ToolInvoked {
			t.Fatalf("history replayed: %#v", evs)
		}
	}
	appendLines(t, path, assistantLine("toolUse", `read {"path":"/work/proj/main.go"}`, `bash {"command":"go test ./..."}`))
	evs = scanActivity(a)
	var tools []wire.AgentEvent
	for _, ev := range evs {
		if ev.ToolInvoked {
			tools = append(tools, ev)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("tools: %#v", evs)
	}
	if !strings.HasPrefix(tools[0].ToolDescription, "Reading") || tools[0].ToolVerb != "read" {
		t.Errorf("read call: %q %q", tools[0].ToolDescription, tools[0].ToolVerb)
	}
	if tools[1].ToolDescription != "Running go test ./..." || tools[1].ToolVerb == "" {
		t.Errorf("bash call: %q %q", tools[1].ToolDescription, tools[1].ToolVerb)
	}
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("repeat: %#v", evs)
	}
}

func TestForgetDropsDeadPids(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	scanActivity(a)
	a.Forget(map[int]bool{})
	if len(a.byPID) != 0 || len(a.files) != 0 {
		t.Fatalf("left %d pids, %d files", len(a.byPID), len(a.files))
	}
}

func TestParseLstart(t *testing.T) {
	cases := map[string]int{"Sat 14 Nov 08:50:01 2026   \n": 14, "Sat Nov 14 08:50:01 2026": 14, "Wed  4 Nov 08:50:01 2026": 4}
	for raw, day := range cases {
		got := parseLstart(raw)
		want := time.Date(2026, 11, day, 8, 50, 1, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("%q: got %v", raw, got)
		}
	}
}
