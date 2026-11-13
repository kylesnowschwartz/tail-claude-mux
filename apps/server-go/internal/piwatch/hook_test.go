package piwatch

import (
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func hook(event, file string) wire.HookPayload {
	return wire.HookPayload{Agent: "pi", Event: event, PID: 42, SessionID: "sid", Cwd: testCwd, TranscriptPath: file}
}

func onlyStatus(t *testing.T, evs []wire.AgentEvent, want string) wire.AgentEvent {
	t.Helper()
	if len(evs) != 1 || evs[0].Status != want || evs[0].Agent != "pi" || evs[0].Session != "work" || evs[0].PID != 42 {
		t.Fatalf("events %#v, want one %s", evs, want)
	}
	return evs[0]
}

func TestHookStatusMapping(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.SetLive(map[int]string{42: "work"})
	ev := onlyStatus(t, a.HandleHook(hook("agent_start", path)), wire.StatusRunning)
	if ev.ThreadID != "s" {
		t.Fatalf("thread %q", ev.ThreadID)
	}
	onlyStatus(t, a.HandleHook(hook("ui_prompt_start", path)), wire.StatusWaiting)
	onlyStatus(t, a.HandleHook(hook("ui_prompt_end", path)), wire.StatusRunning)
	if evs := a.HandleHook(hook("ui_prompt_end", path)); len(evs) != 0 {
		t.Fatalf("repeat status emitted %#v", evs)
	}
	aborted := true
	settle := hook("agent_settled", path)
	settle.Aborted = &aborted
	onlyStatus(t, a.HandleHook(settle), wire.StatusInterrupted)
	onlyStatus(t, a.HandleHook(hook("agent_start", path)), wire.StatusRunning)
	onlyStatus(t, a.HandleHook(hook("agent_settled", path)), wire.StatusDone)
	onlyStatus(t, a.HandleHook(hook("agent_start", path)), wire.StatusRunning)
	appendLines(t, path, assistantLine("error"))
	onlyStatus(t, a.HandleHook(hook("agent_settled", path)), wire.StatusDone)
	onlyStatus(t, scanActivity(a), wire.StatusError)
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("error repeated: %#v", evs)
	}
}

func TestPromptEndRestoresPriorStatus(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.SetLive(map[int]string{42: "work"})
	onlyStatus(t, a.HandleHook(hook("ui_prompt_start", path)), wire.StatusWaiting)
	onlyStatus(t, a.HandleHook(hook("ui_prompt_end", path)), wire.StatusIdle)
	a.HandleHook(hook("agent_start", path))
	a.HandleHook(hook("agent_settled", path))
	onlyStatus(t, a.HandleHook(hook("ui_prompt_start", path)), wire.StatusWaiting)
	onlyStatus(t, a.HandleHook(hook("ui_prompt_end", path)), wire.StatusDone)
	a.HandleHook(hook("agent_start", path))
	onlyStatus(t, a.HandleHook(hook("ui_prompt_start", path)), wire.StatusWaiting)
	onlyStatus(t, a.HandleHook(hook("ui_prompt_end", path)), wire.StatusRunning)
}

func TestHookBetweenActivityAndResolveWins(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("agent_start", path))
	scanActivity(a)
	appendLines(t, path, assistantLine("toolUse", `bash {"command":"make"}`))
	a.ScanStateForPid(42, "")
	reading := a.Activity(42, "work")
	onlyStatus(t, a.HandleHook(hook("agent_settled", path)), wire.StatusDone)
	evs := a.Resolve(reading)
	if len(evs) != 1 || !evs[0].ToolInvoked || evs[0].Status != wire.StatusDone {
		t.Fatalf("stale reading: %#v", evs)
	}
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("later scan: %#v", evs)
	}
}

func TestReadingOfOldFileDropped(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	pathA := writeSession(t, dir, "a", procStart, procStart.Add(time.Minute), userLine("a"))
	pathB := writeSession(t, dir, "b", procStart.Add(time.Minute), procStart.Add(2*time.Minute), userLine("b"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("agent_start", pathA))
	a.ScanStateForPid(42, "")
	reading := a.Activity(42, "work")
	a.HandleHook(hook("session_start", pathB))
	if evs := a.Resolve(reading); len(evs) != 0 {
		t.Fatalf("old file events: %#v", evs)
	}
	if st := a.ScanStateForPid(42, ""); st.ThreadID != "b" {
		t.Fatalf("scan thread %q", st.ThreadID)
	}
}

func TestLostSettleRecoveredAfterTwoScans(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("agent_start", path))
	appendLines(t, path, assistantLine("stop"))
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("first scan: %#v", evs)
	}
	onlyStatus(t, scanActivity(a), wire.StatusDone)
}

func TestSafetyNetSkipsWaitingAndNeverRuns(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"), assistantLine("stop"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("agent_start", path))
	a.HandleHook(hook("ui_prompt_start", path))
	for i := 0; i < 3; i++ {
		if evs := scanActivity(a); len(evs) != 0 {
			t.Fatalf("waiting changed: %#v", evs)
		}
	}
	a.HandleHook(hook("ui_prompt_end", path))
	a.HandleHook(hook("agent_settled", path))
	appendLines(t, path, userLine("custom message reads as working"))
	for i := 0; i < 3; i++ {
		if evs := scanActivity(a); len(evs) != 0 {
			t.Fatalf("settled changed: %#v", evs)
		}
	}
}

func TestSessionSwitchStartsNewThreadFresh(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	pathA := writeSession(t, dir, "a", procStart, procStart.Add(time.Minute), userLine("a"))
	pathB := writeSession(t, dir, "b", procStart.Add(time.Minute), procStart.Add(2*time.Minute))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("agent_start", pathA))
	a.HandleHook(hook("agent_settled", pathA))
	ev := onlyStatus(t, a.HandleHook(hook("session_start", pathB)), wire.StatusIdle)
	if ev.ThreadID != "b" {
		t.Fatalf("thread %q", ev.ThreadID)
	}
}

func TestHookFromUnknownPidWaitsForScan(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	if evs := a.HandleHook(hook("ui_prompt_start", path)); len(evs) != 0 {
		t.Fatalf("unknown pid emitted %#v", evs)
	}
	if evs := scanActivity(a); len(evs) != 1 || evs[0].Status != wire.StatusWaiting {
		t.Fatalf("pending hook not applied: %#v", evs)
	}
}

func TestStalePendingHookDropped(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.HandleHook(hook("ui_prompt_start", path))
	a.now = func() int64 { return 1_000 + (pendingHookTTL + time.Second).Milliseconds() }
	if evs := scanActivity(a); len(evs) != 1 || evs[0].Status != wire.StatusRunning {
		t.Fatalf("stale hook applied: %#v", evs)
	}
}

func TestHookFileWinsOverHeuristic(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	named := writeSession(t, dir, "named", procStart.Add(-time.Hour), procStart.Add(time.Minute), userLine("a"))
	writeSession(t, dir, "newer", procStart, procStart.Add(5*time.Minute), userLine("b"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("session_start", named))
	if st := scanPid(a, 42); st.ThreadID != "named" || st.Verdict != tracker.ProbeNoSignal {
		t.Fatalf("got %#v", st)
	}
}

func TestScanDoesNotUndoWaitingOrSettled(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("ui_prompt_start", path))
	for i := 0; i < 2; i++ {
		if evs := scanActivity(a); len(evs) != 0 {
			t.Fatalf("waiting undone: %#v", evs)
		}
	}
	if v := a.ProbeLiveStatus(42, "s", ""); v != tracker.ProbeNoSignal {
		t.Fatalf("probe while waiting %v", v)
	}
	a.HandleHook(hook("agent_start", path))
	appendLines(t, path, assistantLine("toolUse", `bash {"command":"make"}`))
	a.HandleHook(hook("agent_settled", path))
	evs := scanActivity(a)
	if len(evs) != 1 || !evs[0].ToolInvoked || evs[0].Status != wire.StatusDone {
		t.Fatalf("settled undone: %#v", evs)
	}
}

func TestFileOnlyPidUnchanged(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	if st := scanPid(a, 42); st.Verdict != tracker.ProbeWorking {
		t.Fatalf("got %#v", st)
	}
	other := hook("agent_settled", "")
	other.PID = 99
	a.HandleHook(other)
	if evs := a.Resolve(a.Activity(42, "work")); len(evs) != 1 || evs[0].Status != wire.StatusRunning {
		t.Fatalf("events %#v", evs)
	}
}

func TestShutdownQuitEndsRow(t *testing.T) {
	a, dir := newTestAdapter(t, false)
	path := writeSession(t, dir, "s", procStart, procStart.Add(time.Minute), userLine("a"))
	a.SetLive(map[int]string{42: "work"})
	a.HandleHook(hook("agent_start", path))
	quit := hook("session_shutdown", path)
	quit.ShutdownReason = "quit"
	evs := a.HandleHook(quit)
	if len(evs) != 1 || !evs[0].Ended || evs[0].ThreadID != "s" {
		t.Fatalf("events %#v", evs)
	}
	if evs := scanActivity(a); len(evs) != 0 {
		t.Fatalf("ended pid still active: %#v", evs)
	}
	newSession := hook("session_shutdown", path)
	newSession.ShutdownReason = "new"
	a2, _ := newTestAdapter(t, false)
	a2.SetLive(map[int]string{42: "work"})
	if evs := a2.HandleHook(newSession); len(evs) != 1 || evs[0].Ended || evs[0].Status != wire.StatusIdle {
		t.Fatalf("shutdown for /new: %#v", evs)
	}
}

func TestNonPiHookIgnored(t *testing.T) {
	a, _ := newTestAdapter(t, false)
	a.SetLive(map[int]string{42: "work"})
	p := hook("agent_start", "")
	p.Agent = "codex"
	if evs := a.HandleHook(p); evs != nil {
		t.Fatalf("events %#v", evs)
	}
}
