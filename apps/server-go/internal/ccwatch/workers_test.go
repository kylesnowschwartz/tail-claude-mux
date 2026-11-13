package ccwatch

import (
	"encoding/json"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func workerParent(t *testing.T, event string) (*Adapter, *testCtx) {
	t.Helper()
	tc := makeCtx(nil, map[int]string{100: "work"})
	a := newStartedAdapter(t, tc)
	a.processExists = func(int) bool { return true }
	p := hook(event, "parent", "/project")
	p.PID, p.ProcessSnapshot = 100, "100 1 claude"
	a.HandleHook(p)
	a.threads["parent"].threadName = "Parent thread"
	return a, tc
}

func lastWorkerEvent(t *testing.T, tc *testCtx, status string) wire.AgentEvent {
	t.Helper()
	ev := tc.events[len(tc.events)-1]
	if ev.Agent != "claude-code" || ev.Session != "work" || ev.ThreadID != "parent" || ev.PID != 100 || ev.Status != status {
		t.Fatalf("parent event = %+v, want status %s", ev, status)
	}
	return ev
}

func TestWorkerParentStatuses(t *testing.T) {
	for _, own := range []struct{ event, status string }{
		{"Stop", wire.StatusDone}, {"SessionStart", wire.StatusIdle},
		{"PermissionRequest", wire.StatusWaiting}, {"StopFailure", wire.StatusError},
	} {
		t.Run(own.event, func(t *testing.T) {
			a, tc := workerParent(t, own.event)
			a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
			want := own.status
			if want == wire.StatusDone || want == wire.StatusIdle {
				want = wire.StatusRunning
			}
			lastWorkerEvent(t, tc, want)
			if got := a.threads["parent"].status; got != own.status {
				t.Fatalf("stored status = %s", got)
			}
			a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusDone})
			lastWorkerEvent(t, tc, own.status)
		})
	}
}

func TestWorkerOutlivesParentTurn(t *testing.T) {
	a, tc := workerParent(t, "UserPromptSubmit")
	a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
	a.HandleHook(hook("Stop", "parent", "/project"))
	lastWorkerEvent(t, tc, wire.StatusRunning)
	a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusDone})
	lastWorkerEvent(t, tc, wire.StatusDone)
}

func TestWorkerToolContext(t *testing.T) {
	a, tc := workerParent(t, "UserPromptSubmit")
	a.HandleHook(toolHook("PreToolUse", "parent", "/project", "Read", map[string]json.RawMessage{"file_path": json.RawMessage(`"/project/main.go"`)}))
	state := a.threads["parent"]
	description, verb := state.lastToolDescription, state.lastToolVerb
	for _, invoked := range []bool{true, true, false} {
		a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning, ToolDescription: "Running go test ./...", ToolVerb: "run", ToolInvoked: invoked})
		ev := lastWorkerEvent(t, tc, wire.StatusRunning)
		if ev.ThreadName != "Parent thread" || ev.ToolDescription != "Running go test ./..." || ev.ToolVerb != "run" || ev.ToolInvoked != invoked {
			t.Fatalf("worker tool event = %+v", ev)
		}
	}
	if state.lastToolDescription != description || state.lastToolVerb != verb {
		t.Fatalf("parent tool changed: %+v", state)
	}
	a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", Status: wire.StatusDone})
	if ev := lastWorkerEvent(t, tc, wire.StatusRunning); ev.ToolDescription != description || ev.ToolVerb != verb || ev.ToolInvoked {
		t.Fatalf("restored event = %+v", ev)
	}
}

func TestMultipleWorkers(t *testing.T) {
	a, tc := workerParent(t, "Stop")
	for _, id := range []string{"one", "two"} {
		a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: id, WorkerPID: 200, Status: wire.StatusRunning})
	}
	a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "one", Status: wire.StatusDone})
	lastWorkerEvent(t, tc, wire.StatusRunning)
	a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "two", Status: wire.StatusIdle})
	lastWorkerEvent(t, tc, wire.StatusDone)
}

func TestDeadWorkersPeriodicProbe(t *testing.T) {
	for _, own := range []struct{ event, status string }{{"Stop", wire.StatusDone}, {"SessionStart", wire.StatusIdle}, {"PermissionRequest", wire.StatusWaiting}, {"StopFailure", wire.StatusError}} {
		t.Run(own.event, func(t *testing.T) {
			a, tc := workerParent(t, own.event)
			a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
			writeSessionFile(t, a.SessionsDir, 100, map[string]any{"sessionId": "parent", "status": "idle"})
			if own.status == wire.StatusDone || own.status == wire.StatusIdle {
				if got := a.ProbeLiveStatus(100, "parent", "✳ idle"); got != tracker.ProbeWorking {
					t.Fatalf("live probe = %v", got)
				}
			}
			a.processExists = func(pid int) bool {
				if pid != 200 {
					t.Fatalf("probe pid = %d", pid)
				}
				return false
			}
			a.RecheckWorkers()
			lastWorkerEvent(t, tc, own.status)
			if len(a.threads["parent"].workers) != 0 {
				t.Fatal("dead worker retained")
			}
		})
	}
}

func TestUnknownWorkerParentIsDropped(t *testing.T) {
	a, tc := workerParent(t, "Stop")
	before := len(tc.events)
	a.ReportWorker(WorkerReport{ParentPID: 999, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
	if len(tc.events) != before || len(a.threads["parent"].workers) != 0 {
		t.Fatal("unknown parent affected known thread")
	}
}
