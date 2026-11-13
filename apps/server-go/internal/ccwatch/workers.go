package ccwatch

import "github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"

// WorkerReport describes activity from a worker of a Claude Code process.
type WorkerReport struct {
	ParentPID       int
	WorkerID        string
	WorkerPID       int
	Status          string
	ToolDescription string
	ToolVerb        string
	ToolInvoked     bool
}

// ReportWorker folds worker activity into its known parent thread. Reports
// without a resolved parent are dropped. Call with the server lock held.
func (a *Adapter) ReportWorker(report WorkerReport) {
	if a.ctx == nil || report.ParentPID <= 1 {
		return
	}
	for threadID, state := range a.threads {
		if state.pid != report.ParentPID {
			continue
		}
		if report.Status == wire.StatusIdle || wire.IsTerminalStatus(report.Status) {
			delete(state.workers, report.WorkerID)
		} else {
			if state.workers == nil {
				state.workers = make(map[string]int)
			}
			state.workers[report.WorkerID] = report.WorkerPID
		}
		session := a.ctx.ResolveSessionByPid(state.pid)
		if session == "" {
			return
		}
		kind := emitUpdate
		if report.ToolInvoked {
			kind = emitInvoked
		}
		a.emitWithWorker(threadID, state, session, kind, &report)
		return
	}
}

func (a *Adapter) workerStatus(threadID string, state *threadState) string {
	if len(state.workers) == 0 || state.status == wire.StatusError || state.status == wire.StatusWaiting {
		return state.status
	}
	if a.dialogOpen(threadID, state.pid) {
		return wire.StatusWaiting
	}
	if state.status == wire.StatusIdle || state.status == wire.StatusDone {
		return wire.StatusRunning
	}
	return state.status
}

// RecheckWorkers removes exited workers through the live-status probe.
// Call with the server lock held during the periodic pane scan.
func (a *Adapter) RecheckWorkers() {
	for threadID, state := range a.threads {
		if len(state.workers) > 0 {
			a.ProbeLiveStatus(state.pid, threadID, "")
		}
	}
}
