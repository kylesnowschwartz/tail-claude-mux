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

func TestRecheckWorkersOnlyCleansWorkers(t *testing.T) {
	for _, alive := range []bool{true, false} {
		name := "exited"
		if alive {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			a, tc := workerParent(t, "Stop")
			a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
			writeSessionFile(t, a.SessionsDir, 100, map[string]any{"sessionId": "different-thread", "status": "idle"})
			a.processExists = func(int) bool { return alive }
			before := len(tc.events)
			a.RecheckWorkers()
			state := a.threads["parent"]
			if state == nil {
				t.Fatal("cleanup deleted the parent thread")
			}
			wantEmits, wantWorkers := 1, 0
			if alive {
				wantEmits, wantWorkers = 0, 1
			}
			if len(state.workers) != wantWorkers || len(tc.events)-before != wantEmits {
				t.Fatalf("workers = %d, emits = %d; want %d, %d", len(state.workers), len(tc.events)-before, wantWorkers, wantEmits)
			}
		})
	}
}

func TestWorkersPreserveNormalLiveStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		sessionID string
		title     string
		want      tracker.ProbeVerdict
	}{
		{name: "waiting session", status: "waiting", want: tracker.ProbeEnded},
		{name: "idle session", status: "idle", want: tracker.ProbeEnded},
		{name: "done session", status: "done", want: tracker.ProbeEnded},
		{name: "replaced Claude process", status: "busy", sessionID: "other", want: tracker.ProbeEnded},
		{name: "hung Claude process", status: "busy", want: tracker.ProbeEnded},
		{name: "ended title", title: "✳ idle", want: tracker.ProbeEnded},
		{name: "busy session", status: "busy", want: tracker.ProbeWorking},
		{name: "no signal", want: tracker.ProbeWorking},
		{name: "unknown title", title: "project", want: tracker.ProbeWorking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, events := workerParent(t, "Stop")
			a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
			if tc.status != "" {
				sessionID := tc.sessionID
				if sessionID == "" {
					sessionID = "parent"
				}
				updatedAt := a.now()
				if tc.name == "hung Claude process" {
					updatedAt -= busyHungMS + 1
				}
				writeSessionFile(t, a.SessionsDir, 100, map[string]any{"sessionId": sessionID, "status": tc.status, "updatedAt": updatedAt})
			}
			before := len(events.events)
			if got := a.ProbeLiveStatus(100, "parent", tc.title); got != tc.want {
				t.Fatalf("verdict = %v, want %v", got, tc.want)
			}
			_, retained := a.threads["parent"]
			if retained != (tc.want != tracker.ProbeEnded) {
				t.Fatalf("thread retained = %v for verdict %v", retained, tc.want)
			}
			if len(events.events) != before {
				t.Fatal("live worker probe emitted an event")
			}
		})
	}
}

func TestLastExitedWorkerReturnsNormalVerdict(t *testing.T) {
	for _, status := range []string{"idle", "done", ""} {
		t.Run("session status "+status, func(t *testing.T) {
			a, tc := workerParent(t, "Stop")
			a.ReportWorker(WorkerReport{ParentPID: 100, WorkerID: "worker", WorkerPID: 200, Status: wire.StatusRunning})
			writeSessionFile(t, a.SessionsDir, 100, map[string]any{"sessionId": "parent", "status": status})
			a.processExists = func(int) bool { return false }
			before := len(tc.events)
			want := tracker.ProbeEnded
			if status == "" {
				want = tracker.ProbeNoSignal
			}
			if got := a.ProbeLiveStatus(100, "parent", ""); got != want {
				t.Fatalf("verdict = %v, want %v", got, want)
			}
			if len(tc.events) != before+1 {
				t.Fatalf("cleanup emits = %d, want 1", len(tc.events)-before)
			}
			lastWorkerEvent(t, tc, wire.StatusDone)
		})
	}
}
