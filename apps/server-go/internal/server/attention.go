package server

import (
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// withAttention is the state builder's agent source: the tracker's rows,
// with each Claude Code conversation's attention reason joined in by
// thread ID at read time. A row whose process has exited carries none.
// The tracker never stores the reason, so no rewrite of a row can lose it.
type withAttention struct {
	tracker *tracker.Tracker
	watcher *ccwatch.Adapter // nil leaves rows without a reason
}

func (w withAttention) fill(ev *wire.AgentEvent) {
	if w.watcher == nil || ev.Agent != "claude-code" || ev.ThreadID == "" || ev.Liveness == wire.LivenessExited {
		return
	}
	r := w.watcher.Attention.Get(ev.ThreadID)
	ev.Attention, ev.AttentionSince = r.Name, r.SinceMS
}

func (w withAttention) GetState(session string) *wire.AgentEvent {
	ev := w.tracker.GetState(session) // a copy
	if ev != nil {
		w.fill(ev)
	}
	return ev
}

func (w withAttention) GetAgents(session string) []wire.AgentEvent {
	agents := w.tracker.GetAgents(session)
	for i := range agents {
		w.fill(&agents[i])
	}
	return agents
}

func (w withAttention) GetEventTimestamps(session string) []int64 {
	return w.tracker.GetEventTimestamps(session)
}

func (w withAttention) IsUnseen(session string) bool { return w.tracker.IsUnseen(session) }

// forgetAttentionLocked is the tracker's OnInstanceRemoved: a Claude Code
// conversation the tracker no longer holds anywhere loses its reason.
// Runs with s.mu held.
func (s *Server) forgetAttentionLocked(ev wire.AgentEvent) {
	if ev.Agent != "claude-code" || ev.ThreadID == "" || s.Tracker.HasThread(ev.Agent, ev.ThreadID) {
		return
	}
	s.Watcher.Attention.Forget(ev.ThreadID)
}
