// Package explain builds the diagnostic report served by the `GET /explain`
// endpoint — it answers "why is this row stuck on <status>?" by projecting an
// AgentEvent against the tracker's prune lifecycle and reporting which tier
// governs it and when that tier will fire.
//
// Port of packages/runtime/src/agents/explain.ts (the TS file is the contract
// of record; output strings and JSON shapes are byte-compatible).
//
// Pure: Build takes the event, a `now` timestamp, and a fresh liveness probe
// verdict (the caller runs the probe so this stays I/O-free and
// unit-testable). The tier math mirrors the guards in:
//   - Tracker.ReconcileStaleRunning (ReconcileStaleMS)
//   - Tracker.PruneStuck            (StuckRunningTimeoutMS / tracker.AlivePruneCeilingMS)
//   - Tracker.PruneTerminal         (immediate once exited)
//
// If those guards change, update this in lockstep.
package explain

import (
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// Reconcile/stuck thresholds, mirroring shared.ts RECONCILE_STALE_MS and
// STUCK_RUNNING_TIMEOUT_MS. internal/server/watch.go holds private copies
// (reconcileStaleMS, stuckRunningTimeoutMS); the values must stay in sync.
const (
	// ReconcileStaleMS is how old a running+alive entry must be before the
	// reconcile pass probes its session file.
	ReconcileStaleMS = 60 * 1000
	// StuckRunningTimeoutMS is how old a running+exited entry must be
	// before PruneStuck removes it.
	StuckRunningTimeoutMS = 3 * 60 * 1000
)

// Tier IDs (explain.ts ExplainTier["id"]): stable identifiers matching the
// tracker method that owns each tier.
const (
	TierReconcile     = "reconcile"
	TierPruneStuck    = "prune-stuck"
	TierAliveCeiling  = "alive-ceiling"
	TierPruneTerminal = "prune-terminal"
	TierPruneIdle     = "prune-idle"
	// GoverningStable is the lifecycle "governing" value when no prune
	// tier currently applies to the entry.
	GoverningStable = "stable"
)

// Tier is explain.ts ExplainTier: one prune-lifecycle tier's verdict.
type Tier struct {
	// ID is the stable identifier matching the tracker method that owns
	// this tier.
	ID string `json:"id"`
	// Applies reports whether this tier governs the entry given its
	// status + liveness.
	Applies bool `json:"applies"`
	// ThresholdMS is the timeout constant this tier enforces (ms).
	ThresholdMS int64 `json:"thresholdMs"`
	// EligibleInMS is ms until this tier becomes eligible (0 = eligible
	// now). nil (JSON null) when N/A.
	EligibleInMS *int64 `json:"eligibleInMs"`
	Note         string `json:"note"`
}

// Probe is ExplainReport["probe"]: the reconcile pass's authoritative probe
// (run fresh by the caller).
type Probe struct {
	// Eligible reports whether the reconcile guard would currently run a
	// probe for this entry.
	Eligible bool `json:"eligible"`
	// Verdict is "working", "ended", or nil (JSON null) when the probe
	// had no signal.
	Verdict *string `json:"verdict"`
}

// Lifecycle is ExplainReport["lifecycle"].
type Lifecycle struct {
	// Governing is the id of the tier that will next act on this entry,
	// or "stable".
	Governing string `json:"governing"`
	// Detail is a human-readable explanation of the governing tier's
	// decision.
	Detail string `json:"detail"`
	Tiers  []Tier `json:"tiers"`
}

// Report is explain.ts ExplainReport. Optional event fields are pointers
// without omitempty so absence serializes as explicit null, matching the TS
// endpoint's JSON byte-for-byte.
type Report struct {
	Agent           string    `json:"agent"`
	Session         string    `json:"session"`
	Status          string    `json:"status"`
	Liveness        string    `json:"liveness"`
	ThreadID        *string   `json:"threadId"`
	PID             *int      `json:"pid"`
	PaneID          *string   `json:"paneId"`
	WindowIndex     *int      `json:"windowIndex"`
	PaneIndex       *int      `json:"paneIndex"`
	ToolDescription *string   `json:"toolDescription"`
	TS              int64     `json:"ts"`
	AgeMS           int64     `json:"ageMs"`
	Probe           Probe     `json:"probe"`
	Lifecycle       Lifecycle `json:"lifecycle"`
}

// Build constructs the diagnostic report (explain.ts buildExplain). `nowMS`
// and `verdict` are injected so the function is pure (no clock, no disk
// read); the route maps tracker.ProbeVerdict straight through — ProbeWorking
// → "working", ProbeEnded → "ended", ProbeNoSignal → null.
func Build(event wire.AgentEvent, nowMS int64, verdict tracker.ProbeVerdict) Report {
	status := event.Status
	liveness := event.Liveness
	if liveness == "" {
		liveness = wire.LivenessUnknown
	}
	ageMS := max(0, nowMS-event.TS)
	isRunning := status == wire.StatusRunning
	isTerminal := wire.IsTerminalStatus(status)
	isAlive := liveness == wire.LivenessAlive
	isExited := liveness == wire.LivenessExited
	hasPID := event.PID != 0

	// --- reconcile (running + alive + pid): probes the session file once stale ---
	reconcileApplies := isRunning && isAlive && hasPID
	reconcile := Tier{
		ID:           TierReconcile,
		Applies:      reconcileApplies,
		ThresholdMS:  ReconcileStaleMS,
		EligibleInMS: eligibleIn(ReconcileStaleMS, ageMS, reconcileApplies),
		Note:         "only running+alive entries with a pid are reconciled",
	}
	if reconcileApplies {
		reconcile.Note = "running+alive: probes ~/.claude/sessions/<pid>.json once stale; 'ended'→done (clears spinner), 'working'→resets the clock"
	}

	// --- pruneStuck (running): exited prunes at StuckRunningTimeoutMS ---
	stuckApplies := isRunning && isExited
	pruneStuck := Tier{
		ID:           TierPruneStuck,
		Applies:      stuckApplies,
		ThresholdMS:  StuckRunningTimeoutMS,
		EligibleInMS: eligibleIn(StuckRunningTimeoutMS, ageMS, stuckApplies),
		Note:         "applies to running+exited entries",
	}
	if stuckApplies {
		pruneStuck.Note = "running+exited: pruned once age exceeds the stuck-running timeout"
	}

	// --- alive ceiling (running + alive): last-resort backstop ---
	ceilingApplies := isRunning && isAlive
	aliveCeiling := Tier{
		ID:           TierAliveCeiling,
		Applies:      ceilingApplies,
		ThresholdMS:  tracker.AlivePruneCeilingMS,
		EligibleInMS: eligibleIn(tracker.AlivePruneCeilingMS, ageMS, ceilingApplies),
		Note:         "applies to running+alive entries",
	}
	if ceilingApplies {
		aliveCeiling.Note = "running+alive: pruned only if it reaches the ceiling with no fresh hook and no 'working' confirmation (lost terminal signal)"
	}

	// --- pruneTerminal: terminal+exited now; idle/waiting+exited now ---
	terminalApplies := isTerminal && isExited
	pruneTerminal := Tier{
		ID:           TierPruneTerminal,
		Applies:      terminalApplies,
		ThresholdMS:  0,
		EligibleInMS: eligibleIn(0, ageMS, terminalApplies),
		Note:         "applies to done/error/interrupted entries that have exited",
	}
	if terminalApplies {
		pruneTerminal.Note = "terminal+exited: pruned immediately (a dead process is a dead click target)"
	} else if isTerminal {
		pruneTerminal.Note = "terminal+alive: not pruned until the process exits"
	}

	idleApplies := (status == wire.StatusIdle || status == wire.StatusWaiting) && isExited
	pruneIdle := Tier{
		ID:           TierPruneIdle,
		Applies:      idleApplies,
		ThresholdMS:  0,
		EligibleInMS: eligibleIn(0, ageMS, idleApplies),
		Note:         "applies to idle/waiting entries that have exited",
	}
	if idleApplies {
		pruneIdle.Note = "idle/waiting+exited: pruned immediately (no narrative to preserve)"
	}

	tiers := []Tier{reconcile, pruneStuck, aliveCeiling, pruneTerminal, pruneIdle}

	// Governing tier: the applicable tier acting soonest. Among applicable
	// tiers, pick the smallest EligibleInMS; ties resolve in declaration
	// order (reconcile before the ceiling, matching the actual pass
	// ordering).
	governing := GoverningStable
	detail := status
	if liveness != wire.LivenessUnknown {
		detail += "+" + liveness
	}
	detail += ": no prune tier currently governs this entry"
	haveBest := false
	var bestEligible int64
	for _, t := range tiers {
		if !t.Applies || t.EligibleInMS == nil {
			continue
		}
		if !haveBest || *t.EligibleInMS < bestEligible {
			haveBest = true
			bestEligible = *t.EligibleInMS
			governing = t.ID
			detail = t.Note
		}
	}

	return Report{
		Agent:           event.Agent,
		Session:         event.Session,
		Status:          status,
		Liveness:        liveness,
		ThreadID:        stringOrNull(event.ThreadID),
		PID:             pidOrNull(event.PID),
		PaneID:          stringOrNull(event.PaneID),
		WindowIndex:     event.WindowIndex,
		PaneIndex:       event.PaneIndex,
		ToolDescription: stringOrNull(event.ToolDescription),
		TS:              event.TS,
		AgeMS:           ageMS,
		Probe: Probe{
			Eligible: reconcileApplies && ageMS > ReconcileStaleMS,
			Verdict:  verdictString(verdict),
		},
		Lifecycle: Lifecycle{Governing: governing, Detail: detail, Tiers: tiers},
	}
}

// eligibleIn is explain.ts eligibleIn plus the per-tier applies guard
// (the TS caller writes `applies ? eligibleIn(...) : null` at each site).
func eligibleIn(thresholdMS, ageMS int64, applies bool) *int64 {
	if !applies {
		return nil
	}
	v := max(0, thresholdMS-ageMS)
	return &v
}

// verdictString maps the tracker's probe enum onto the wire's
// "working" | "ended" | null (explain.ts ExplainProbeVerdict).
func verdictString(v tracker.ProbeVerdict) *string {
	var s string
	switch v {
	case tracker.ProbeWorking:
		s = "working"
	case tracker.ProbeEnded:
		s = "ended"
	default: // ProbeNoSignal → null
		return nil
	}
	return &s
}

// stringOrNull maps wire's ""-means-absent convention to explicit JSON null
// (the TS report nulls out absent optional fields).
func stringOrNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pidOrNull maps wire's 0-means-absent pid to explicit JSON null.
func pidOrNull(pid int) *int {
	if pid == 0 {
		return nil
	}
	return &pid
}
