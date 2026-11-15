package tracker

import (
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func TestStatusSinceFollowsStatusChanges(t *testing.T) {
	tr := New(WithNow(func() int64 { return 500 }))
	get := func() int64 {
		return tr.GetEvent("s", "claude-code", "t", "").StatusSince
	}
	apply := func(status string, ts int64) {
		tr.ApplyEvent(wire.AgentEvent{Agent: "claude-code", Session: "s", ThreadID: "t", Status: status, TS: ts}, false)
	}
	apply(wire.StatusRunning, 100)
	if got := get(); got != 100 {
		t.Fatalf("new row statusSince = %d, want 100", got)
	}
	apply(wire.StatusRunning, 200)
	if got := get(); got != 100 {
		t.Fatalf("same status statusSince = %d, want 100", got)
	}
	apply(wire.StatusDone, 300)
	if got := get(); got != 300 {
		t.Fatalf("changed status statusSince = %d, want 300", got)
	}
}

func TestStatusSinceOnReconcileEnded(t *testing.T) {
	tr := New(WithNow(func() int64 { return 10_000 }))
	tr.ApplyEvent(wire.AgentEvent{Agent: "claude-code", Session: "s", ThreadID: "t", Status: wire.StatusRunning, TS: 100, PID: 7, Liveness: wire.LivenessAlive}, false)
	tr.ReconcileStaleRunning(1, func(wire.AgentEvent) ProbeVerdict { return ProbeEnded })
	if got := tr.GetEvent("s", "claude-code", "t", "").StatusSince; got != 10_000 {
		t.Fatalf("statusSince = %d, want 10000", got)
	}
}
