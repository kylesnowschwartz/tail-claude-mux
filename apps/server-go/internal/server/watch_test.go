package server

import (
	"slices"
	"testing"

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

func (f *fakeAgentStateSource) ScanStateForPid(int, string) (string, string, tracker.ProbeVerdict) {
	f.scanCalls++
	return f.threadID, f.threadName, f.verdict
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
	if got := probeLivenessFromSources(wire.AgentEvent{Agent: "pi", PID: 44}, sources...); got != tracker.ProbeNoSignal {
		t.Fatalf("unknown-agent verdict = %v, want ProbeNoSignal", got)
	}
}

func TestScanStateForPaneResolvesIdentityAndWorkingStatus(t *testing.T) {
	claude := &fakeAgentStateSource{name: "claude-code", threadID: "claude-thread"}
	codex := &fakeAgentStateSource{name: "codex", threadID: "codex-thread", threadName: "Fix state", verdict: tracker.ProbeWorking}

	pa, verdict := scanStateForPane(tracker.PanePresence{Agent: "codex", PID: 42, PaneTitle: "Codex"}, claude, codex)
	if pa.ThreadID != "codex-thread" || pa.ThreadName != "Fix state" {
		t.Fatalf("scan identity = (%q, %q), want codex thread identity", pa.ThreadID, pa.ThreadName)
	}
	if verdict != tracker.ProbeWorking {
		t.Fatalf("scan verdict = %v, want ProbeWorking", verdict)
	}
	if codex.scanCalls != 1 || claude.scanCalls != 0 {
		t.Fatalf("scan calls = claude %d, codex %d", claude.scanCalls, codex.scanCalls)
	}

	pa, verdict = scanStateForPane(tracker.PanePresence{Agent: "claude-code", PID: 43}, claude, codex)
	if pa.ThreadID != "claude-thread" || verdict != tracker.ProbeNoSignal {
		t.Fatalf("claude scan = thread %q, verdict %v; want identity without a scan probe", pa.ThreadID, verdict)
	}
	if claude.scanCalls != 1 || claude.calls != 0 {
		t.Fatalf("claude calls = scan %d, live probe %d; want identity-only scan", claude.scanCalls, claude.calls)
	}
}

func TestColdScanCompletedCodexPaneStartsDone(t *testing.T) {
	codex := &fakeAgentStateSource{name: "codex", threadID: "codex-thread", verdict: tracker.ProbeDone}
	pa, verdict := scanStateForPane(tracker.PanePresence{Agent: "codex", PaneID: "%7", PID: 42}, codex)
	status := scanStatus(verdict)
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

	t.Run("error status surfaces the carried error label", func(t *testing.T) {
		s := newServer()
		// pi's agent_end error: the truncated error text rides in as the
		// description with ToolInvoked=false — it must reach the log as the
		// error entry, not vanish behind a generic "errored".
		ev := toolEv("API rate limit exceeded", false)
		ev.Status = wire.StatusError
		entries := s.deriveLogEntriesLocked(ev)
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		if entries[0].Message != "API rate limit exceeded" {
			t.Errorf("message = %q, want the error text", entries[0].Message)
		}
		if entries[0].Tone != "error" {
			t.Errorf("tone = %q, want error", entries[0].Tone)
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
