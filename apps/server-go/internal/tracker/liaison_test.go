package tracker

import (
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// linkedPane is a Claude Code pane whose liaison options name worker as
// the started conversation.
func linkedPane(threadID, worker string) PanePresence {
	return PanePresence{
		Agent: "claude-code", PaneID: "%1", PID: 100, ThreadID: threadID,
		LiaisonWorkID: "w7", LiaisonParentThreadID: "parent", LiaisonWorkerThreadID: worker,
	}
}

func liaisonLink(ev *wire.AgentEvent) [2]string {
	if ev == nil {
		return [2]string{"<nil>", "<nil>"}
	}
	return [2]string{ev.LiaisonWorkID, ev.LiaisonParentThreadID}
}

func TestLiaisonLink(t *testing.T) {
	alive := func(int) bool { return true }
	linked, none := [2]string{"w7", "parent"}, [2]string{}

	t.Run("the conversation the pane names has both fields", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		tr.ApplyPanePresence("s1", []PanePresence{linkedPane("worker", "worker")})
		if got := liaisonLink(findThread(tr.GetAgents("s1"), "worker")); got != linked {
			t.Errorf("agents[] link = %v, want %v", got, linked)
		}
		if got := liaisonLink(tr.GetState("s1")); got != linked {
			t.Errorf("agentState link = %v, want %v", got, linked)
		}
		if got := liaisonLink(tr.GetEvent("s1", "claude-code", "worker", "")); got != linked {
			t.Errorf("GetEvent link = %v, want %v", got, linked)
		}
	})

	t.Run("a pane with no options gives none", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		tr.ApplyPanePresence("s1", []PanePresence{{Agent: "claude-code", PaneID: "%1", PID: 100, ThreadID: "worker"}})
		if got := liaisonLink(findThread(tr.GetAgents("s1"), "worker")); got != none {
			t.Errorf("link = %v, want none", got)
		}
	})

	t.Run("a conversation whose ID differs from the worker gets none", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		tr.ApplyPanePresence("s1", []PanePresence{linkedPane("other", "worker")})
		if got := liaisonLink(findThread(tr.GetAgents("s1"), "other")); got != none {
			t.Errorf("agents[] link = %v, want none", got)
		}
		if got := liaisonLink(tr.GetState("s1")); got != none {
			t.Errorf("agentState link = %v, want none", got)
		}
	})

	t.Run("a conversation with no known ID gets none", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		tr.ApplyPanePresence("s1", []PanePresence{linkedPane("", "worker")})
		if got := liaisonLink(tr.GetState("s1")); got != none {
			t.Errorf("link = %v, want none", got)
		}
	})

	t.Run("a missing worker option gives none", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		tr.ApplyPanePresence("s1", []PanePresence{linkedPane("worker", "")})
		if got := liaisonLink(tr.GetState("s1")); got != none {
			t.Errorf("link = %v, want none", got)
		}
	})

	t.Run("a watcher event keeps the link the scan read", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		tr.ApplyPanePresence("s1", []PanePresence{linkedPane("worker", "worker")})
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Agent: "claude-code", ThreadID: "worker", Status: wire.StatusRunning, PID: 100}), false)
		if got := liaisonLink(findThread(tr.GetAgents("s1"), "worker")); got != linked {
			t.Errorf("link = %v, want %v", got, linked)
		}
	})

	t.Run("a scan that sets or removes the options reports a change", func(t *testing.T) {
		tr := New(fixedNow(), WithIsPidAlive(alive))
		bare := PanePresence{Agent: "claude-code", PaneID: "%1", PID: 100, ThreadID: "worker"}
		tr.ApplyPanePresence("s1", []PanePresence{bare})
		if !tr.ApplyPanePresence("s1", []PanePresence{linkedPane("worker", "worker")}) {
			t.Error("changed = false when the options appear")
		}
		if tr.ApplyPanePresence("s1", []PanePresence{linkedPane("worker", "worker")}) {
			t.Error("changed = true on an identical scan")
		}
		if !tr.ApplyPanePresence("s1", []PanePresence{bare}) {
			t.Error("changed = false when the options go")
		}
		if got := liaisonLink(findThread(tr.GetAgents("s1"), "worker")); got != none {
			t.Errorf("link after removal = %v, want none", got)
		}
	})

	t.Run("another conversation that starts in the pane gets none, and the ended worker loses it", func(t *testing.T) {
		live := map[int]bool{200: true}
		tr := New(fixedNow(), WithIsPidAlive(func(pid int) bool { return live[pid] }))
		tr.ApplyEvent(newEvent(wire.AgentEvent{Session: "s1", Agent: "claude-code", ThreadID: "worker", Status: wire.StatusDone, PID: 100}), false)
		tr.ApplyPanePresence("s1", []PanePresence{linkedPane("worker", "worker")})

		next := linkedPane("next", "worker")
		next.PID = 200
		tr.ApplyPanePresence("s1", []PanePresence{next})

		agents := tr.GetAgents("s1")
		if got := liaisonLink(findThread(agents, "next")); got != none {
			t.Errorf("new conversation link = %v, want none", got)
		}
		old := findThread(agents, "worker")
		if old == nil || old.Liveness != wire.LivenessExited {
			t.Fatalf("old row = %+v, want an exited row", old)
		}
		if got := liaisonLink(old); got != none {
			t.Errorf("ended worker link = %v, want none", got)
		}
	})
}
