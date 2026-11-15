package server

import (
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func TestAttentionForgottenWhenTrackerRemovesConversation(t *testing.T) {
	s := newAttentionTestServer(t)
	postHook(t, s, ccHook("Stop", "a"))
	bindPane(s, "a", "%1", wire.StatusDone)
	if s.Watcher.Attention.Get("a").Name != "finished" {
		t.Fatal("no reason before removal")
	}
	s.mu.Lock()
	removed := s.Tracker.Dismiss("work", "claude-code", "a", "", 0)
	s.mu.Unlock()
	if !removed {
		t.Fatal("dismiss removed nothing")
	}
	if got := s.Watcher.Attention.Get("a"); got.Name != "" {
		t.Fatalf("reason kept after removal: %+v", got)
	}
}

func TestExitedRowServesNoAttention(t *testing.T) {
	s := newAttentionTestServer(t)
	postHook(t, s, ccHook("StopFailure", "a"))
	s.mu.Lock()
	s.Tracker.ApplyEvent(wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "a", Status: wire.StatusRunning, Liveness: wire.LivenessExited, TS: time.Now().UnixMilli()}, false)
	s.mu.Unlock()
	if ev := agentByThread(t, s, "a"); ev.Attention != "" || ev.AttentionSince != 0 {
		t.Fatalf("exited row serves attention: %+v", ev)
	}
}
