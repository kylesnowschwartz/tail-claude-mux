package server

import (
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

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
}
