package ccwatch

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/claude/agents"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
)

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
		isActive := !meta.Finished && !meta.Interrupted && !meta.ModTime.Before(time.UnixMilli(startMS))
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
		a.emitWithWorker(threadID, state, session, emitInvoked, &WorkerReport{
			ToolInvoked:     true,
			ToolVerb:        FleetStepVerb(step.Kind),
			ToolDescription: textutil.TruncateToWidth(textutil.SanitizeForDisplay(step.Title), 40),
		})
	}
}
