// Package tracker ports packages/runtime/src/agents/tracker.ts — the
// agent-instance state machine: instance keying, unseen tracking, pane
// presence folding, prune tiers, and the stale-running reconcile pass.
// The TS file is the reference; the doc comments and constants mirror it
// deliberately so the two stay diffable.
//
// Field conventions on wire.AgentEvent: "" means unset for strings, 0 for
// PID/FirstSeenTS, nil for WindowIndex/PaneIndex. Liveness "" is the
// TS null/undefined (seed entries), distinct from "alive"/"exited".
//
// Not safe for concurrent use — the server serializes all access under
// its own lock, the same way the bun server's single JS thread does.
package tracker

import (
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const maxEventTimestamps = 30

// recentEndSuppressMS: window after SessionEnd during which the pane
// scanner must not mint a synthetic for that pane (ps shows the exiting
// process for a beat).
const recentEndSuppressMS = 5_000

// defaultMissThreshold: consecutive missed pane scans before alive→exited,
// absorbing false negatives from agent re-execs (compaction, sandboxes).
const defaultMissThreshold = 2

// AlivePruneCeilingMS is the last-resort ceiling for an alive running
// entry with no fresh hook and no "working" probe confirmation.
const AlivePruneCeilingMS = 30 * 60 * 1000

var statusPriority = map[string]int{
	wire.StatusRunning:     5,
	wire.StatusError:       4,
	wire.StatusInterrupted: 3,
	wire.StatusWaiting:     2,
	wire.StatusDone:        1,
	wire.StatusIdle:        0,
}

// InstanceKey mirrors tracker.ts instanceKey.
func InstanceKey(agent, threadID string) string {
	if threadID != "" {
		return agent + ":" + threadID
	}
	return agent
}

func syntheticKey(agent, paneID string) string { return agent + ":pane:" + paneID }

func isSyntheticKey(k string) bool { return strings.Contains(k, ":pane:") }

// PanePresence is contracts/agent.ts PanePresenceInput: the scanner's
// answer to "is there a live agent process in this pane?". PID 0 = not
// resolved; WindowIndex/PaneIndex nil = unknown. ThreadID is the agent's
// thread identity when the server can resolve it at scan time (e.g.
// claude-code's sessions/<pid>.json); "" = unknown, mint synthetically.
type PanePresence struct {
	Agent       string
	PaneID      string
	PID         int
	WindowIndex *int
	PaneIndex   *int
	PaneTitle   string
	ThreadID    string
	ThreadName  string
}

// ProbeVerdict is a watcher's authoritative liveness answer.
type ProbeVerdict int

const (
	ProbeNoSignal ProbeVerdict = iota // file absent / sdk-cli / no probe
	ProbeWorking
	ProbeEnded
	ProbeDone
	ProbeInterrupted
	ProbeError
)

// Tracker is the AgentTracker port.
type Tracker struct {
	// instances: session → instance key → event.
	instances map[string]map[string]*wire.AgentEvent
	// eventTimestamps: session → recent event ts (sparkline data).
	eventTimestamps map[string][]int64
	// unseen: "session\x00key" set.
	unseen map[string]bool
	// active sessions (focus-driven seen/unseen policy).
	active map[string]bool
	// paneMisses: "session\x00key" → consecutive missed scans.
	paneMisses map[string]int
	// recentlyEndedPanes: "session\x00agent\x00paneId" → suppression expiry.
	recentlyEndedPanes map[string]int64

	missThreshold int
	isPidAlive    func(pid int) bool
	now           func() int64 // epoch ms
}

// Option configures a Tracker (test seams).
type Option func(*Tracker)

// WithMissThreshold overrides the pane-miss grace count.
func WithMissThreshold(n int) Option { return func(t *Tracker) { t.missThreshold = n } }

// WithIsPidAlive injects the pid probe.
func WithIsPidAlive(f func(int) bool) Option { return func(t *Tracker) { t.isPidAlive = f } }

// WithNow injects the clock (epoch ms).
func WithNow(f func() int64) Option { return func(t *Tracker) { t.now = f } }

// New returns an empty tracker.
func New(opts ...Option) *Tracker {
	t := &Tracker{
		instances:          map[string]map[string]*wire.AgentEvent{},
		eventTimestamps:    map[string][]int64{},
		unseen:             map[string]bool{},
		active:             map[string]bool{},
		paneMisses:         map[string]int{},
		recentlyEndedPanes: map[string]int64{},
		missThreshold:      defaultMissThreshold,
		isPidAlive:         defaultIsPidAlive,
		now:                func() int64 { return time.Now().UnixMilli() },
	}
	for _, o := range opts {
		o(t)
	}
	return t
}

// defaultIsPidAlive is the signal-0 probe (kill(pid, 0)).
func defaultIsPidAlive(pid int) bool {
	return pid > 1 && syscall.Kill(pid, 0) == nil
}

func unseenKey(session, key string) string { return session + "\x00" + key }

func endSuppressKey(session, agent, paneID string) string {
	return session + "\x00" + agent + "\x00" + paneID
}

func (t *Tracker) isPaneEndSuppressed(session, agent, paneID string) bool {
	k := endSuppressKey(session, agent, paneID)
	exp, ok := t.recentlyEndedPanes[k]
	if !ok {
		return false
	}
	if t.now() >= exp {
		delete(t.recentlyEndedPanes, k)
		return false
	}
	return true
}

func (t *Tracker) clearMissState(session, key string) {
	delete(t.paneMisses, unseenKey(session, key))
}

func (t *Tracker) deleteInstance(session, key string) {
	if si := t.instances[session]; si != nil {
		delete(si, key)
		if len(si) == 0 {
			delete(t.instances, session)
		}
	}
	delete(t.unseen, unseenKey(session, key))
	t.clearMissState(session, key)
}

// ApplyEvent folds one watcher event in (tracker.ts applyEvent). seed=true
// marks terminal/waiting statuses unseen unconditionally (pre-connection
// state from the cold-start scan).
func (t *Tracker) ApplyEvent(event wire.AgentEvent, seed bool) {
	key := InstanceKey(event.Agent, event.ThreadID)

	// Definitive end: remove now; suppress ghost synthetics for the pane.
	if event.Ended {
		if si := t.instances[event.Session]; si != nil {
			if removed := si[key]; removed != nil && removed.PaneID != "" {
				t.recentlyEndedPanes[endSuppressKey(event.Session, event.Agent, removed.PaneID)] =
					t.now() + recentEndSuppressMS
			}
			t.deleteInstance(event.Session, key)
		}
		return
	}

	si := t.instances[event.Session]
	if si == nil {
		si = map[string]*wire.AgentEvent{}
		t.instances[event.Session] = si
	}

	// Preserve prior pane enrichment, pid, title, subagent, firstSeen.
	prev := si[key]
	if prev != nil && prev.PaneID != "" {
		if event.PaneID == "" {
			event.PaneID = prev.PaneID
		}
		if event.Liveness == "" {
			event.Liveness = prev.Liveness
		}
		if event.WindowIndex == nil {
			event.WindowIndex = prev.WindowIndex
		}
		if event.PaneIndex == nil {
			event.PaneIndex = prev.PaneIndex
		}
	}
	if prev != nil {
		if event.PID == 0 {
			event.PID = prev.PID
		}
		if event.PaneTitle == "" {
			event.PaneTitle = prev.PaneTitle
		}
		if event.Subagent == "" {
			event.Subagent = prev.Subagent
		}
		if event.ThreadName == "" {
			// A scan-stamped registry name must survive hook events that
			// carry no name of their own.
			event.ThreadName = prev.ThreadName
		}
		if prev.FirstSeenTS != 0 {
			event.FirstSeenTS = prev.FirstSeenTS
		}
	}
	if event.FirstSeenTS == 0 {
		event.FirstSeenTS = event.TS
	}
	stored := event
	// ToolInvoked is edge semantics ("this event STARTS a tool call") and
	// must not persist as level state in snapshots — like Ended (consumed
	// above) and Unseen (side map), it never survives the store.
	stored.ToolInvoked = false
	si[key] = &stored

	// One live process hosts one thread row: an event with a known pid
	// supersedes every other row holding that pid — synthetics awaiting
	// graduation, and stale thread keys left behind when the process
	// swapped session ids in place (/clear). Those stale rows never take
	// pane misses (their pane still exists), so this is their only exit.
	// Adopt the pane binding so enrichment survives the swap.
	graduated := false
	if stored.PID != 0 {
		for k, ev := range si {
			if k == key || ev.Agent != stored.Agent || ev.PID != stored.PID {
				continue
			}
			adoptPane(&stored, ev)
			// k != key, so the delete can't empty the session map.
			t.deleteInstance(stored.Session, k)
			graduated = true
		}
	}
	// Pid-less matching graduates at most one synthetic: paneId /
	// first-with-paneId fallback (tracker.ts graduation comments apply).
	graduateKey := ""
	if !graduated {
		for k, ev := range si {
			if k == key || ev.Agent != stored.Agent || !isSyntheticKey(k) {
				continue
			}
			if ev.PaneID != "" && stored.PaneID == "" {
				// Never adopt a synthetic whose pid disagrees.
				if stored.PID != 0 && ev.PID != 0 && ev.PID != stored.PID {
					continue
				}
				adoptPane(&stored, ev)
				stored.Liveness = ev.Liveness
				graduateKey = k
				break
			}
			if stored.PaneID != "" && ev.PaneID == stored.PaneID {
				graduateKey = k
				break
			}
		}
	}
	if graduateKey != "" {
		// graduateKey != key (the scan skips our own key), so the session
		// map always retains this event and can't be emptied by the delete.
		t.deleteInstance(stored.Session, graduateKey)
	}

	// Event timestamps (sparkline).
	ts := append(t.eventTimestamps[event.Session], event.TS)
	if len(ts) > maxEventTimestamps {
		ts = ts[len(ts)-maxEventTimestamps:]
	}
	t.eventTimestamps[event.Session] = ts

	// Unseen policy: terminal/waiting in an inactive (or seeded) session
	// is unseen; anything else marks the instance seen.
	ukey := unseenKey(event.Session, key)
	if wire.IsTerminalStatus(event.Status) || event.Status == wire.StatusWaiting {
		if seed || !t.active[event.Session] {
			t.unseen[ukey] = true
		}
	} else {
		delete(t.unseen, ukey)
	}
}

func adoptPane(dst *wire.AgentEvent, src *wire.AgentEvent) {
	if dst.PaneID == "" {
		dst.PaneID = src.PaneID
	}
	if dst.Liveness == "" {
		dst.Liveness = src.Liveness
	}
	if dst.WindowIndex == nil {
		dst.WindowIndex = src.WindowIndex
	}
	if dst.PaneIndex == nil {
		dst.PaneIndex = src.PaneIndex
	}
	if dst.PaneTitle == "" {
		dst.PaneTitle = src.PaneTitle
	}
}

// GetState returns the highest-priority instance (ties: newest ts, then
// newest firstSeenTs) — SessionData.agentState.
func (t *Tracker) GetState(session string) *wire.AgentEvent {
	si := t.instances[session]
	if len(si) == 0 {
		return nil
	}
	var best *wire.AgentEvent
	bestPriority := -1
	for _, ev := range si {
		p := statusPriority[ev.Status]
		switch {
		case p > bestPriority:
			bestPriority = p
			best = ev
		case p == bestPriority && best != nil:
			if ev.TS > best.TS || (ev.TS == best.TS && ev.FirstSeenTS > best.FirstSeenTS) {
				best = ev
			}
		}
	}
	if best == nil {
		return nil
	}
	out := *best
	return &out
}

// GetEvent is the O(1) single-instance lookup by (agent, threadId) or
// (agent, paneId).
func (t *Tracker) GetEvent(session, agent, threadID, paneID string) *wire.AgentEvent {
	si := t.instances[session]
	if si == nil {
		return nil
	}
	var hit *wire.AgentEvent
	if threadID != "" {
		hit = si[InstanceKey(agent, threadID)]
	} else if paneID != "" {
		hit = si[syntheticKey(agent, paneID)]
	}
	if hit == nil {
		return nil
	}
	out := *hit
	return &out
}

// GetAgents returns all instances for a session, unseen-stamped, sorted
// by window index, pane index, then firstSeenTs (unresolved sorts last).
func (t *Tracker) GetAgents(session string) []wire.AgentEvent {
	si := t.instances[session]
	out := make([]wire.AgentEvent, 0, len(si))
	for key, ev := range si {
		e := *ev
		if t.unseen[unseenKey(session, key)] {
			e.Unseen = true
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := idxOrMax(out[i].WindowIndex), idxOrMax(out[j].WindowIndex)
		if wi != wj {
			return wi < wj
		}
		pi, pj := idxOrMax(out[i].PaneIndex), idxOrMax(out[j].PaneIndex)
		if pi != pj {
			return pi < pj
		}
		return firstSeen(out[i]) < firstSeen(out[j])
	})
	return out
}

func idxOrMax(p *int) int {
	if p == nil {
		return int(^uint(0) >> 1)
	}
	return *p
}

func firstSeen(e wire.AgentEvent) int64 {
	if e.FirstSeenTS != 0 {
		return e.FirstSeenTS
	}
	return e.TS
}

// GetEventTimestamps returns the sparkline timestamps.
func (t *Tracker) GetEventTimestamps(session string) []int64 {
	ts := t.eventTimestamps[session]
	if ts == nil {
		return []int64{}
	}
	return ts
}

// IsUnseen reports whether any instance in the session is unseen.
func (t *Tracker) IsUnseen(session string) bool {
	for key := range t.instances[session] {
		if t.unseen[unseenKey(session, key)] {
			return true
		}
	}
	return false
}

// MarkSeen clears unseen flags for a session; reports whether any were set.
func (t *Tracker) MarkSeen(session string) bool {
	if !t.IsUnseen(session) {
		return false
	}
	for key := range t.instances[session] {
		delete(t.unseen, unseenKey(session, key))
	}
	return true
}

// HandleFocus marks the session active and clears its unseen flags;
// reports whether any were set (caller broadcasts on true).
func (t *Tracker) HandleFocus(session string) bool {
	t.active = map[string]bool{session: true}
	return t.MarkSeen(session)
}

// SetActiveSessions replaces the active-session set.
func (t *Tracker) SetActiveSessions(sessions []string) {
	t.active = map[string]bool{}
	for _, s := range sessions {
		t.active[s] = true
	}
}

// Dismiss removes the first instance matching every supplied identifier
// (threadID/paneID "" and pid 0 = wildcard).
func (t *Tracker) Dismiss(session, agent, threadID, paneID string, pid int) bool {
	si := t.instances[session]
	if si == nil {
		return false
	}
	for key, ev := range si {
		if ev.Agent != agent {
			continue
		}
		if threadID != "" && ev.ThreadID != threadID {
			continue
		}
		if paneID != "" && ev.PaneID != paneID {
			continue
		}
		if pid != 0 && ev.PID != pid {
			continue
		}
		t.deleteInstance(session, key)
		return true
	}
	return false
}

// PruneStuck removes stale running entries; alive entries get the
// 30-minute ceiling (see tracker.ts pruneStuck).
func (t *Tracker) PruneStuck(timeoutMS int64) {
	now := t.now()
	for session, si := range t.instances {
		for key, ev := range si {
			if ev.Status != wire.StatusRunning {
				continue
			}
			age := now - ev.TS
			if age <= timeoutMS {
				continue
			}
			if ev.Liveness == wire.LivenessAlive && age <= AlivePruneCeilingMS {
				continue
			}
			t.deleteInstance(session, key)
		}
	}
}

// ReconcileStaleRunning asks the probe about stale alive running entries
// with a pid; "ended" flips them to done (visible change → returns true),
// "working" resets the staleness clock silently.
func (t *Tracker) ReconcileStaleRunning(staleMS int64, probe func(wire.AgentEvent) ProbeVerdict) bool {
	now := t.now()
	changed := false
	for _, si := range t.instances {
		for _, ev := range si {
			if ev.Status != wire.StatusRunning || ev.Liveness != wire.LivenessAlive || ev.PID == 0 {
				continue
			}
			if now-ev.TS <= staleMS {
				continue
			}
			switch probe(*ev) {
			case ProbeEnded:
				ev.Status = wire.StatusDone
				ev.TS = now
				changed = true
			case ProbeWorking:
				ev.TS = now
			}
		}
	}
	return changed
}

// PruneTerminal removes exited instances immediately — a dead process has
// no pane to navigate to, so the row is a dead click target the moment the
// exit is confirmed. Running is exempt (PruneStuck owns it); unknown
// liveness is exempt (no scan has confirmed the process is gone).
func (t *Tracker) PruneTerminal() {
	for session, si := range t.instances {
		for key, ev := range si {
			if ev.Liveness != wire.LivenessExited || ev.Status == wire.StatusRunning {
				continue
			}
			t.deleteInstance(session, key)
		}
	}
}

// RunLivenessSweepOnce marks non-terminal instances with a dead pid as
// exited; reports whether anything changed.
func (t *Tracker) RunLivenessSweepOnce() bool {
	changed := false
	for _, si := range t.instances {
		for _, ev := range si {
			if ev.PID == 0 || ev.Liveness == wire.LivenessExited || wire.IsTerminalStatus(ev.Status) {
				continue
			}
			if !t.isPidAlive(ev.PID) {
				ev.Liveness = wire.LivenessExited
				changed = true
			}
		}
	}
	return changed
}

// ApplyPanePresence folds one session's pane scan into the tracker; see
// tracker.ts applyPanePresence for the three-step contract (miss counting
// with spare-pane rebinds, claim/stamp with PID disambiguation, seed-ghost
// exit marking). Returns true when anything visible changed.
func (t *Tracker) ApplyPanePresence(session string, paneAgents []PanePresence) bool {
	changed := false
	si := t.instances[session]

	activePaneIDs := map[string]bool{}
	for _, pa := range paneAgents {
		activePaneIDs[pa.PaneID] = true
	}

	// Spare panes per agent: incoming panes not bound to an alive entry.
	sparePanes := map[string]int{}
	if si != nil {
		bound := map[string]bool{}
		for _, ev := range si {
			if ev.Liveness == wire.LivenessAlive && ev.PaneID != "" && activePaneIDs[ev.PaneID] {
				bound[ev.PaneID] = true
			}
		}
		for _, pa := range paneAgents {
			if !bound[pa.PaneID] {
				sparePanes[pa.Agent]++
			}
		}
	}

	// 1. Previously-alive entries whose pane disappeared.
	if si != nil {
		for key, ev := range si {
			if ev.Liveness != wire.LivenessAlive || ev.PaneID == "" || activePaneIDs[ev.PaneID] {
				continue
			}
			if sparePanes[ev.Agent] > 0 {
				sparePanes[ev.Agent]--
				t.clearMissState(session, key)
				continue
			}
			ukey := unseenKey(session, key)
			misses := t.paneMisses[ukey] + 1
			if misses < t.missThreshold {
				t.paneMisses[ukey] = misses
				continue
			}
			delete(t.paneMisses, ukey)
			if isSyntheticKey(key) {
				t.deleteInstance(session, key)
			} else {
				ev.Liveness = wire.LivenessExited
				ev.PaneID = ""
			}
			changed = true
		}
	}

	// 2. Stamp pane info or mint synthetics, claiming watcher entries by PID.
	claimed := map[string]bool{}
	for _, pa := range paneAgents {
		if si == nil {
			si = map[string]*wire.AgentEvent{}
			t.instances[session] = si
		}

		// Thread identity outranks a pid match: after an in-place session-id
		// swap (/clear) two rows can hold the same pid, and Go's randomized
		// map order would let a bare pid match claim the stale one.
		threadKey := ""
		if pa.ThreadID != "" {
			threadKey = InstanceKey(pa.Agent, pa.ThreadID)
		}
		var bestKey, pidKey, fallbackKey string
		var bestEv, pidEv, fallbackEv *wire.AgentEvent
		for k, ev := range si {
			if ev.Agent != pa.Agent || claimed[k] || ev.Liveness == wire.LivenessExited || isSyntheticKey(k) {
				continue
			}
			if k == threadKey {
				bestKey, bestEv = k, ev
				break
			}
			if pa.PID != 0 && ev.PID == pa.PID {
				// A pid match carrying a different resolved thread id is the
				// superseded identity itself — don't stamp fresh pane data
				// onto it; the supersede sweep below retires it.
				if threadKey != "" && ev.ThreadID != "" && ev.ThreadID != pa.ThreadID {
					continue
				}
				if pidEv == nil {
					pidKey, pidEv = k, ev
				}
				continue
			}
			// Fallback only for entries with no pid yet (cold-boot watcher);
			// a different pid is a different process. Among fallback
			// candidates, prefer the entry already bound to this pane: the
			// TS Map's insertion order happened to keep pane bindings
			// stable, but Go map iteration is randomized, and rebinding a
			// different entry here would crisscross panes and reset the
			// wrong instance's miss counter.
			if ev.PID == 0 {
				if ev.PaneID == pa.PaneID {
					fallbackKey, fallbackEv = k, ev
				} else if fallbackEv == nil {
					fallbackKey, fallbackEv = k, ev
				}
			}
		}
		if bestEv == nil {
			bestKey, bestEv = pidKey, pidEv
		}
		if bestEv == nil {
			bestKey, bestEv = fallbackKey, fallbackEv
		}

		if bestEv != nil {
			claimed[bestKey] = true
			wasDifferent := bestEv.PaneID != pa.PaneID ||
				bestEv.Liveness != wire.LivenessAlive ||
				!intPtrEq(bestEv.WindowIndex, pa.WindowIndex) ||
				!intPtrEq(bestEv.PaneIndex, pa.PaneIndex) ||
				(pa.ThreadName != "" && bestEv.ThreadName != pa.ThreadName)
			bestEv.PaneID = pa.PaneID
			bestEv.Liveness = wire.LivenessAlive
			bestEv.WindowIndex = pa.WindowIndex
			bestEv.PaneIndex = pa.PaneIndex
			bestEv.PaneTitle = pa.PaneTitle // never drives a broadcast
			if pa.ThreadName != "" {
				// Registry name is the user-facing one (renamable live);
				// it outranks the transcript-derived fallback.
				bestEv.ThreadName = pa.ThreadName
			}
			t.clearMissState(session, bestKey)
			if t.retireSupersededPidRows(session, pa.Agent, bestKey, pa.PID) {
				changed = true
			}
			if wasDifferent {
				changed = true
			}
			continue
		}

		if t.isPaneEndSuppressed(session, pa.Agent, pa.PaneID) {
			continue
		}

		// Mint under the real thread key when the scan resolved one: the
		// row carries its thread identity from birth, and later hook
		// events for the same thread merge instead of graduating. Refuse
		// the thread key only when it's held by a live different process
		// (a thread id can't be in two processes; fall back to synthetic).
		sk := syntheticKey(pa.Agent, pa.PaneID)
		threadID := ""
		if pa.ThreadID != "" {
			tk := InstanceKey(pa.Agent, pa.ThreadID)
			held, ok := si[tk]
			if !ok || held.PID == 0 || held.PID == pa.PID || held.Liveness != wire.LivenessAlive {
				sk, threadID = tk, pa.ThreadID
			}
		}
		threadName := ""
		if threadID != "" {
			threadName = pa.ThreadName
		}
		if existing, ok := si[sk]; !ok {
			si[sk] = &wire.AgentEvent{
				Agent:       pa.Agent,
				Session:     session,
				Status:      wire.StatusIdle,
				TS:          t.now(),
				ThreadID:    threadID,
				ThreadName:  threadName,
				PaneID:      pa.PaneID,
				Liveness:    wire.LivenessAlive,
				WindowIndex: pa.WindowIndex,
				PaneIndex:   pa.PaneIndex,
				PID:         pa.PID,
				PaneTitle:   pa.PaneTitle,
			}
			changed = true
		} else {
			wasDifferent := existing.PaneID != pa.PaneID ||
				existing.Liveness != wire.LivenessAlive ||
				!intPtrEq(existing.WindowIndex, pa.WindowIndex) ||
				!intPtrEq(existing.PaneIndex, pa.PaneIndex) ||
				existing.PID != pa.PID ||
				(threadName != "" && existing.ThreadName != threadName)
			existing.PaneID = pa.PaneID
			existing.Liveness = wire.LivenessAlive
			existing.WindowIndex = pa.WindowIndex
			existing.PaneIndex = pa.PaneIndex
			existing.PID = pa.PID
			existing.PaneTitle = pa.PaneTitle
			if threadName != "" {
				existing.ThreadName = threadName
			}
			t.clearMissState(session, sk)
			if wasDifferent {
				changed = true
			}
		}
		if t.retireSupersededPidRows(session, pa.Agent, sk, pa.PID) {
			changed = true
		}
	}

	// 3. Unclaimed seed ghosts (liveness unset, terminal-ish status) are
	// dead processes: mark exited so pruneTerminal can act.
	if si != nil {
		for key, ev := range si {
			if claimed[key] || isSyntheticKey(key) || ev.Liveness != "" {
				continue
			}
			if !wire.IsTerminalStatus(ev.Status) && ev.Status != wire.StatusIdle && ev.Status != wire.StatusWaiting {
				continue
			}
			ev.Liveness = wire.LivenessExited
			changed = true
		}
	}

	return changed
}

// retireSupersededPidRows enforces one-process-one-row after a pane scan
// resolves a pid to keepKey: any other same-agent row holding that pid is
// a stale identity (the process swapped session ids in place — /clear —
// and the old row never takes pane misses because its pane still exists).
// Reports whether anything was deleted.
func (t *Tracker) retireSupersededPidRows(session, agent, keepKey string, pid int) bool {
	if pid == 0 {
		return false
	}
	changed := false
	for k, ev := range t.instances[session] {
		if k == keepKey || ev.Agent != agent || ev.PID != pid {
			continue
		}
		// keepKey exists in the map, so the delete can't empty the session.
		t.deleteInstance(session, k)
		changed = true
	}
	return changed
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
