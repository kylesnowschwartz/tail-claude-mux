package ccwatch

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/claude/agents"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const workerQuietLimit = 30 * time.Minute

// scanWorkerFiles tracks active subagents and consumes fleet steps once per poll.
func (a *Adapter) scanWorkerFiles(threadID string, state *threadState) []agents.FleetStep {
	sessionPath := state.thinking.path
	if sessionPath == "" {
		return nil
	}
	metas := agents.ScanSubagentMeta(sessionPath)
	if len(metas) == 0 {
		state.fileWorkers = nil
		state.workersScanned = true
		return nil
	}
	startMS := state.firstSeenMS
	if file := a.readSessionFile(state.pid); file != nil && file.SessionID == threadID && file.StartedAt > 0 {
		startMS = int64(file.StartedAt)
	}
	active := make(map[string]bool)
	if state.fleetStepOffsets == nil {
		state.fleetStepOffsets = make(map[string]int64)
	}
	var steps []agents.FleetStep
	for _, meta := range metas {
		wasActive := state.fileWorkers[meta.ID]
		offset := state.fleetStepOffsets[meta.ID]
		isActive := !meta.Finished && !meta.Interrupted && !meta.ModTime.Before(time.UnixMilli(startMS)) &&
			!meta.ModTime.Before(time.UnixMilli(a.now()).Add(-workerQuietLimit))
		if strings.HasPrefix(meta.AgentType, "fleet:") {
			if !state.workersScanned {
				state.fleetStepOffsets[meta.ID] = meta.Size
			} else if isActive || wasActive || meta.Size > offset {
				path := filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents", "agent-"+meta.ID+".jsonl")
				batch, next, _ := agents.ReadFleetSteps(path, offset)
				steps = append(steps, batch...)
				state.fleetStepOffsets[meta.ID] = next
			}
		}
		if isActive {
			active[meta.ID] = true
		}
	}
	state.fileWorkers = active
	state.workersScanned = true
	return steps
}

func (a *Adapter) emitWorkerSteps(threadID string, state *threadState, session string, steps []agents.FleetStep) {
	for _, step := range steps {
		a.emitWithStep(threadID, state, session, emitInvoked, &stepActivity{
			verb:        FleetStepVerb(step.Kind),
			description: textutil.TruncateToWidth(textutil.SanitizeForDisplay(step.Title), 40),
		})
	}
}

func (a *Adapter) workerStatus(threadID string, state *threadState) string {
	if len(state.fileWorkers) == 0 || state.status == wire.StatusError || state.status == wire.StatusWaiting {
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

// RecheckWorkers refreshes worker activity and emits any effective status change.
// Call with the server lock held during the periodic pane scan.
func (a *Adapter) RecheckWorkers() {
	for threadID, state := range a.threads {
		before := a.workerStatus(threadID, state)
		steps := a.scanWorkerFiles(threadID, state)
		changed := before != a.workerStatus(threadID, state)
		if a.ctx == nil || (!changed && len(steps) == 0) {
			continue
		}
		session := a.ctx.ResolveSessionByPid(state.pid)
		if session == "" {
			continue
		}
		if changed {
			a.emit(threadID, state, session, emitUpdate)
		}
		a.emitWorkerSteps(threadID, state, session, steps)
	}
}

type stepActivity struct{ verb, description string }
