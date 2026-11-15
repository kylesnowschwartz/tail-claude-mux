package server

import (
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
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

func TestPaneScanRestoresPermissionFromOpenDialog(t *testing.T) {
	s := newAttentionTestServer(t)
	dialogAt := time.Now().UnixMilli() - 5000
	reading := scanReading{
		ev:    wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "a", PID: 42, PaneID: "%1", TS: time.Now().UnixMilli()},
		state: tracker.ScanState{ThreadID: "a", WaitingSinceMS: dialogAt},
	}
	s.mu.Lock()
	s.applyScanReadingLocked(reading)
	s.mu.Unlock()
	if ev := agentByThread(t, s, "a"); ev.Attention != "permission" || ev.AttentionSince != dialogAt {
		t.Fatalf("restored = %q at %d, want permission at %d", ev.Attention, ev.AttentionSince, dialogAt)
	}
}

func TestPaneScanRestoreKeepsHookReason(t *testing.T) {
	s := newAttentionTestServer(t)
	q := ccHook("PreToolUse", "a")
	q.ToolName = "AskUserQuestion"
	postHook(t, s, q)
	held := s.Watcher.Attention.Get("a")
	reading := scanReading{
		ev:    wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "a", TS: time.Now().UnixMilli()},
		state: tracker.ScanState{ThreadID: "a", WaitingSinceMS: time.Now().UnixMilli() + 1000},
	}
	s.mu.Lock()
	s.applyScanReadingLocked(reading)
	s.mu.Unlock()
	if got := s.Watcher.Attention.Get("a"); got != held {
		t.Fatalf("hook reason overwritten: %+v, held %+v", got, held)
	}
}

func TestPaneScanWithUnchangedStatusDoesNotRebuildEndedReason(t *testing.T) {
	s := newAttentionTestServer(t)
	postHook(t, s, ccHook("StopFailure", "a"))
	bindPane(s, "a", "%1", wire.StatusError)
	postPaneFocus(t, s, "%1")
	if got := s.Watcher.Attention.Get("a"); got.Name != "" {
		t.Fatalf("focus kept %+v", got)
	}
	reading := scanReading{
		ev:    wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "a", PaneID: "%1", TS: time.Now().UnixMilli()},
		state: tracker.ScanState{ThreadID: "a", Verdict: tracker.ProbeError},
	}
	s.mu.Lock()
	s.applyScanReadingLocked(reading)
	s.mu.Unlock()
	if got := s.Watcher.Attention.Get("a"); got.Name != "" {
		t.Fatalf("scan rebuilt an ended reason: %+v", got)
	}
}
