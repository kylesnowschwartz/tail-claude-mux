// Port of packages/runtime/test/agent-tracker.test.ts. Subtest names are
// kept byte-identical to the TS test names so the suites stay diffable.
//
// Not ported (the Go tracker has no timer — the server owns scheduling):
//
//	describe("startLivenessCheck / stopLivenessCheck")
//	  - "start twice is a no-op (no leaked interval)"
//	  - "stop is safe when never started"
//	  - "onChange fires when a sweep flips an instance to exited"
//	  - "onChange does not fire on a no-op sweep (nothing to flip)"
//
// TS tests use real Date.now(); here every tracker gets a pinned clock via
// WithNow (baseTS) so age math ("4 minutes ago") is deterministic.
package tracker

import (
	"slices"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// baseTS is the test suite's "Date.now()".
const baseTS int64 = 1_700_000_000_000

func fixedNow() Option { return WithNow(func() int64 { return baseTS }) }

func minutesMS(n int64) int64 { return n * 60 * 1000 }

func ip(v int) *int { return &v }

// newEvent mirrors the TS event() helper: fills the amp/sess-1/running/now
// defaults for any field left at its zero value.
func newEvent(o wire.AgentEvent) wire.AgentEvent {
	if o.Agent == "" {
		o.Agent = "amp"
	}
	if o.Session == "" {
		o.Session = "sess-1"
	}
	if o.Status == "" {
		o.Status = wire.StatusRunning
	}
	if o.TS == 0 {
		o.TS = baseTS
	}
	return o
}

func findThread(list []wire.AgentEvent, tid string) *wire.AgentEvent {
	for i := range list {
		if list[i].ThreadID == tid {
			return &list[i]
		}
	}
	return nil
}

func threadIDs(list []wire.AgentEvent) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.ThreadID
	}
	return out
}

func TestAgentTracker(t *testing.T) {
	// --- applyEvent ---

	t.Run("applyEvent stores agent state by session", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning}), false)

		state := tr.GetState("sess-1")
		if state == nil {
			t.Fatal("expected non-nil state")
		}
		if state.Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", state.Status)
		}
		if state.Agent != "amp" {
			t.Errorf("agent = %q, want amp", state.Agent)
		}
	})

	t.Run("applyEvent overwrites previous state for same session", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone}), false)

		if got := tr.GetState("sess-1").Status; got != wire.StatusDone {
			t.Errorf("status = %q, want done", got)
		}
	})

	t.Run("applyEvent marks terminal status as unseen when session not active", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone}), false)

		if !tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 unseen")
		}
	})

	t.Run("applyEvent does NOT mark terminal status as unseen when session is active", func(t *testing.T) {
		tr := New(fixedNow())
		tr.SetActiveSessions([]string{"sess-1"})
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone}), false)

		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("applyEvent marks waiting as unseen when session not active", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusWaiting}), false)

		if !tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 unseen")
		}
	})

	t.Run("applyEvent does NOT mark waiting as unseen when session is active", func(t *testing.T) {
		tr := New(fixedNow())
		tr.SetActiveSessions([]string{"sess-1"})
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusWaiting}), false)

		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("applyEvent clears waiting unseen when same instance transitions to running", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusWaiting, ThreadID: "t1"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen after waiting")
		}

		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1"}), false)
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen after running")
		}
	})

	t.Run("applyEvent: waiting unseen on instance A, running on instance B — A stays unseen", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusWaiting, ThreadID: "t1"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen")
		}

		// Instance B is running — should NOT clear instance A's unseen
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t2"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 still unseen (t1 still unseen)")
		}
	})

	t.Run("applyEvent clears unseen when same instance transitions to non-terminal", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen")
		}

		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1"}), false)
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("applyEvent: resuming thread A does NOT clear thread B unseen", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1"}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t2"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen")
		}

		// Thread A resumes (user interacted) — but thread B is still unseen
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 still unseen (thread B)")
		}
	})

	// --- ended flag ---

	t.Run("applyEvent with ended=true removes the instance immediately", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1"}), false)
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Fatalf("agents = %d, want 1", got)
		}

		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1", Ended: true}), false)

		if got := len(tr.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("applyEvent with ended=true bypasses the terminal-prune window", func(t *testing.T) {
		// Simulates the SessionEnd-after-Stop case: tracker already holds a done
		// entry marked unseen; the ended flag must clear it without waiting.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1"}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen")
		}

		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1", Ended: true}), false)

		if got := len(tr.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("applyEvent with ended=true only removes the targeted instance", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1"}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t2"}), false)

		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1", Ended: true}), false)

		remaining := tr.GetAgents("sess-1")
		if len(remaining) != 1 {
			t.Fatalf("agents = %d, want 1", len(remaining))
		}
		if remaining[0].ThreadID != "t2" {
			t.Errorf("threadID = %q, want t2", remaining[0].ThreadID)
		}
	})

	// --- getState ---

	t.Run("getState returns null for unknown session", func(t *testing.T) {
		tr := New(fixedNow())
		if tr.GetState("unknown") != nil {
			t.Error("expected nil state")
		}
	})

	// --- markSeen ---

	t.Run("markSeen clears unseen flag but keeps terminal instances", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen")
		}

		cleared := tr.MarkSeen("sess-1")
		if !cleared {
			t.Error("MarkSeen = false, want true")
		}
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
		// Instance still exists (seen terminal), pruneTerminal will clean it up
		state := tr.GetState("sess-1")
		if state == nil {
			t.Fatal("expected non-nil state")
		}
		if state.Status != wire.StatusDone {
			t.Errorf("status = %q, want done", state.Status)
		}
	})

	t.Run("markSeen returns false when session has no unseen", func(t *testing.T) {
		tr := New(fixedNow())
		if tr.MarkSeen("nonexistent") {
			t.Error("MarkSeen = true, want false")
		}
	})

	t.Run("markSeen does NOT remove state when status is not terminal", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning}), false)
		cleared := tr.MarkSeen("sess-1")
		if cleared {
			t.Error("MarkSeen = true, want false")
		}
		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})

	t.Run("dismiss removes only the targeted agent instance", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, Agent: "amp", ThreadID: "t1"}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, Agent: "codex", ThreadID: "t2"}), false)

		dismissed := tr.Dismiss("sess-1", "amp", "t1", "", 0)

		if !dismissed {
			t.Error("Dismiss = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		got := make([]string, len(agents))
		for i, a := range agents {
			got[i] = a.Agent + ":" + a.ThreadID
		}
		if !slices.Equal(got, []string{"codex:t2"}) {
			t.Errorf("agents = %v, want [codex:t2]", got)
		}
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("dismiss with paneId targets the matching pane when threadIds collide", func(t *testing.T) {
		// Two panes running the same agent + threadId (replayed session, cloned
		// worktree). Asserts the API surface, not the storage shape.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "amp", ThreadID: "t1", PaneID: "%5", PID: 1001}), false)
		if tr.Dismiss("sess-1", "amp", "t1", "%99", 9999) {
			t.Error("Dismiss(%99, 9999) = true, want false")
		}
		if !tr.Dismiss("sess-1", "amp", "t1", "%5", 1001) {
			t.Error("Dismiss(%5, 1001) = false, want true")
		}
		if got := len(tr.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}
	})

	t.Run("getEvent returns the event for (session, agent, threadId) without scanning", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "xyz", Status: wire.StatusWaiting}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-2", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusDone}), false)

		hit := tr.GetEvent("sess-1", "claude-code", "abc", "")
		if hit == nil {
			t.Fatal("expected non-nil event")
		}
		if hit.ThreadID != "abc" {
			t.Errorf("threadID = %q, want abc", hit.ThreadID)
		}
		if hit.Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", hit.Status)
		}

		sibling := tr.GetEvent("sess-1", "claude-code", "xyz", "")
		if sibling == nil || sibling.Status != wire.StatusWaiting {
			t.Errorf("sibling = %+v, want waiting", sibling)
		}

		if tr.GetEvent("sess-1", "amp", "abc", "") != nil {
			t.Error("expected nil for wrong agent")
		}
		if tr.GetEvent("missing", "claude-code", "abc", "") != nil {
			t.Error("expected nil for missing session")
		}
	})

	t.Run("getEvent finds synthetic rows keyed by pane (no threadId)", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%9", PID: 1234}})
		// Synthetics are stored under `agent:pane:<paneId>`. Lookup by threadId
		// alone must NOT return the synthetic — it's a different identity.
		if tr.GetEvent("sess-1", "claude-code", "", "") != nil {
			t.Error("expected nil for empty threadID/paneID")
		}
		// But the synthetic IS retrievable by (agent, paneId).
		synth := tr.GetEvent("sess-1", "claude-code", "", "%9")
		if synth == nil || synth.PaneID != "%9" {
			t.Errorf("synth = %+v, want paneID %%9", synth)
		}
	})

	t.Run("getState ties at same STATUS_PRIORITY break by most-recent ts", func(t *testing.T) {
		// Two waiting agents in the same session — same priority, different ts.
		// The newer event must win regardless of arrival order.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "amp", ThreadID: "t1", Status: wire.StatusWaiting, TS: 100}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "codex", ThreadID: "t2", Status: wire.StatusWaiting, TS: 200}), false)
		if got := tr.GetState("sess-1").Agent; got != "codex" {
			t.Errorf("agent = %q, want codex", got)
		}

		// Swap arrival order; result should be the same.
		t2 := New(fixedNow())
		t2.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "codex", ThreadID: "t2", Status: wire.StatusWaiting, TS: 200}), false)
		t2.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "amp", ThreadID: "t1", Status: wire.StatusWaiting, TS: 100}), false)
		if got := t2.GetState("sess-1").Agent; got != "codex" {
			t.Errorf("agent = %q, want codex", got)
		}
	})

	t.Run("applyEvent preserves prev.pid when incoming event omits it", func(t *testing.T) {
		// Pane scanner posted a pid-bearing entry; a subsequent watcher event
		// (e.g. PostToolUse) lands without pid. The pid must survive.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t", PID: 4242, PaneID: "%1"}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t", Status: wire.StatusWaiting}), false) // no pid, no paneId
		after := findThread(tr.GetAgents("sess-1"), "t")
		if after == nil {
			t.Fatal("expected instance t")
		}
		if after.PID != 4242 {
			t.Errorf("pid = %d, want 4242", after.PID)
		}
		if after.Status != wire.StatusWaiting {
			t.Errorf("status = %q, want waiting", after.Status)
		}
	})

	t.Run("dismiss without threadId can target a synthetic by paneId", func(t *testing.T) {
		// Synthetics carry paneId but no threadId.
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "amp", PaneID: "%7", PID: 2002}})
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Fatalf("agents = %d, want 1", got)
		}

		if !tr.Dismiss("sess-1", "amp", "", "%7", 0) {
			t.Error("Dismiss = false, want true")
		}
		if got := len(tr.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}
	})

	// --- pruneStuck ---

	t.Run("pruneStuck removes running states older than timeout", func(t *testing.T) {
		tr := New(fixedNow())
		oldTs := baseTS - minutesMS(4) // 4 minutes ago
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: oldTs}), false)

		tr.PruneStuck(minutesMS(3))

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("pruneStuck does NOT remove recent running states", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: baseTS}), false)

		tr.PruneStuck(minutesMS(3))

		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})

	t.Run("pruneStuck does NOT remove non-running states regardless of age", func(t *testing.T) {
		tr := New(fixedNow())
		oldTs := baseTS - minutesMS(10)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, TS: oldTs}), false)

		tr.PruneStuck(minutesMS(3))

		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})

	t.Run("pruneStuck keeps an alive running entry within the 30min ceiling", func(t *testing.T) {
		// Interactive agents stay alive between turns; a stale-but-recent alive
		// entry must survive so reconcileStaleRunning gets a chance to confirm it.
		tr := New(fixedNow())
		ts := baseTS - minutesMS(10) // 10 min — past timeout, under ceiling
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: ts, Liveness: wire.LivenessAlive}), false)

		tr.PruneStuck(minutesMS(3))

		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})

	t.Run("pruneStuck prunes an alive running entry past the 30min ceiling (lost terminal signal)", func(t *testing.T) {
		tr := New(fixedNow())
		ts := baseTS - minutesMS(31) // beyond AlivePruneCeilingMS
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: ts, Liveness: wire.LivenessAlive}), false)

		tr.PruneStuck(minutesMS(3))

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
	})

	// --- reconcileStaleRunning ---

	t.Run("reconcileStaleRunning marks an 'ended' stale-alive running as done", func(t *testing.T) {
		tr := New(fixedNow())
		ts := baseTS - minutesMS(5)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: ts, Liveness: wire.LivenessAlive, PID: 100}), false)

		changed := tr.ReconcileStaleRunning(60*1000, func(wire.AgentEvent) ProbeVerdict { return ProbeEnded })

		if !changed {
			t.Error("changed = false, want true")
		}
		if got := tr.GetState("sess-1").Status; got != wire.StatusDone {
			t.Errorf("status = %q, want done", got)
		}
	})

	t.Run("reconcileStaleRunning bumps ts (and stays running) when probe says working", func(t *testing.T) {
		tr := New(fixedNow())
		ts := baseTS - minutesMS(5)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: ts, Liveness: wire.LivenessAlive, PID: 100}), false)

		changed := tr.ReconcileStaleRunning(60*1000, func(wire.AgentEvent) ProbeVerdict { return ProbeWorking })

		if changed {
			t.Error("changed = true, want false")
		}
		state := tr.GetState("sess-1")
		if state.Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", state.Status)
		}
		if state.TS <= ts { // staleness clock reset
			t.Errorf("ts = %d, want > %d", state.TS, ts)
		}
		// A subsequent pruneStuck must not fire on the freshly-bumped entry.
		tr.PruneStuck(minutesMS(3))
		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state after pruneStuck")
		}
	})

	t.Run("reconcileStaleRunning skips fresh, exited, non-running, and pid-less entries", func(t *testing.T) {
		tr := New(fixedNow())
		old := baseTS - minutesMS(5)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Agent: "a", Session: "s", ThreadID: "fresh", Status: wire.StatusRunning, TS: baseTS, Liveness: wire.LivenessAlive, PID: 1}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Agent: "a", Session: "s", ThreadID: "exited", Status: wire.StatusRunning, TS: old, Liveness: wire.LivenessExited, PID: 2}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Agent: "a", Session: "s", ThreadID: "done", Status: wire.StatusDone, TS: old, Liveness: wire.LivenessAlive, PID: 3}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Agent: "a", Session: "s", ThreadID: "nopid", Status: wire.StatusRunning, TS: old, Liveness: wire.LivenessAlive}), false)

		probed := 0
		tr.ReconcileStaleRunning(60*1000, func(wire.AgentEvent) ProbeVerdict { probed++; return ProbeEnded })

		if probed != 0 { // none of the four qualify
			t.Errorf("probed = %d, want 0", probed)
		}
	})

	// --- isUnseen ---

	t.Run("isUnseen returns correct value", func(t *testing.T) {
		tr := New(fixedNow())
		if tr.IsUnseen("sess-1") {
			t.Error("expected not unseen initially")
		}

		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusError}), false)
		if !tr.IsUnseen("sess-1") {
			t.Error("expected unseen after error")
		}

		tr.MarkSeen("sess-1")
		if tr.IsUnseen("sess-1") {
			t.Error("expected not unseen after markSeen")
		}
	})

	// --- handleFocus ---

	t.Run("handleFocus clears unseen for focused session", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone}), false)
		if !tr.IsUnseen("sess-1") {
			t.Fatal("expected sess-1 unseen")
		}

		hadUnseen := tr.HandleFocus("sess-1")
		if !hadUnseen {
			t.Error("HandleFocus = false, want true")
		}
		if tr.IsUnseen("sess-1") {
			t.Error("expected sess-1 not unseen")
		}
	})

	t.Run("handleFocus updates active sessions", func(t *testing.T) {
		tr := New(fixedNow())
		tr.HandleFocus("sess-2")

		// Now sess-2 is active; a terminal event shouldn't mark it unseen
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-2", Status: wire.StatusDone}), false)
		if tr.IsUnseen("sess-2") {
			t.Error("expected sess-2 not unseen")
		}
	})

	// --- getAgents unseen flag ---

	t.Run("getAgents stamps unseen flag on terminal instances", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1"}), false)
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if !agents[0].Unseen {
			t.Error("unseen = false, want true")
		}
	})

	t.Run("getAgents does not stamp unseen on seen terminal instances", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, ThreadID: "t1"}), false)
		tr.MarkSeen("sess-1")
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].Unseen {
			t.Error("unseen = true, want false")
		}
	})

	// --- getAgents ordering ---

	t.Run("getAgents returns oldest items first by first-seen", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1", TS: 100}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t2", TS: 200}), false)

		if got := threadIDs(tr.GetAgents("sess-1")); !slices.Equal(got, []string{"t1", "t2"}) {
			t.Errorf("order = %v, want [t1 t2]", got)
		}
	})

	t.Run("getAgents order is stable across status updates on existing instances", func(t *testing.T) {
		// Two agents arrive in order t1, t2.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1", TS: 100}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t2", TS: 200}), false)

		// t1 then fires a fresher status update — its ts is now newer than t2's.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, ThreadID: "t1", TS: 300}), false)

		// Sort is by first-seen, so t1 must stay above t2.
		if got := threadIDs(tr.GetAgents("sess-1")); !slices.Equal(got, []string{"t1", "t2"}) {
			t.Errorf("order = %v, want [t1 t2]", got)
		}
	})

	// --- pruneTerminal ---

	t.Run("pruneTerminal removes terminal + exited immediately (no age threshold)", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, TS: baseTS, Liveness: wire.LivenessExited}), false)
		tr.MarkSeen("sess-1")

		tr.PruneTerminal()

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
	})

	t.Run("pruneTerminal removes unseen terminal + exited too (dead rows are dead click targets)", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusDone, TS: baseTS, Liveness: wire.LivenessExited}), false)
		// NOT marked seen — unseen no longer exempts a confirmed-dead row

		tr.PruneTerminal()

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
	})

	t.Run("pruneTerminal removes idle + exited immediately (no age threshold)", func(t *testing.T) {
		// The 'opened pi then closed the pane without ever submitting a prompt'
		// case: such rows must not pile up forever.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusIdle, TS: baseTS, Liveness: wire.LivenessExited}), false)
		tr.MarkSeen("sess-1")

		tr.PruneTerminal()

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
	})

	t.Run("pruneTerminal removes waiting + exited immediately", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusWaiting, TS: baseTS, Liveness: wire.LivenessExited}), false)
		tr.MarkSeen("sess-1")

		tr.PruneTerminal()

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
	})

	t.Run("pruneTerminal does NOT remove idle + alive", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusIdle, TS: baseTS, Liveness: wire.LivenessAlive}), false)
		tr.MarkSeen("sess-1")

		tr.PruneTerminal()

		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})

	t.Run("pruneTerminal does NOT remove idle with unknown liveness", func(t *testing.T) {
		// No pane scan has confirmed the process is gone — can't safely prune.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusIdle, TS: baseTS}), false)
		tr.MarkSeen("sess-1")

		tr.PruneTerminal()

		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})

	t.Run("pruneTerminal leaves running + exited to pruneStuck", func(t *testing.T) {
		// running + exited within pruneStuck's stuck-timeout window must survive
		// pruneTerminal.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Status: wire.StatusRunning, TS: baseTS, Liveness: wire.LivenessExited}), false)
		tr.MarkSeen("sess-1")

		tr.PruneTerminal()

		if tr.GetState("sess-1") == nil {
			t.Error("expected non-nil state")
		}
	})
}

func TestApplyPanePresence(t *testing.T) {
	t.Run("enriches existing watcher entry with paneId and liveness", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)

		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].PaneID != "%1" {
			t.Errorf("paneID = %q, want %%1", agents[0].PaneID)
		}
		if agents[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agents[0].Liveness)
		}
		// Watcher status preserved — scanner doesn't touch it
		if agents[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", agents[0].Status)
		}
	})

	t.Run("mints under the thread key when the scan resolved a threadId", func(t *testing.T) {
		tr := New(fixedNow())
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%5", PID: 42, ThreadID: "t-live"}})

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].ThreadID != "t-live" {
			t.Errorf("threadID = %q, want t-live", agents[0].ThreadID)
		}

		// A later hook event for the same thread merges into the minted row
		// (same key) instead of creating a second instance.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t-live", Status: wire.StatusRunning}), false)
		agents = tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("after hook: agents = %d, want 1", len(agents))
		}
		if agents[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", agents[0].Status)
		}
		if agents[0].PaneID != "%5" {
			t.Errorf("paneID = %q, want %%5 (pane enrichment preserved)", agents[0].PaneID)
		}
	})

	t.Run("thread-keyed mint refuses a key held by a live different process", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t-dup", Status: wire.StatusRunning, PID: 100}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1", PID: 100}})

		// A second pane claims the same threadId with a different pid —
		// mint falls back to a synthetic row rather than hijacking t-dup.
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%1", PID: 100},
			{Agent: "claude-code", PaneID: "%9", PID: 200, ThreadID: "t-dup"},
		})
		agents := tr.GetAgents("sess-1")
		if len(agents) != 2 {
			t.Fatalf("agents = %d, want 2", len(agents))
		}
		for _, a := range agents {
			if a.PaneID == "%9" && a.ThreadID != "" {
				t.Errorf("synthetic fallback row carries threadID %q, want empty", a.ThreadID)
			}
			if a.ThreadID == "t-dup" && a.PID != 100 {
				t.Errorf("t-dup pid = %d, want 100 (not hijacked)", a.PID)
			}
		}
	})

	t.Run("thread-keyed mint resurrects an exited row for the same thread", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t-res", Status: wire.StatusDone, PID: 100, Liveness: wire.LivenessExited}), false)

		// Same conversation resumed in a new process/pane.
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%7", PID: 300, ThreadID: "t-res"}})
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].Liveness != wire.LivenessAlive || agents[0].PID != 300 || agents[0].PaneID != "%7" {
			t.Errorf("resurrected row = %+v, want alive pid=300 pane=%%7", agents[0])
		}
	})

	t.Run("creates minimal synthetic entry for unmatched pane agent", func(t *testing.T) {
		tr := New(fixedNow())
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%5"}})

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].Agent != "claude-code" {
			t.Errorf("agent = %q, want claude-code", agents[0].Agent)
		}
		if agents[0].PaneID != "%5" {
			t.Errorf("paneID = %q, want %%5", agents[0].PaneID)
		}
		if agents[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agents[0].Liveness)
		}
		if agents[0].Status != wire.StatusIdle { // default for synthetics
			t.Errorf("status = %q, want idle", agents[0].Status)
		}
		if agents[0].ThreadID != "" { // scanner doesn't resolve threadId
			t.Errorf("threadID = %q, want empty", agents[0].ThreadID)
		}
	})

	t.Run("transitions watcher-sourced entry to exited when pane disappears", func(t *testing.T) {
		// Watcher creates a real entry, then scanner enriches it
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Fatalf("liveness = %q, want alive", got)
		}

		// Pane disappears for one scan — held alive within hysteresis grace.
		firstChanged := tr.ApplyPanePresence("sess-1", nil)
		if firstChanged {
			t.Error("first miss changed = true, want false")
		}
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", got)
		}

		// Second consecutive miss → threshold crossed, transition to exited.
		secondChanged := tr.ApplyPanePresence("sess-1", nil)

		if !secondChanged {
			t.Error("second miss changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].Liveness != wire.LivenessExited {
			t.Errorf("liveness = %q, want exited", agents[0].Liveness)
		}
		if agents[0].PaneID != "" {
			t.Errorf("paneID = %q, want empty", agents[0].PaneID)
		}
		// Watcher-sourced data preserved
		if agents[0].ThreadID != "abc" {
			t.Errorf("threadID = %q, want abc", agents[0].ThreadID)
		}
		if agents[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", agents[0].Status)
		}
	})

	t.Run("deletes synthetic entries when their pane disappears", func(t *testing.T) {
		// Synthetic created by pane scanner (no watcher data)
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%5"}})
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Fatalf("agents = %d, want 1", got)
		}
		if got := tr.GetAgents("sess-1")[0].Status; got != wire.StatusIdle {
			t.Fatalf("status = %q, want idle", got)
		}

		// Pane disappears — first miss is held within hysteresis grace.
		tr.ApplyPanePresence("sess-1", nil)
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Fatalf("agents = %d, want 1", got)
		}

		// Second consecutive miss → synthetic deleted.
		changed := tr.ApplyPanePresence("sess-1", nil)

		if !changed {
			t.Error("changed = false, want true")
		}
		if got := len(tr.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}
	})

	t.Run("does not transition unknown-liveness running agents to exited", func(t *testing.T) {
		// A "running" entry may be mid-stream before the pane scanner sees it
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)

		changed := tr.ApplyPanePresence("sess-1", nil)

		if changed {
			t.Error("changed = true, want false")
		}
		if got := tr.GetAgents("sess-1")[0].Liveness; got != "" { // still unknown — not safe to assume exited
			t.Errorf("liveness = %q, want empty", got)
		}
	})

	t.Run("transitions unknown-liveness terminal agents to exited (seed ghosts)", func(t *testing.T) {
		// Cold-start seed creates entries with null liveness for sessions found in JSONL.
		// If the pane scanner runs and finds no matching pane, these are dead.
		tr := New(fixedNow())
		for _, status := range []string{wire.StatusDone, wire.StatusInterrupted} {
			tid := "ghost-" + status
			tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: tid, Status: status}), false)
		}

		changed := tr.ApplyPanePresence("sess-1", nil)

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 2 {
			t.Fatalf("agents = %d, want 2", len(agents))
		}
		for _, a := range agents {
			if a.Liveness != wire.LivenessExited {
				t.Errorf("liveness = %q, want exited", a.Liveness)
			}
		}
	})

	t.Run("transitions unknown-liveness idle/waiting agents to exited (seed ghosts)", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "idle-ghost", Status: wire.StatusIdle}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "waiting-ghost", Status: wire.StatusWaiting}), false)

		changed := tr.ApplyPanePresence("sess-1", nil)

		if !changed {
			t.Error("changed = false, want true")
		}
		for _, a := range tr.GetAgents("sess-1") {
			if a.Liveness != wire.LivenessExited {
				t.Errorf("liveness = %q, want exited", a.Liveness)
			}
		}
	})

	t.Run("does not mark claimed entries as exited even with terminal status", func(t *testing.T) {
		// Agent has terminal status but pane scanner found a matching pane — still alive
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "alive-done", Status: wire.StatusDone}), false)

		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%10"}})

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if agents[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agents[0].Liveness)
		}
		if agents[0].PaneID != "%10" {
			t.Errorf("paneID = %q, want %%10", agents[0].PaneID)
		}
	})

	t.Run("pruneStuck skips alive agents", func(t *testing.T) {
		tr := New(fixedNow())
		oldTs := baseTS - minutesMS(10)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning, TS: oldTs}), false)

		// Make it alive via pane presence
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		// Prune with a timeout that would normally remove it
		tr.PruneStuck(minutesMS(3))

		// Should survive because it's alive
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Errorf("agents = %d, want 1", got)
		}
	})

	t.Run("pruneStuck removes exited agents", func(t *testing.T) {
		tr := New(fixedNow())
		oldTs := baseTS - minutesMS(10)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning, TS: oldTs}), false)

		// Make alive then exited (two consecutive empty scans cross hysteresis)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
		tr.ApplyPanePresence("sess-1", nil) // first miss — held alive
		tr.ApplyPanePresence("sess-1", nil) // second miss — exits

		tr.PruneStuck(minutesMS(3))

		if tr.GetState("sess-1") != nil {
			t.Error("expected nil state")
		}
	})

	t.Run("pruneTerminal skips entries with unknown liveness (no pane scan yet)", func(t *testing.T) {
		tr := New(fixedNow())
		oldTs := baseTS - minutesMS(6)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusDone, TS: oldTs}), false)
		tr.MarkSeen("sess-1")

		// No pane scan has run — liveness is unknown
		tr.PruneTerminal()

		// Should survive: unknown liveness means we can't confirm the pane is gone
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Errorf("agents = %d, want 1", got)
		}
	})

	t.Run("pruneTerminal skips alive agents even with terminal status", func(t *testing.T) {
		tr := New(fixedNow())
		oldTs := baseTS - minutesMS(6)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusDone, TS: oldTs}), false)
		tr.MarkSeen("sess-1") // Mark seen so prune would normally remove

		// Make it alive
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		tr.PruneTerminal()

		// Should survive because alive
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Errorf("agents = %d, want 1", got)
		}
	})

	t.Run("returns false when nothing changed", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		// Apply same presence again
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		if changed {
			t.Error("changed = true, want false")
		}
	})

	t.Run("enriches watcher entry matched by agent name", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "amp", Status: wire.StatusRunning}), false)

		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "amp", PaneID: "%3"}})

		// Should not create a duplicate — enriches the existing entry
		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].PaneID != "%3" {
			t.Errorf("paneID = %q, want %%3", agents[0].PaneID)
		}
		if agents[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agents[0].Liveness)
		}
	})

	t.Run("prefers watcher entry over synthetic when enriching", func(t *testing.T) {
		// Watcher tracks the current conversation
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "current-convo", Status: wire.StatusRunning}), false)

		// Scanner just reports agent + paneId (no threadId)
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%21"}})

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].ThreadID != "current-convo" { // watcher's threadId preserved
			t.Errorf("threadID = %q, want current-convo", agents[0].ThreadID)
		}
		if agents[0].PaneID != "%21" {
			t.Errorf("paneID = %q, want %%21", agents[0].PaneID)
		}
		if agents[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agents[0].Liveness)
		}
	})

	t.Run("two panes of same agent match distinct watcher entries (no spurious idle)", func(t *testing.T) {
		// Two Claude Code instances in the same session, each with a watcher entry
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "thread-aaa", Status: wire.StatusDone}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "thread-bbb", Status: wire.StatusRunning}), false)

		// Pane scanner finds two claude-code panes
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%10"},
			{Agent: "claude-code", PaneID: "%11"},
		})

		if !changed {
			t.Error("changed = false, want true")
		}
		agents := tr.GetAgents("sess-1")

		// Both watcher entries should be enriched — no synthetic "idle" entry created
		if len(agents) != 2 {
			t.Fatalf("agents = %d, want 2", len(agents))
		}
		statuses := make([]string, len(agents))
		for i, a := range agents {
			statuses[i] = a.Status
		}
		slices.Sort(statuses)
		if !slices.Equal(statuses, []string{"done", "running"}) {
			t.Errorf("statuses = %v, want [done running]", statuses)
		}

		// Each got a distinct paneId
		panes := map[string]bool{}
		for _, a := range agents {
			panes[a.PaneID] = true
		}
		if len(panes) != 2 || !panes["%10"] || !panes["%11"] {
			t.Errorf("panes = %v, want {%%10, %%11}", panes)
		}

		// No idle synthetics
		for _, a := range agents {
			if a.Status == wire.StatusIdle {
				t.Error("unexpected idle entry")
			}
		}
	})

	t.Run("cleans up synthetic entry when watcher creates entry for same agent", func(t *testing.T) {
		// Scanner detects agent before watcher → creates synthetic
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%21"}})
		if got := len(tr.GetAgents("sess-1")); got != 1 {
			t.Fatalf("agents = %d, want 1", got)
		}
		if got := tr.GetAgents("sess-1")[0].PaneID; got != "%21" {
			t.Fatalf("paneID = %q, want %%21", got)
		}

		// Watcher catches up → creates entry with threadId, auto-cleans synthetic
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].ThreadID != "abc" {
			t.Errorf("threadID = %q, want abc", agents[0].ThreadID)
		}
		if agents[0].PaneID != "%21" {
			t.Errorf("paneID = %q, want %%21", agents[0].PaneID)
		}
		if agents[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agents[0].Liveness)
		}
	})

	t.Run("suppresses synthetic creation for ~5s after SessionEnd on the same pane", func(t *testing.T) {
		// Simulate the /exit race: SessionEnd hook fires while ps still shows
		// claude in the pane for a beat.
		mockNow := int64(1_000_000)
		localTracker := New(WithNow(func() int64 { return mockNow }))

		// Watcher entry established with a paneId via the normal flow.
		localTracker.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%50"}})
		if got := len(localTracker.GetAgents("sess-1")); got != 1 {
			t.Fatalf("agents = %d, want 1", got)
		}

		// SessionEnd fires — entry removed.
		localTracker.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusDone, Ended: true}), false)
		if got := len(localTracker.GetAgents("sess-1")); got != 0 {
			t.Fatalf("agents = %d, want 0", got)
		}

		// Pane scan within the suppression window still sees claude (exit cleanup).
		// Synthetic must NOT be created.
		mockNow += 1_000
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%50"}})
		if got := len(localTracker.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}

		mockNow += 3_000 // total +4s, still within 5s window
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%50"}})
		if got := len(localTracker.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}

		// Past the window, scanner is allowed to mint a synthetic again
		// (e.g. a freshly-launched claude in the same pane).
		mockNow += 2_000 // total +6s
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%50"}})
		agents := localTracker.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].ThreadID != "" { // synthetic, not a watcher entry
			t.Errorf("threadID = %q, want empty", agents[0].ThreadID)
		}
		if agents[0].PaneID != "%50" {
			t.Errorf("paneID = %q, want %%50", agents[0].PaneID)
		}
	})

	t.Run("end-suppression is per pane: other panes in same session unaffected", func(t *testing.T) {
		mockNow := int64(1_000_000)
		localTracker := New(WithNow(func() int64 { return mockNow }))

		localTracker.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%60"}})

		// /exit on pane %60
		localTracker.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusDone, Ended: true}), false)

		// Pane scan sees claude in %60 (residue) AND in %61 (a different live CC).
		// %60's synthetic must stay suppressed; %61 must mint its synthetic.
		mockNow += 500
		localTracker.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%60"},
			{Agent: "claude-code", PaneID: "%61"},
		})

		agents := localTracker.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].PaneID != "%61" {
			t.Errorf("paneID = %q, want %%61", agents[0].PaneID)
		}
	})

	t.Run("watcher applyEvent leaves synthetics for OTHER panes intact", func(t *testing.T) {
		// Two claude processes in the same tmux session: my watcher (pane %40)
		// and a second claude (pane %41) that hasn't fired hooks yet.
		// Scanner creates two synthetics first.
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%40"},
			{Agent: "claude-code", PaneID: "%41"},
		})
		if got := len(tr.GetAgents("sess-1")); got != 2 {
			t.Fatalf("agents = %d, want 2", got)
		}

		// My watcher arrives — graduates ONE synthetic (the one for my pane),
		// must leave the other intact.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "mine", Status: wire.StatusRunning}), false)

		agents := tr.GetAgents("sess-1")
		if len(agents) != 2 {
			t.Fatalf("agents = %d, want 2", len(agents))
		}

		mine := findThread(agents, "mine")
		other := findThread(agents, "")
		if mine == nil || other == nil {
			t.Fatalf("mine = %v, other = %v; want both present", mine, other)
		}

		if mine.PaneID != "%40" && mine.PaneID != "%41" {
			t.Errorf("mine.paneID = %q, want %%40 or %%41", mine.PaneID)
		}
		if other.PaneID == mine.PaneID {
			t.Errorf("other.paneID = %q, must differ from mine", other.PaneID)
		}
		if other.Liveness != wire.LivenessAlive {
			t.Errorf("other.liveness = %q, want alive", other.Liveness)
		}
	})

	t.Run("does not resurrect sweep-exited entry; creates synthetic for new pane occupant", func(t *testing.T) {
		// A tracker that pretends pid 42 is dead and pid 99 is alive.
		localTracker := New(fixedNow(), WithIsPidAlive(func(pid int) bool { return pid == 99 }))

		// Watcher entry for the original CC at pane %30 with pid 42.
		localTracker.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "old-thread", Status: wire.StatusRunning, PID: 42}), false)
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%30"}})

		// Sweep notices pid 42 is gone and flips liveness=exited on the watcher entry.
		localTracker.RunLivenessSweepOnce()
		afterSweep := findThread(localTracker.GetAgents("sess-1"), "old-thread")
		if afterSweep == nil || afterSweep.Liveness != wire.LivenessExited {
			t.Fatalf("afterSweep = %+v, want liveness exited", afterSweep)
		}

		// Pane %30 still has a claude (a *different* CC). Scanner must NOT
		// resurrect the dead entry — it should create a synthetic instead.
		localTracker.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%30"}})

		agents := localTracker.GetAgents("sess-1")
		dead := findThread(agents, "old-thread")
		synthetic := findThread(agents, "")

		if dead == nil || dead.Liveness != wire.LivenessExited { // still dead, not flipped back
			t.Errorf("dead = %+v, want liveness exited", dead)
		}
		if synthetic == nil { // synthetic created for the pane
			t.Fatal("expected synthetic entry")
		}
		if synthetic.PaneID != "%30" {
			t.Errorf("synthetic.paneID = %q, want %%30", synthetic.PaneID)
		}
		if synthetic.Liveness != wire.LivenessAlive {
			t.Errorf("synthetic.liveness = %q, want alive", synthetic.Liveness)
		}
	})
}

// Pane-presence hysteresis: a single missed scan must not transition an
// alive entry to exited. Bug source — process tree races during agent
// re-execs (Claude Code compaction, codex sandbox spawn). See blueprint
// §V1, ticket TMUX-HEADER-001.
func TestApplyPanePresenceHysteresis(t *testing.T) {
	t.Run("T1: single missed scan holds entry alive (under threshold)", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Fatalf("liveness = %q, want alive", got)
		}

		changed := tr.ApplyPanePresence("sess-1", nil)

		if changed {
			t.Error("changed = true, want false")
		}
		agent := tr.GetAgents("sess-1")[0]
		if agent.Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agent.Liveness)
		}
		if agent.PaneID != "%1" {
			t.Errorf("paneID = %q, want %%1", agent.PaneID)
		}
	})

	t.Run("T2: two consecutive misses cross threshold and transition to exited", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		first := tr.ApplyPanePresence("sess-1", nil)
		if first {
			t.Error("first = true, want false")
		}
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", got)
		}

		second := tr.ApplyPanePresence("sess-1", nil)
		if !second {
			t.Error("second = false, want true")
		}
		agent := tr.GetAgents("sess-1")[0]
		if agent.Liveness != wire.LivenessExited {
			t.Errorf("liveness = %q, want exited", agent.Liveness)
		}
		if agent.PaneID != "" {
			t.Errorf("paneID = %q, want empty", agent.PaneID)
		}
	})

	t.Run("T3: re-appearance during grace clears miss counter, no flicker", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		// Simulate a long flap loop: alternate empty/present scans 5 times.
		for i := 0; i < 5; i++ {
			tr.ApplyPanePresence("sess-1", nil) // single miss — held alive
			tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
			if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
				t.Fatalf("iteration %d: liveness = %q, want alive", i, got)
			}
		}
	})

	t.Run("T4: pane move (same agent name, new paneId) does not count as a miss", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
		if got := tr.GetAgents("sess-1")[0].PaneID; got != "%1" {
			t.Fatalf("paneID = %q, want %%1", got)
		}

		// Pane "moves" — agent name reappears on a different paneId.
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%2"}})
		if !changed { // paneId rebound
			t.Error("changed = false, want true")
		}
		agent := tr.GetAgents("sess-1")[0]
		if agent.Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", agent.Liveness)
		}
		if agent.PaneID != "%2" {
			t.Errorf("paneID = %q, want %%2", agent.PaneID)
		}

		// And no exit transition is queued: a subsequent missing scan is treated
		// as the FIRST miss of a new lifecycle, not the second.
		if tr.ApplyPanePresence("sess-1", nil) {
			t.Error("miss after move changed = true, want false")
		}
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", got)
		}
	})

	t.Run("T5: ended:true watcher event overrides hysteresis (immediate exit)", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		// Pane disappears — single miss, would normally hold alive.
		tr.ApplyPanePresence("sess-1", nil)
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Fatalf("liveness = %q, want alive", got)
		}

		// Definitive exit signal arrives. Entry must be removed immediately.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusDone, Ended: true}), false)
		if got := len(tr.GetAgents("sess-1")); got != 0 {
			t.Errorf("agents = %d, want 0", got)
		}
	})

	t.Run("T6: multi-instance same agent — exiting instance accrues misses while survivor stays alive", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "thread-A", Status: wire.StatusRunning}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "thread-B", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%1"},
			{Agent: "claude-code", PaneID: "%2"},
		})
		// Threads A and B may have rebound to either pane — capture the binding.
		initial := tr.GetAgents("sess-1")
		a := findThread(initial, "thread-A")
		b := findThread(initial, "thread-B")
		if a == nil || b == nil {
			t.Fatal("expected both threads present")
		}
		aPane, bPane := a.PaneID, b.PaneID
		got := []string{aPane, bPane}
		slices.Sort(got)
		if !slices.Equal(got, []string{"%1", "%2"}) {
			t.Fatalf("panes = {%q, %q}, want {%%1, %%2}", aPane, bPane)
		}

		// Survivor is whichever pane is still in the next scan; the other instance
		// is the one that exits. Pick the survivor by paneId.
		survivorPane := "%1"
		exitingPane := bPane
		if survivorPane != aPane {
			exitingPane = aPane
		}
		survivorThread := "thread-B"
		if aPane == survivorPane {
			survivorThread = "thread-A"
		}
		exitingThread := "thread-A"
		if survivorThread == "thread-A" {
			exitingThread = "thread-B"
		}

		// Scan 1 — only the survivor reports. Zero spare panes for rebinding, so
		// the exiting instance must accrue a real miss (held alive within grace).
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: survivorPane}})
		afterFirstMiss := tr.GetAgents("sess-1")
		if len(afterFirstMiss) != 2 {
			t.Fatalf("agents = %d, want 2", len(afterFirstMiss))
		}
		survivor1 := findThread(afterFirstMiss, survivorThread)
		exiting1 := findThread(afterFirstMiss, exitingThread)
		if survivor1.Liveness != wire.LivenessAlive {
			t.Errorf("survivor liveness = %q, want alive", survivor1.Liveness)
		}
		if survivor1.PaneID != survivorPane {
			t.Errorf("survivor paneID = %q, want %q", survivor1.PaneID, survivorPane)
		}
		if exiting1.Liveness != wire.LivenessAlive {
			t.Errorf("exiting liveness = %q, want alive", exiting1.Liveness)
		}
		// Exiting instance still holds its old paneId (within hysteresis grace).
		if exiting1.PaneID != exitingPane {
			t.Errorf("exiting paneID = %q, want %q", exiting1.PaneID, exitingPane)
		}

		// Scan 2 — same shape. Threshold (default 2) crosses — exiting instance
		// transitions to exited. Survivor untouched.
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: survivorPane}})
		if !changed {
			t.Error("changed = false, want true")
		}
		afterSecondMiss := tr.GetAgents("sess-1")
		survivor2 := findThread(afterSecondMiss, survivorThread)
		exiting2 := findThread(afterSecondMiss, exitingThread)
		if survivor2.Liveness != wire.LivenessAlive {
			t.Errorf("survivor liveness = %q, want alive", survivor2.Liveness)
		}
		if survivor2.PaneID != survivorPane {
			t.Errorf("survivor paneID = %q, want %q", survivor2.PaneID, survivorPane)
		}
		if exiting2.Liveness != wire.LivenessExited {
			t.Errorf("exiting liveness = %q, want exited", exiting2.Liveness)
		}
		if exiting2.PaneID != "" {
			t.Errorf("exiting paneID = %q, want empty", exiting2.PaneID)
		}
	})

	t.Run("T6b: multi-instance pane move — single survivor, single new paneId, no false miss", func(t *testing.T) {
		// Single instance whose pane moves %1 → %2 must be treated as a pane
		// move, not a miss.
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
		if got := tr.GetAgents("sess-1")[0].PaneID; got != "%1" {
			t.Fatalf("paneID = %q, want %%1", got)
		}

		// Pane moves: scan returns %2 only.
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%2"}})
		if !changed {
			t.Error("changed = false, want true")
		}
		after := tr.GetAgents("sess-1")
		if len(after) != 1 {
			t.Fatalf("agents = %d, want 1", len(after))
		}
		if after[0].Liveness != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", after[0].Liveness)
		}
		if after[0].PaneID != "%2" {
			t.Errorf("paneID = %q, want %%2", after[0].PaneID)
		}
	})

	t.Run("T7: configurable missThreshold preserves test determinism", func(t *testing.T) {
		tr := New(fixedNow(), WithMissThreshold(1))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		// With threshold=1, single miss exits immediately (matches old behaviour).
		changed := tr.ApplyPanePresence("sess-1", nil)
		if !changed {
			t.Error("changed = false, want true")
		}
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessExited {
			t.Errorf("liveness = %q, want exited", got)
		}
	})

	t.Run("T8: seed ghost path unchanged (immediate exit on first scan)", func(t *testing.T) {
		tr := New(fixedNow())
		// Seed entry: applied with seed=true, has no paneId, liveness unset.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusIdle}), true)
		// Liveness unset (seed never enriched by scanner)
		if got := tr.GetAgents("sess-1")[0].Liveness; got != "" {
			t.Fatalf("liveness = %q, want empty", got)
		}

		// Scanner runs, finds different panes (no claude-code) → seed ghost
		// transitions to exited via step 3 (NOT step 1 / hysteresis).
		changed := tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "pi", PaneID: "%9"}})
		if !changed {
			t.Error("changed = false, want true")
		}
		// The scan also mints a pi synthetic; look the seed entry up by thread
		// (the TS test's index-0 access relied on Map insertion order).
		agent := findThread(tr.GetAgents("sess-1"), "abc")
		if agent == nil {
			t.Fatal("expected seed entry")
		}
		if agent.Liveness != wire.LivenessExited {
			t.Errorf("liveness = %q, want exited", agent.Liveness)
		}
	})

	t.Run("miss state is cleared on dismiss()", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})
		tr.ApplyPanePresence("sess-1", nil) // accrue one miss

		tr.Dismiss("sess-1", "claude-code", "abc", "", 0)
		// Re-create the same instance and verify the counter is fresh.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{{Agent: "claude-code", PaneID: "%1"}})

		// First miss after re-create should still hold alive (counter starts at 0).
		changed := tr.ApplyPanePresence("sess-1", nil)
		if changed {
			t.Error("changed = true, want false")
		}
		if got := tr.GetAgents("sess-1")[0].Liveness; got != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", got)
		}
	})
}

func TestRunLivenessSweepOnce(t *testing.T) {
	t.Run("marks instances with dead pid as liveness='exited'", func(t *testing.T) {
		alive := map[int]bool{200: true, 300: true}
		tr := New(fixedNow(), WithIsPidAlive(func(pid int) bool { return alive[pid] }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Agent: "claude-code", ThreadID: "a", Status: wire.StatusRunning, PID: 200}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Agent: "claude-code", ThreadID: "b", Status: wire.StatusRunning, PID: 999}), false)

		changed := tr.RunLivenessSweepOnce()
		if !changed {
			t.Error("changed = false, want true")
		}

		agents := tr.GetAgents("s1")
		a := findThread(agents, "a")
		b := findThread(agents, "b")
		if a.Liveness != "" { // alive — no change
			t.Errorf("a.liveness = %q, want empty", a.Liveness)
		}
		if b.Liveness != wire.LivenessExited { // dead — marked
			t.Errorf("b.liveness = %q, want exited", b.Liveness)
		}
	})

	t.Run("leaves alive pid alone", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(func(int) bool { return true }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Status: wire.StatusRunning, PID: 200}), false)
		changed := tr.RunLivenessSweepOnce()
		if changed {
			t.Error("changed = true, want false")
		}
		if got := tr.GetState("s1").Liveness; got != "" {
			t.Errorf("liveness = %q, want empty", got)
		}
	})

	t.Run("skips instances without pid", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(func(int) bool { return false }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Status: wire.StatusRunning}), false) // no pid
		changed := tr.RunLivenessSweepOnce()
		if changed {
			t.Error("changed = true, want false")
		}
	})

	t.Run("skips instances in a terminal status", func(t *testing.T) {
		// A done/error/interrupted instance keeps its current liveness — the
		// sweep is for transitioning alive→exited, not for re-marking already
		// done sessions.
		tr := New(fixedNow(), WithIsPidAlive(func(int) bool { return false }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Status: wire.StatusDone, PID: 999, Liveness: wire.LivenessAlive}), false)
		changed := tr.RunLivenessSweepOnce()
		if changed {
			t.Error("changed = true, want false")
		}
		if got := tr.GetState("s1").Liveness; got != wire.LivenessAlive {
			t.Errorf("liveness = %q, want alive", got)
		}
	})

	t.Run("skips instances already marked exited (idempotent)", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(func(int) bool { return false }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Status: wire.StatusRunning, PID: 999, Liveness: wire.LivenessExited}), false)
		changed := tr.RunLivenessSweepOnce()
		if changed {
			t.Error("changed = true, want false")
		}
	})

	t.Run("handles multiple sessions independently", func(t *testing.T) {
		alive := map[int]bool{200: true}
		tr := New(fixedNow(), WithIsPidAlive(func(pid int) bool { return alive[pid] }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Status: wire.StatusRunning, PID: 200}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s2", Status: wire.StatusRunning, PID: 999}), false)

		tr.RunLivenessSweepOnce()
		if got := tr.GetState("s1").Liveness; got != "" {
			t.Errorf("s1 liveness = %q, want empty", got)
		}
		if got := tr.GetState("s2").Liveness; got != wire.LivenessExited {
			t.Errorf("s2 liveness = %q, want exited", got)
		}
	})
}

func TestSubagentPreservation(t *testing.T) {
	t.Run("preserves prior subagent when incoming event has undefined", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", ThreadID: "t1", Status: wire.StatusRunning, Subagent: "rb-orchestrator"}), false)

		// PostToolUse-style event with subagent absent — should not blank the field
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", ThreadID: "t1", Status: wire.StatusRunning}), false)

		if got := tr.GetAgents("sess-1")[0].Subagent; got != "rb-orchestrator" {
			t.Errorf("subagent = %q, want rb-orchestrator", got)
		}
	})

	t.Run("overwrites subagent when incoming event provides a new value", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", ThreadID: "t1", Status: wire.StatusRunning, Subagent: "rb-orchestrator"}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", ThreadID: "t1", Status: wire.StatusRunning, Subagent: "doc-writer"}), false)

		if got := tr.GetAgents("sess-1")[0].Subagent; got != "doc-writer" {
			t.Errorf("subagent = %q, want doc-writer", got)
		}
	})
}

func TestWindowIndexPaneIndex(t *testing.T) {
	t.Run("synthetic carries windowIndex and paneIndex from scanner input", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%7", WindowIndex: ip(3), PaneIndex: ip(1)},
		})

		a := tr.GetAgents("sess-1")[0]
		if a.WindowIndex == nil || *a.WindowIndex != 3 {
			t.Errorf("windowIndex = %v, want 3", a.WindowIndex)
		}
		if a.PaneIndex == nil || *a.PaneIndex != 1 {
			t.Errorf("paneIndex = %v, want 1", a.PaneIndex)
		}
	})

	t.Run("watcher entry adopts windowIndex/paneIndex on pane enrichment", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%7", WindowIndex: ip(2), PaneIndex: ip(0)},
		})

		a := tr.GetAgents("sess-1")[0]
		if a.WindowIndex == nil || *a.WindowIndex != 2 {
			t.Errorf("windowIndex = %v, want 2", a.WindowIndex)
		}
		if a.PaneIndex == nil || *a.PaneIndex != 0 {
			t.Errorf("paneIndex = %v, want 0", a.PaneIndex)
		}
	})

	t.Run("subsequent watcher event without pane info preserves windowIndex/paneIndex", func(t *testing.T) {
		tr := New(fixedNow())
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%7", WindowIndex: ip(2), PaneIndex: ip(0)},
		})
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusWaiting}), false)

		a := tr.GetAgents("sess-1")[0]
		if a.WindowIndex == nil || *a.WindowIndex != 2 {
			t.Errorf("windowIndex = %v, want 2", a.WindowIndex)
		}
		if a.PaneIndex == nil || *a.PaneIndex != 0 {
			t.Errorf("paneIndex = %v, want 0", a.PaneIndex)
		}
	})

	t.Run("synthetic graduation copies windowIndex/paneIndex onto adopting watcher entry", func(t *testing.T) {
		tr := New(fixedNow())
		// Pane scanner sees a claude-code in window 4 first — creates a synthetic.
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%9", WindowIndex: ip(4), PaneIndex: ip(2)},
		})
		// Then the watcher fires — should adopt the synthetic's pane fields.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)

		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].ThreadID != "abc" {
			t.Errorf("threadID = %q, want abc", agents[0].ThreadID)
		}
		if agents[0].WindowIndex == nil || *agents[0].WindowIndex != 4 {
			t.Errorf("windowIndex = %v, want 4", agents[0].WindowIndex)
		}
		if agents[0].PaneIndex == nil || *agents[0].PaneIndex != 2 {
			t.Errorf("paneIndex = %v, want 2", agents[0].PaneIndex)
		}
	})

	t.Run("getAgents sorts by (windowIndex, paneIndex, firstSeenTs)", func(t *testing.T) {
		tr := New(fixedNow())
		// Three watcher entries arrive in firstSeenTs order: t1, t2, t3.
		//
		// The TS test relies on Map insertion order for pane→entry claiming;
		// Go map iteration is randomized, so each entry carries a PID and each
		// pane reports the matching PID to pin the binding deterministically
		// (t1→%1 w2p1, t2→%2 w1p0, t3→%3 w2p0). The sort assertion is the same.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t1", Status: wire.StatusRunning, TS: 100, PID: 11}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t2", Status: wire.StatusRunning, TS: 200, PID: 12}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t3", Status: wire.StatusRunning, TS: 300, PID: 13}), false)

		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%1", PID: 11, WindowIndex: ip(2), PaneIndex: ip(1)},
			{Agent: "claude-code", PaneID: "%2", PID: 12, WindowIndex: ip(1), PaneIndex: ip(0)},
			{Agent: "claude-code", PaneID: "%3", PID: 13, WindowIndex: ip(2), PaneIndex: ip(0)},
		})

		order := threadIDs(tr.GetAgents("sess-1"))
		// Expected: rows sort by (window asc, pane asc) — t2 (w1p0), t3 (w2p0), t1 (w2p1)
		if !slices.Equal(order, []string{"t2", "t3", "t1"}) {
			t.Errorf("order = %v, want [t2 t3 t1]", order)
		}
	})

	t.Run("claim loop prefers PID match over Map iteration order (no crisscross)", func(t *testing.T) {
		tr := New(fixedNow())
		// Two claude-code watcher entries — different threadIds, different pids.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "assistant", Status: wire.StatusRunning, PID: 1001}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "rb-orch", Status: wire.StatusRunning, PID: 2002}), false)

		// Scanner emits panes in the OPPOSITE order — rb-orch's pane first.
		// With pid-aware claiming, each pane claims the entry whose pid matches.
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%200", PID: 2002, WindowIndex: ip(2), PaneIndex: ip(2)},
			{Agent: "claude-code", PaneID: "%100", PID: 1001, WindowIndex: ip(4), PaneIndex: ip(1)},
		})

		agents := tr.GetAgents("sess-1")
		assistant := findThread(agents, "assistant")
		rbOrch := findThread(agents, "rb-orch")
		if assistant.PaneID != "%100" {
			t.Errorf("assistant.paneID = %q, want %%100", assistant.PaneID)
		}
		if assistant.WindowIndex == nil || *assistant.WindowIndex != 4 {
			t.Errorf("assistant.windowIndex = %v, want 4", assistant.WindowIndex)
		}
		if rbOrch.PaneID != "%200" {
			t.Errorf("rbOrch.paneID = %q, want %%200", rbOrch.PaneID)
		}
		if rbOrch.WindowIndex == nil || *rbOrch.WindowIndex != 2 {
			t.Errorf("rbOrch.windowIndex = %v, want 2", rbOrch.WindowIndex)
		}
	})

	t.Run("claim loop falls back to PID-less watcher when no PID match available", func(t *testing.T) {
		tr := New(fixedNow())
		// Cold-boot watcher entry — no pid yet (hook hasn't fired).
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning}), false)

		// Scanner emits a pane with a pid that doesn't match anything yet —
		// the PID-less entry is a fair fallback target.
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%5", PID: 1234, WindowIndex: ip(1), PaneIndex: ip(0)},
		})

		agents := tr.GetAgents("sess-1")
		if len(agents) != 1 {
			t.Fatalf("agents = %d, want 1", len(agents))
		}
		if agents[0].ThreadID != "abc" {
			t.Errorf("threadID = %q, want abc", agents[0].ThreadID)
		}
		if agents[0].PaneID != "%5" {
			t.Errorf("paneID = %q, want %%5", agents[0].PaneID)
		}
	})

	t.Run("claim loop refuses to bind to an entry whose pid disagrees", func(t *testing.T) {
		tr := New(fixedNow())
		// Watcher resolved pid 5555.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "abc", Status: wire.StatusRunning, PID: 5555}), false)

		// Scanner sees a different pid (e.g. a freshly-launched second claude
		// whose hook hasn't fired yet) — must NOT claim the pid 5555 entry.
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%9", PID: 9999, WindowIndex: ip(1), PaneIndex: ip(0)},
		})

		agents := tr.GetAgents("sess-1")
		watcher := findThread(agents, "abc")
		// Watcher entry must not have been bound to %9.
		if watcher.PaneID != "" {
			t.Errorf("watcher.paneID = %q, want empty", watcher.PaneID)
		}
		// A synthetic should have been minted for the new pane.
		var synthetic *wire.AgentEvent
		for i := range agents {
			if agents[i].PaneID == "%9" {
				synthetic = &agents[i]
				break
			}
		}
		if synthetic == nil {
			t.Fatal("expected synthetic for %9")
		}
		if synthetic.PID != 9999 {
			t.Errorf("synthetic.pid = %d, want 9999", synthetic.PID)
		}
	})

	t.Run("getAgents puts rows without windowIndex last", func(t *testing.T) {
		tr := New(fixedNow())
		// t1 has no pane info; t2 is on window 5.
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t1", Status: wire.StatusRunning, TS: 100}), false)
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "sess-1", Agent: "claude-code", ThreadID: "t2", Status: wire.StatusRunning, TS: 200}), false)
		tr.ApplyPanePresence("sess-1", []PanePresence{
			{Agent: "claude-code", PaneID: "%2", WindowIndex: ip(5), PaneIndex: ip(0)},
		})

		// Only one entry got enriched (which one is claim-order dependent);
		// check that the watcher entry with windowIndex sorts before the one
		// without.
		agents := tr.GetAgents("sess-1")
		idxWith, idxWithout := -1, -1
		for i := range agents {
			if agents[i].WindowIndex != nil {
				idxWith = i
			} else {
				idxWithout = i
			}
		}
		if idxWith == -1 || idxWithout == -1 {
			t.Fatalf("expected one row with and one without windowIndex, got %+v", agents)
		}
		if idxWith >= idxWithout {
			t.Errorf("row with windowIndex at %d, without at %d; want with < without", idxWith, idxWithout)
		}
	})
}
