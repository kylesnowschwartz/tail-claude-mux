package server

import (
	"slices"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func TestSelectAgentPaneSwitchesClientAcrossSessions(t *testing.T) {
	var calls [][]string
	runner := func(args ...string) (string, error) {
		calls = append(calls, slices.Clone(args))
		if args[0] == "list-clients" {
			return "client\t/dev/ttys001\t123\tcurrent\t200\t50", nil
		}
		return "", nil
	}
	s := &Server{Builder: &state.Builder{Tmux: &tmux.Tmux{Run: runner}}}
	cmd := wire.ClientCommand{Session: "target", ClientTTY: "/dev/ttys001"}

	if ok := s.selectAgentPane(cmd, "%9"); !ok {
		t.Fatal("selectAgentPane returned false")
	}

	wantCommands := []string{"list-clients", "switch-client", "select-window", "select-pane"}
	if len(calls) != len(wantCommands) {
		t.Fatalf("commands = %v, want %v", calls, wantCommands)
	}
	for i, want := range wantCommands {
		if calls[i][0] != want {
			t.Fatalf("command %d = %v, want %s", i, calls[i], want)
		}
	}
	wantSwitch := []string{"switch-client", "-c", "/dev/ttys001", "-t", "target"}
	if !slices.Equal(calls[1], wantSwitch) {
		t.Fatalf("switch command = %v, want %v", calls[1], wantSwitch)
	}
}

type fakeAgentStateSource struct {
	name       string
	threadID   string
	threadName string
	verdict    tracker.ProbeVerdict
	scanCalls  int
	calls      int
}

func (f *fakeAgentStateSource) Name() string { return f.name }

func (f *fakeAgentStateSource) ScanStateForPid(int, string) tracker.ScanState {
	f.scanCalls++
	return tracker.ScanState{ThreadID: f.threadID, ThreadName: f.threadName, Verdict: f.verdict}
}

func (f *fakeAgentStateSource) ProbeLiveStatus(int, string, string) tracker.ProbeVerdict {
	f.calls++
	return f.verdict
}

func TestProbeLivenessRoutesByAgent(t *testing.T) {
	claude := &fakeAgentStateSource{name: "claude-code", verdict: tracker.ProbeEnded}
	codex := &fakeAgentStateSource{name: "codex", verdict: tracker.ProbeWorking}
	sources := []agentStateSource{claude, codex}

	if got := probeLivenessFromSources(wire.AgentEvent{Agent: "codex", PID: 42}, sources...); got != tracker.ProbeWorking {
		t.Fatalf("codex verdict = %v, want ProbeWorking", got)
	}
	if codex.calls != 1 || claude.calls != 0 {
		t.Fatalf("calls after codex probe = claude %d, codex %d", claude.calls, codex.calls)
	}
	if got := probeLivenessFromSources(wire.AgentEvent{Agent: "claude-code", PID: 43}, sources...); got != tracker.ProbeEnded {
		t.Fatalf("claude verdict = %v, want ProbeEnded", got)
	}
	if codex.calls != 1 || claude.calls != 1 {
		t.Fatalf("calls after claude probe = claude %d, codex %d", claude.calls, codex.calls)
	}
	if got := probeLivenessFromSources(wire.AgentEvent{Agent: "other", PID: 44}, sources...); got != tracker.ProbeNoSignal {
		t.Fatalf("unknown-agent verdict = %v, want ProbeNoSignal", got)
	}
}

func TestScanStateForPaneResolvesIdentityAndWorkingStatus(t *testing.T) {
	claude := &fakeAgentStateSource{name: "claude-code", threadID: "claude-thread"}
	codex := &fakeAgentStateSource{name: "codex", threadID: "codex-thread", threadName: "Fix state", verdict: tracker.ProbeWorking}

	pa, state := scanStateForPane(tracker.PanePresence{Agent: "codex", PID: 42, PaneTitle: "Codex"}, claude, codex)
	if pa.ThreadID != "codex-thread" || pa.ThreadName != "Fix state" {
		t.Fatalf("scan identity = (%q, %q), want codex thread identity", pa.ThreadID, pa.ThreadName)
	}
	if state.Verdict != tracker.ProbeWorking {
		t.Fatalf("scan verdict = %v, want ProbeWorking", state.Verdict)
	}
	if codex.scanCalls != 1 || claude.scanCalls != 0 {
		t.Fatalf("scan calls = claude %d, codex %d", claude.scanCalls, codex.scanCalls)
	}

	pa, state = scanStateForPane(tracker.PanePresence{Agent: "claude-code", PID: 43}, claude, codex)
	if pa.ThreadID != "claude-thread" || state.Verdict != tracker.ProbeNoSignal {
		t.Fatalf("claude scan = thread %q, verdict %v; want identity without a scan probe", pa.ThreadID, state.Verdict)
	}
	if claude.scanCalls != 1 || claude.calls != 0 {
		t.Fatalf("claude calls = scan %d, live probe %d; want identity-only scan", claude.scanCalls, claude.calls)
	}
}

func TestPaneScanBindsDelegation(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{
		Agent:       "codex",
		SessionName: "work",
		PaneID:      "%7",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Delegations: store}

	s.bindDelegationsFromPaneScan(map[string][]tracker.PanePresence{
		"work": {{Agent: "codex", PaneID: "%7", ThreadID: "thread-123"}},
	})

	bound, err := store.Get(record.DelegationID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ThreadID != "thread-123" || bound.BoundAt == nil {
		t.Fatalf("binding = (%q, %v), want thread-123 with bound time", bound.ThreadID, bound.BoundAt)
	}
}

func TestColdScanCompletedCodexPaneStartsDone(t *testing.T) {
	codex := &fakeAgentStateSource{name: "codex", threadID: "codex-thread", verdict: tracker.ProbeDone}
	pa, state := scanStateForPane(tracker.PanePresence{Agent: "codex", PaneID: "%7", PID: 42}, codex)
	status := scanStatus(state.Verdict)
	if status != wire.StatusDone {
		t.Fatalf("scan status = %q, want done", status)
	}

	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{
		Agent: pa.Agent, Session: "work", Status: status, ThreadID: pa.ThreadID, PID: pa.PID,
	}, false)
	tr.ApplyPanePresence("work", []tracker.PanePresence{pa})
	got := tr.GetState("work")
	if got == nil || got.Status != wire.StatusDone || got.Liveness != wire.LivenessAlive || got.PaneID != "%7" {
		t.Fatalf("cold-scan state = %#v, want alive done state on pane %%7", got)
	}
}

func TestScanEventOverWaitingPrecedence(t *testing.T) {
	const waitingSince = int64(1_000_000)
	waiting := &wire.AgentEvent{
		Agent: "claude-code", Session: "work", ThreadID: "t", Status: wire.StatusWaiting, TS: waitingSince,
		ToolDescription: "Running sleep 20", ToolVerb: "Running",
	}
	scanned := wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", TS: waitingSince + 5000}
	approved := tracker.PromptResolution{Verdict: tracker.ProbeWorking, AtMS: waitingSince + 4000}
	declined := tracker.PromptResolution{Verdict: tracker.ProbeDone, AtMS: waitingSince + 4000}

	t.Run("approval recorded after the request clears waiting to running", func(t *testing.T) {
		ev, ok := scanEventOver(waiting, scanned, tracker.ScanState{ThreadID: "t", Prompt: approved})
		if !ok || ev.Status != wire.StatusRunning {
			t.Fatalf("scanEventOver = (%q, %v), want running", ev.Status, ok)
		}
		if ev.ToolDescription != "Running sleep 20" || ev.ToolVerb != "Running" {
			t.Errorf("tool = (%q, %q), want the approved call's description kept", ev.ToolDescription, ev.ToolVerb)
		}
		if ev.ToolInvoked {
			t.Error("ToolInvoked = true, want false (the call was counted at PreToolUse)")
		}
	})

	t.Run("decline recorded after the request clears waiting to done", func(t *testing.T) {
		ev, ok := scanEventOver(waiting, scanned, tracker.ScanState{ThreadID: "t", Prompt: declined})
		if !ok || ev.Status != wire.StatusDone {
			t.Fatalf("scanEventOver = (%q, %v), want done", ev.Status, ok)
		}
		if ev.ToolDescription != "" {
			t.Errorf("toolDescription = %q, want cleared at the prompt", ev.ToolDescription)
		}
	})

	t.Run("busy recorded before the request leaves waiting alone", func(t *testing.T) {
		// The file still shows the turn's busy from before PermissionRequest.
		stale := tracker.PromptResolution{Verdict: tracker.ProbeWorking, AtMS: waitingSince - 200}
		if ev, ok := scanEventOver(waiting, scanned, tracker.ScanState{ThreadID: "t", Prompt: stale}); ok {
			t.Fatalf("scanEventOver = (%q, true), want waiting untouched", ev.Status)
		}
		sameMS := tracker.PromptResolution{Verdict: tracker.ProbeWorking, AtMS: waitingSince}
		if ev, ok := scanEventOver(waiting, scanned, tracker.ScanState{ThreadID: "t", Prompt: sameMS}); ok {
			t.Fatalf("scanEventOver at the same ms = (%q, true), want waiting untouched", ev.Status)
		}
	})

	t.Run("hung busy carries no resolution and leaves waiting alone", func(t *testing.T) {
		// ClassifyPromptResolution reports a hung busy as no signal.
		if ev, ok := scanEventOver(waiting, scanned, tracker.ScanState{ThreadID: "t"}); ok {
			t.Fatalf("scanEventOver = (%q, true), want waiting untouched", ev.Status)
		}
	})

	t.Run("general working verdict never clears waiting", func(t *testing.T) {
		// Codex rollout "working" and claude "shell" stay below a hook's waiting.
		state := tracker.ScanState{ThreadID: "t", Verdict: tracker.ProbeWorking}
		if ev, ok := scanEventOver(waiting, scanned, state); ok {
			t.Fatalf("scanEventOver = (%q, true), want waiting untouched", ev.Status)
		}
	})
}

func TestScanEventOverNonWaitingRows(t *testing.T) {
	scanned := wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", TS: 2_000_000}
	declined := tracker.PromptResolution{Verdict: tracker.ProbeDone, AtMS: 1_999_000}
	approved := tracker.PromptResolution{Verdict: tracker.ProbeWorking, AtMS: 1_999_000}

	for _, status := range []string{wire.StatusRunning, wire.StatusDone, wire.StatusIdle} {
		t.Run(status+" row ignores prompt resolutions", func(t *testing.T) {
			previous := &wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", Status: status, TS: 1_000_000}
			for _, prompt := range []tracker.PromptResolution{declined, approved} {
				if ev, ok := scanEventOver(previous, scanned, tracker.ScanState{ThreadID: "t", Prompt: prompt}); ok {
					t.Fatalf("scanEventOver = (%q, true), want row untouched", ev.Status)
				}
			}
		})
	}

	t.Run("shell verdict still marks a done row running", func(t *testing.T) {
		previous := &wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", Status: wire.StatusDone, TS: 1_000_000}
		ev, ok := scanEventOver(previous, scanned, tracker.ScanState{ThreadID: "t", Verdict: tracker.ProbeWorking})
		if !ok || ev.Status != wire.StatusRunning {
			t.Fatalf("scanEventOver = (%q, %v), want running", ev.Status, ok)
		}
	})

	t.Run("no row yet takes the general verdict only", func(t *testing.T) {
		if ev, ok := scanEventOver(nil, scanned, tracker.ScanState{ThreadID: "t", Prompt: declined}); ok {
			t.Fatalf("scanEventOver = (%q, true), want no row minted from a prompt resolution", ev.Status)
		}
		ev, ok := scanEventOver(nil, scanned, tracker.ScanState{ThreadID: "t", Verdict: tracker.ProbeDone})
		if !ok || ev.Status != wire.StatusDone {
			t.Fatalf("scanEventOver = (%q, %v), want done", ev.Status, ok)
		}
	})
}

// deriveLogEntriesLocked is the seismograph's data source: every entry it
// returns becomes one activity-log event. The contract under test: tool
// entries are keyed on ToolInvoked (each fresh call counts, even identical
// back-to-back ones), never on description change.
func TestDeriveLogEntries(t *testing.T) {
	newServer := func() *Server {
		return &Server{lastSeenByThread: map[string]lastSeen{}}
	}
	toolEv := func(desc string, invoked bool) wire.AgentEvent {
		return wire.AgentEvent{
			Agent:           "claude-code",
			Session:         "myproject",
			Status:          wire.StatusRunning,
			ThreadID:        "sess-abcd1234",
			ToolDescription: desc,
			ToolVerb:        "read",
			ToolInvoked:     invoked,
		}
	}

	t.Run("repeated identical invocations each produce an entry", func(t *testing.T) {
		s := newServer()
		var total int
		for i := 0; i < 3; i++ {
			total += len(s.deriveLogEntriesLocked(toolEv("Reading main.go", true)))
		}
		if total != 3 {
			t.Fatalf("got %d entries for 3 identical invocations, want 3", total)
		}
	})

	t.Run("a kept description without an invocation does not log", func(t *testing.T) {
		s := newServer()
		if got := len(s.deriveLogEntriesLocked(toolEv("Reading main.go", true))); got != 1 {
			t.Fatalf("invocation produced %d entries, want 1", got)
		}
		// PostToolUse-style echo: same description carried, not a new call.
		if got := len(s.deriveLogEntriesLocked(toolEv("Reading main.go", false))); got != 0 {
			t.Fatalf("non-invocation echo produced %d entries, want 0", got)
		}
	})

	t.Run("an invocation with an empty description does not log", func(t *testing.T) {
		s := newServer()
		if got := len(s.deriveLogEntriesLocked(toolEv("", true))); got != 0 {
			t.Fatalf("empty-description invocation produced %d entries, want 0", got)
		}
	})

	t.Run("tool entries carry verb and source", func(t *testing.T) {
		s := newServer()
		entries := s.deriveLogEntriesLocked(toolEv("Reading main.go", true))
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		e := entries[0]
		if e.Verb != "read" {
			t.Errorf("verb = %q, want read", e.Verb)
		}
		if e.Source != "cc 1234" {
			t.Errorf("source = %q, want %q", e.Source, "cc 1234")
		}
	})

	t.Run("thread name and status transitions still log once per change", func(t *testing.T) {
		s := newServer()
		ev := toolEv("", false)
		ev.ThreadName = "fix the bug"
		ev.Status = wire.StatusWaiting
		if got := len(s.deriveLogEntriesLocked(ev)); got != 2 {
			t.Fatalf("first sighting produced %d entries, want 2 (name + waiting)", got)
		}
		if got := len(s.deriveLogEntriesLocked(ev)); got != 0 {
			t.Fatalf("unchanged repeat produced %d entries, want 0", got)
		}
	})

	t.Run("error status without a label logs plain errored", func(t *testing.T) {
		s := newServer()
		ev := toolEv("", false)
		ev.Status = wire.StatusError
		entries := s.deriveLogEntriesLocked(ev)
		if len(entries) != 1 || entries[0].Message != "errored" {
			t.Fatalf("entries = %+v, want single 'errored'", entries)
		}
	})
}

func TestScanEventOverRestoresWaitingFromOpenDialog(t *testing.T) {
	const dialogAt = int64(1_000_000)
	scanned := wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", TS: dialogAt + 5000}
	open := tracker.ScanState{ThreadID: "t", WaitingSinceMS: dialogAt}
	done := &wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", Status: wire.StatusDone, TS: dialogAt - 500}

	ev, ok := scanEventOver(done, scanned, open)
	if !ok || ev.Status != wire.StatusWaiting || ev.TS != dialogAt {
		t.Fatalf("scanEventOver = (%q at %d, %v), want waiting at %d", ev.Status, ev.TS, ok, dialogAt)
	}

	newerHook := &wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t", Status: wire.StatusRunning, TS: dialogAt + 1}
	if ev, ok := scanEventOver(newerHook, scanned, open); ok {
		t.Fatalf("scanEventOver over a newer hook = (%q, true), want untouched", ev.Status)
	}

	restored := &ev
	if again, ok := scanEventOver(restored, scanned, open); ok {
		t.Fatalf("scanEventOver on the restored row = (%q, true), want waiting untouched", again.Status)
	}
	declined := tracker.ScanState{ThreadID: "t", Prompt: tracker.PromptResolution{Verdict: tracker.ProbeDone, AtMS: dialogAt + 2000}}
	if cleared, ok := scanEventOver(restored, scanned, declined); !ok || cleared.Status != wire.StatusDone {
		t.Fatalf("scanEventOver after decline = (%q, %v), want done", cleared.Status, ok)
	}
}
