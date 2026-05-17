import type { AgentEvent, PanePresenceInput } from "../contracts/agent";
import { TERMINAL_STATUSES } from "../contracts/agent";

const MAX_EVENT_TIMESTAMPS = 30;
const TERMINAL_PRUNE_MS = 5 * 60 * 1000;
/** Window after a watcher signals SessionEnd during which the pane scanner
 *  must NOT mint a synthetic for that pane/agent. SessionEnd fires while the
 *  agent process is still wrapping up — ps shows it for a beat after — so
 *  without this gate the row reappears as a ghost synthetic for ~5s. */
const RECENT_END_SUPPRESS_MS = 5_000;

// Pane scans (server/index.ts:1205) run every 3s. A single missed scan can
// happen when the agent process re-execs (Claude Code compaction, codex
// sandbox spawn) and the tree match transiently fails. Requiring N consecutive
// misses before transitioning alive→exited absorbs the false negative without
// noticeably delaying real exits.
const DEFAULT_MISS_THRESHOLD = 2;

const STATUS_PRIORITY: Record<string, number> = {
  running: 5,
  error: 4,
  interrupted: 3,
  waiting: 2,
  done: 1,
  idle: 0,
};

export function instanceKey(agent: string, threadId?: string): string {
  return threadId ? `${agent}:${threadId}` : agent;
}

/** Real `process.kill(pid, 0)` — sends signal 0 to probe for existence
 *  without affecting the target. Returns false when the pid is gone or
 *  unreachable (ESRCH / EPERM / any other error). */
function defaultIsPidAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 1) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

export class AgentTracker {
  // Outer key: session name, inner key: instance key (agent or agent:threadId)
  private instances = new Map<string, Map<string, AgentEvent>>();
  private eventTimestamps = new Map<string, number[]>();
  // Per-instance unseen tracking: "session\0instanceKey"
  private unseenInstances = new Set<string>();
  private active = new Set<string>();
  // Per-instance pane-scan miss counter, keyed "session\0instanceKey".
  // An entry exists only while a previously-alive agent is missing from the
  // current scan but hasn't yet crossed the threshold. Cleared on every rebind
  // and on every delete path so it can't leak.
  private paneMisses = new Map<string, number>();
  private missThreshold: number;
  private livenessTimer: ReturnType<typeof setInterval> | null = null;
  /** Injected for tests so we can simulate a dead pid without forking. */
  private isPidAlive: (pid: number) => boolean;
  /** Injected for tests so suppression windows can be advanced without setTimeout. */
  private now: () => number;
  /** paneId-keyed suppression: skip creating synthetics for a pane that the
   *  watcher just removed via SessionEnd. Value is the expiration timestamp.
   *  Key shape: `${session}\0${agent}\0${paneId}`. */
  private recentlyEndedPanes = new Map<string, number>();

  constructor(opts: { missThreshold?: number; isPidAlive?: (pid: number) => boolean; now?: () => number } = {}) {
    this.missThreshold = opts.missThreshold ?? DEFAULT_MISS_THRESHOLD;
    this.isPidAlive = opts.isPidAlive ?? defaultIsPidAlive;
    this.now = opts.now ?? (() => Date.now());
  }

  private endSuppressKey(session: string, agent: string, paneId: string): string {
    return `${session}\0${agent}\0${paneId}`;
  }

  /** Return true if a synthetic for this pane/agent should be suppressed
   *  because the watcher recently ended an entry here. Lazily expires. */
  private isPaneEndSuppressed(session: string, agent: string, paneId: string): boolean {
    const k = this.endSuppressKey(session, agent, paneId);
    const exp = this.recentlyEndedPanes.get(k);
    if (exp === undefined) return false;
    if (this.now() >= exp) {
      this.recentlyEndedPanes.delete(k);
      return false;
    }
    return true;
  }

  private unseenKey(session: string, key: string): string {
    return `${session}\0${key}`;
  }

  /** Single helper to keep miss-state cleanup co-located with every delete path. */
  private clearMissState(session: string, key: string): void {
    this.paneMisses.delete(this.unseenKey(session, key));
  }

  applyEvent(event: AgentEvent, options?: { seed?: boolean }): void {
    const key = instanceKey(event.agent, event.threadId);

    // Watcher signalled the agent session is definitively ended — remove now
    // rather than waiting for the terminal-prune window. Covers /exit, Ctrl+C,
    // and any other clean shutdown where a SessionEnd-equivalent fires.
    if (event.ended) {
      const sessionInstances = this.instances.get(event.session);
      if (sessionInstances) {
        // Stash the paneId before deletion so the pane scanner can suppress
        // a transient synthetic during the agent's exit-cleanup window.
        const removed = sessionInstances.get(key);
        if (removed?.paneId) {
          this.recentlyEndedPanes.set(
            this.endSuppressKey(event.session, event.agent, removed.paneId),
            this.now() + RECENT_END_SUPPRESS_MS,
          );
        }
        sessionInstances.delete(key);
        this.unseenInstances.delete(this.unseenKey(event.session, key));
        this.clearMissState(event.session, key);
        if (sessionInstances.size === 0) {
          this.instances.delete(event.session);
        }
      }
      return;
    }

    // Store instance
    let sessionInstances = this.instances.get(event.session);
    if (!sessionInstances) {
      sessionInstances = new Map();
      this.instances.set(event.session, sessionInstances);
    }
    // Preserve pane info from prior enrichment by applyPanePresence
    const prev = sessionInstances.get(key);
    if (prev?.paneId) {
      event.paneId = event.paneId ?? prev.paneId;
      event.liveness = event.liveness ?? prev.liveness;
      event.windowIndex = event.windowIndex ?? prev.windowIndex;
      event.paneIndex = event.paneIndex ?? prev.paneIndex;
    }
    // Preserve subagent across PostToolUse-style events that don't re-read sessions/
    if (event.subagent === undefined && prev?.subagent !== undefined) {
      event.subagent = prev.subagent;
    }
    // Stamp first-seen timestamp once per instance so getAgents() sort is
    // stable across subsequent status updates.
    event.firstSeenTs = prev?.firstSeenTs ?? event.ts;
    sessionInstances.set(key, event);

    // Graduate at most ONE synthetic pane-keyed entry — the one that represents
    // this watcher's pane. Synthetics for OTHER panes belong to other live
    // claude processes in the same tmux session (e.g. rb-orchestrator,
    // rb-planner) and must stay until those processes fire their own hooks.
    // Deleting them indiscriminately produced ~3s flicker as the pane scanner
    // re-created them on every cycle.
    //
    // Prefer matching by PID — synthetics carry the scanner's pid, this event
    // carries the watcher's pid, both resolve from the same OS process. PID
    // match disambiguates when several synthetics share an agent name.
    // Fall back to paneId match (when we already adopted one), then to
    // first-with-paneId (cold-boot watcher with no pane info yet).
    let graduateKey: string | undefined;
    if (event.pid !== undefined) {
      for (const [k, ev] of sessionInstances) {
        if (k === key) continue;
        if (ev.agent !== event.agent) continue;
        if (!k.includes(":pane:")) continue;
        if (ev.pid !== event.pid) continue;
        event.paneId = event.paneId ?? ev.paneId;
        event.liveness = event.liveness ?? ev.liveness;
        event.windowIndex = event.windowIndex ?? ev.windowIndex;
        event.paneIndex = event.paneIndex ?? ev.paneIndex;
        graduateKey = k;
        break;
      }
    }
    if (graduateKey === undefined) {
      for (const [k, ev] of sessionInstances) {
        if (k === key) continue;
        if (ev.agent !== event.agent) continue;
        if (!k.includes(":pane:")) continue;
        // Two graduation cases:
        //   1. We have no paneId yet — adopt this synthetic's pane and graduate it.
        //   2. We already have a paneId — graduate the synthetic whose pane matches.
        if (ev.paneId && !event.paneId) {
          // Don't adopt a synthetic whose pid disagrees with this event's pid —
          // it belongs to a different process.
          if (event.pid !== undefined && ev.pid !== undefined && ev.pid !== event.pid) continue;
          event.paneId = ev.paneId;
          event.liveness = ev.liveness;
          event.windowIndex = event.windowIndex ?? ev.windowIndex;
          event.paneIndex = event.paneIndex ?? ev.paneIndex;
          graduateKey = k;
          break;
        }
        if (event.paneId && ev.paneId === event.paneId) {
          graduateKey = k;
          break;
        }
      }
    }
    if (graduateKey !== undefined) {
      sessionInstances.delete(graduateKey);
      this.unseenInstances.delete(this.unseenKey(event.session, graduateKey));
      this.clearMissState(event.session, graduateKey);
    }

    // Track event timestamps
    let timestamps = this.eventTimestamps.get(event.session);
    if (!timestamps) {
      timestamps = [];
      this.eventTimestamps.set(event.session, timestamps);
    }
    timestamps.push(event.ts);
    if (timestamps.length > MAX_EVENT_TIMESTAMPS) {
      timestamps.splice(0, timestamps.length - MAX_EVENT_TIMESTAMPS);
    }

    // Per-instance unseen tracking
    // Seeded events always mark as unseen (they represent state from before the user connected)
    const ukey = this.unseenKey(event.session, key);
    if (TERMINAL_STATUSES.has(event.status) || event.status === "waiting") {
      if (options?.seed || !this.active.has(event.session)) {
        this.unseenInstances.add(ukey);
      }
    } else {
      // Non-terminal/non-waiting status for this instance = user is interacting, mark seen
      this.unseenInstances.delete(ukey);
    }
  }

  /** Returns the most important agent state for backward compat */
  getState(session: string): AgentEvent | null {
    const sessionInstances = this.instances.get(session);
    if (!sessionInstances || sessionInstances.size === 0) return null;

    let best: AgentEvent | null = null;
    let bestPriority = -1;
    for (const event of sessionInstances.values()) {
      const p = STATUS_PRIORITY[event.status] ?? 0;
      if (p > bestPriority) {
        bestPriority = p;
        best = event;
      }
    }
    return best;
  }

  /** Returns all agent instances for a session, with unseen flag stamped */
  getAgents(session: string): AgentEvent[] {
    const sessionInstances = this.instances.get(session);
    if (!sessionInstances) return [];
    return [...sessionInstances.values()]
      .map((event) => {
        const key = instanceKey(event.agent, event.threadId);
        const isUnseen = this.unseenInstances.has(this.unseenKey(session, key));
        return isUnseen ? { ...event, unseen: true } : event;
      })
      .sort((a, b) => {
        // Primary: tmux window index so rows align with the status-bar tabs.
        // Secondary: pane index within a window for stable ordering when
        // multiple agents share a window. Tertiary: firstSeenTs keeps
        // watcher-only rows (no pane info yet) and synthetics in arrival
        // order. Unresolved values sort last via Infinity.
        const wA = a.windowIndex ?? Infinity;
        const wB = b.windowIndex ?? Infinity;
        if (wA !== wB) return wA - wB;
        const pA = a.paneIndex ?? Infinity;
        const pB = b.paneIndex ?? Infinity;
        if (pA !== pB) return pA - pB;
        return (a.firstSeenTs ?? a.ts) - (b.firstSeenTs ?? b.ts);
      });
  }

  /** Returns recent event timestamps for sparkline rendering */
  getEventTimestamps(session: string): number[] {
    return this.eventTimestamps.get(session) ?? [];
  }

  markSeen(session: string): boolean {
    const hadUnseen = this.isUnseen(session);
    if (!hadUnseen) return false;

    // Clear unseen flags for all instances — keep the instances themselves
    // (pruneTerminal will remove seen terminal instances after timeout)
    const sessionInstances = this.instances.get(session);
    if (sessionInstances) {
      for (const key of sessionInstances.keys()) {
        this.unseenInstances.delete(this.unseenKey(session, key));
      }
    }
    return true;
  }

  dismiss(session: string, agent: string, threadId?: string): boolean {
    const sessionInstances = this.instances.get(session);
    if (!sessionInstances) return false;

    const key = instanceKey(agent, threadId);
    const removed = sessionInstances.delete(key);
    if (!removed) return false;

    this.unseenInstances.delete(this.unseenKey(session, key));
    this.clearMissState(session, key);
    if (sessionInstances.size === 0) {
      this.instances.delete(session);
    }
    return true;
  }

  pruneStuck(timeoutMs: number): void {
    const now = Date.now();
    for (const [session, sessionInstances] of this.instances) {
      for (const [key, event] of sessionInstances) {
        if (event.status === "running" && now - event.ts > timeoutMs) {
          if (event.liveness === "alive") continue;
          sessionInstances.delete(key);
          this.unseenInstances.delete(this.unseenKey(session, key));
          this.clearMissState(session, key);
        }
      }
      if (sessionInstances.size === 0) {
        this.instances.delete(session);
      }
    }
  }

  /** Auto-prune terminal instances older than timeout, but only if instance is not unseen or alive */
  pruneTerminal(): void {
    const now = Date.now();
    for (const [session, sessionInstances] of this.instances) {
      for (const [key, event] of sessionInstances) {
        if (!TERMINAL_STATUSES.has(event.status)) continue;
        const ukey = this.unseenKey(session, key);
        if (this.unseenInstances.has(ukey)) continue; // Don't prune unseen — user hasn't looked yet
        if (event.liveness !== "exited") continue; // Only prune when we know the pane is gone
        if (now - event.ts > TERMINAL_PRUNE_MS) {
          sessionInstances.delete(key);
          this.clearMissState(session, key);
        }
      }
      if (sessionInstances.size === 0) {
        this.instances.delete(session);
      }
    }
  }

  /**
   * Run one liveness sweep: for every tracked instance with a `pid`, check
   * whether the process is still alive. If not, mark `liveness: "exited"`
   * so the rest of the system (pane miss counter, terminal prune) can act.
   *
   * Exposed for tests; the timer in `startLivenessCheck` just calls this on
   * a 5s interval. Returns true if anything changed.
   */
  runLivenessSweepOnce(): boolean {
    let changed = false;
    for (const sessionInstances of this.instances.values()) {
      for (const event of sessionInstances.values()) {
        if (event.pid == null) continue;
        if (event.liveness === "exited") continue;
        if (TERMINAL_STATUSES.has(event.status)) continue;
        if (!this.isPidAlive(event.pid)) {
          event.liveness = "exited";
          changed = true;
        }
      }
    }
    return changed;
  }

  /** Start the periodic liveness check. Caller is responsible for stopLivenessCheck(). */
  startLivenessCheck(intervalMs = 5_000): void {
    if (this.livenessTimer != null) return;
    this.livenessTimer = setInterval(() => this.runLivenessSweepOnce(), intervalMs);
  }

  /** Stop the periodic liveness check. Safe to call when not started. */
  stopLivenessCheck(): void {
    if (this.livenessTimer == null) return;
    clearInterval(this.livenessTimer);
    this.livenessTimer = null;
  }

  isUnseen(session: string): boolean {
    // Session is unseen if any instance within it is unseen
    const sessionInstances = this.instances.get(session);
    if (!sessionInstances) return false;
    for (const key of sessionInstances.keys()) {
      if (this.unseenInstances.has(this.unseenKey(session, key))) return true;
    }
    return false;
  }

  getUnseen(): string[] {
    // Derive session-level unseen from per-instance tracking
    const sessions = new Set<string>();
    for (const ukey of this.unseenInstances) {
      sessions.add(ukey.split("\0")[0]!);
    }
    return [...sessions];
  }

  handleFocus(session: string): boolean {
    this.active.clear();
    this.active.add(session);

    const hadUnseen = this.isUnseen(session);
    if (hadUnseen) {
      // Clear unseen flags — keep terminal instances visible (as "seen")
      // pruneTerminal will clean them up after timeout
      const sessionInstances = this.instances.get(session);
      if (sessionInstances) {
        for (const key of sessionInstances.keys()) {
          this.unseenInstances.delete(this.unseenKey(session, key));
        }
      }
    }
    return hadUnseen;
  }

  setActiveSessions(sessions: string[]): void {
    this.active.clear();
    for (const s of sessions) this.active.add(s);
  }

  /** Fold pane scanner results into the tracker.
   *  The scanner only reports {agent, paneId} — no threadId, status, or threadName.
   *  Watchers are the single source of truth for those fields.
   *
   *  1. Entries with liveness "alive" whose paneId is missing from the scan are
   *     held alive on a miss-counter. Only after `missThreshold` consecutive
   *     missed scans do we transition them: synthetics get deleted, watcher
   *     entries get liveness="exited". This absorbs the false-negative scans
   *     that happen when an agent process re-execs (Claude Code compaction,
   *     codex sandbox spawn).
   *  2. Each pane agent: find existing entry for this agent → stamp paneId +
   *     liveness, clear miss counter. If none exists, create a minimal
   *     synthetic (status: "idle", liveness: "alive").
   *  Returns true if anything changed (caller uses this for broadcast decisions). */
  applyPanePresence(session: string, paneAgents: PanePresenceInput[]): boolean {
    let changed = false;
    let sessionInstances = this.instances.get(session);

    // Index incoming pane IDs for fast lookup.
    const activePaneIds = new Set<string>();
    for (const pa of paneAgents) activePaneIds.add(pa.paneId);

    // "Spare" panes per agent — panes in this scan whose paneId is NOT already
    // bound to an alive tracker entry. These are the only panes available for
    // pane-move rebinding in step 2. A missing-paneId entry can suppress its
    // miss only if it can plausibly consume a spare. With one alive instance
    // (T4 pane move) the new paneId is spare → suppress. With two instances
    // and one survivor, every incoming pane is bound to a survivor's old
    // paneId → spare count is zero → the exiting instance MUST accrue a miss.
    const sparePanesByAgent = new Map<string, number>();
    if (sessionInstances) {
      const boundPaneIds = new Set<string>();
      for (const ev of sessionInstances.values()) {
        if (ev.liveness === "alive" && ev.paneId && activePaneIds.has(ev.paneId)) {
          boundPaneIds.add(ev.paneId);
        }
      }
      for (const pa of paneAgents) {
        if (boundPaneIds.has(pa.paneId)) continue;
        sparePanesByAgent.set(pa.agent, (sparePanesByAgent.get(pa.agent) ?? 0) + 1);
      }
    }

    // 1. Handle previously-alive entries whose pane disappeared
    if (sessionInstances) {
      for (const [key, event] of sessionInstances) {
        if (event.liveness === "alive" && event.paneId && !activePaneIds.has(event.paneId)) {
          // Pane move? Consume one spare same-agent pane if available — step 2
          // will rebind us. With no spare, this entry truly missed.
          const spare = sparePanesByAgent.get(event.agent) ?? 0;
          if (spare > 0) {
            sparePanesByAgent.set(event.agent, spare - 1);
            this.clearMissState(session, key);
            continue;
          }
          // Bona fide miss — bump the counter.
          const ukey = this.unseenKey(session, key);
          const misses = (this.paneMisses.get(ukey) ?? 0) + 1;
          if (misses < this.missThreshold) {
            // Within grace — hold alive, do nothing visible. Counter persists
            // until next scan resolves the question.
            this.paneMisses.set(ukey, misses);
            continue;
          }
          // Threshold crossed — transition for real.
          this.paneMisses.delete(ukey);
          if (key.includes(":pane:")) {
            // Synthetic — remove entirely
            sessionInstances.delete(key);
            this.unseenInstances.delete(this.unseenKey(session, key));
          } else {
            // Watcher-sourced — keep entry, clear pane binding
            event.liveness = "exited";
            event.paneId = undefined;
          }
          changed = true;
        }
      }
      if (sessionInstances.size === 0) {
        this.instances.delete(session);
      }
    }

    // 2. Stamp pane info onto existing entries, or create minimal synthetics
    //    Track which watcher entries have already been claimed by a pane so that
    //    multiple panes running the same agent each match a distinct watcher entry.
    const claimedKeys = new Set<string>();

    for (const pa of paneAgents) {
      if (!sessionInstances) {
        sessionInstances = new Map();
        this.instances.set(session, sessionInstances);
      }

      // Find an unclaimed alive watcher-sourced entry for this agent.
      // Skip synthetics: each synthetic is pinned to a specific paneId in its
      // key, and claiming one for a different pane silently rebinds it,
      // producing entry-count drift when multiple panes share an agent.
      // Skip entries the liveness sweep marked dead — resurrecting them
      // flickers the row against the sweep at every broadcast.
      //
      // Disambiguate by PID when available. Watcher entries resolve pid via
      // ancestor walk on hook fire; the pane scanner resolves the same pid
      // via descendant walk of the pane's shell. When both agree, the claim
      // is unambiguous even with multiple panes sharing an agent name
      // (assistant + rb-orchestrator + rb-planner all "claude-code").
      // Without this guard, the loop fell back to Map iteration order and
      // silently crisscrossed watcher entries with the wrong panes.
      let bestKey: string | undefined;
      let bestEvent: AgentEvent | undefined;
      let fallbackKey: string | undefined;
      let fallbackEvent: AgentEvent | undefined;
      for (const [k, ev] of sessionInstances) {
        if (ev.agent !== pa.agent) continue;
        if (claimedKeys.has(k)) continue;
        if (ev.liveness === "exited") continue;
        if (k.includes(":pane:")) continue;
        if (pa.pid !== undefined && ev.pid === pa.pid) {
          // Strict PID match — done.
          bestKey = k;
          bestEvent = ev;
          break;
        }
        // Fallback only when the entry has no pid yet (cold-boot watcher).
        // An entry with a different pid belongs to a different process —
        // never claim it from this pane.
        if (ev.pid === undefined && fallbackEvent === undefined) {
          fallbackKey = k;
          fallbackEvent = ev;
        }
      }
      if (!bestEvent) {
        bestKey = fallbackKey;
        bestEvent = fallbackEvent;
      }

      if (bestEvent && bestKey) {
        claimedKeys.add(bestKey);
        const wasDifferent =
          bestEvent.paneId !== pa.paneId ||
          bestEvent.liveness !== "alive" ||
          bestEvent.windowIndex !== pa.windowIndex ||
          bestEvent.paneIndex !== pa.paneIndex;
        bestEvent.paneId = pa.paneId;
        bestEvent.liveness = "alive";
        bestEvent.windowIndex = pa.windowIndex;
        bestEvent.paneIndex = pa.paneIndex;
        // Resolved — any pending miss for this entry is no longer relevant.
        this.clearMissState(session, bestKey);
        if (wasDifferent) changed = true;

        continue;
      }

      // Pane just had a SessionEnd — ps still shows the agent for a beat
      // during exit cleanup. Don't mint a ghost synthetic for that window.
      if (this.isPaneEndSuppressed(session, pa.agent, pa.paneId)) {
        continue;
      }

      // No existing entry — create minimal synthetic
      const syntheticKey = `${pa.agent}:pane:${pa.paneId}`;

      if (!sessionInstances.has(syntheticKey)) {
        sessionInstances.set(syntheticKey, {
          agent: pa.agent,
          session,
          status: "idle",
          ts: this.now(),
          paneId: pa.paneId,
          liveness: "alive",
          windowIndex: pa.windowIndex,
          paneIndex: pa.paneIndex,
          pid: pa.pid,
        });
        changed = true;
      } else {
        const existing = sessionInstances.get(syntheticKey)!;
        const wasDifferent =
          existing.paneId !== pa.paneId ||
          existing.liveness !== "alive" ||
          existing.windowIndex !== pa.windowIndex ||
          existing.paneIndex !== pa.paneIndex ||
          existing.pid !== pa.pid;
        existing.paneId = pa.paneId;
        existing.liveness = "alive";
        existing.windowIndex = pa.windowIndex;
        existing.paneIndex = pa.paneIndex;
        existing.pid = pa.pid;
        this.clearMissState(session, syntheticKey);
        if (wasDifferent) changed = true;
      }
    }

    // 3. Mark unclaimed seed ghosts as exited.
    //    Entries from the cold-start seed have null liveness and no paneId.
    //    If the pane scanner ran, found panes for this session, but didn't claim
    //    a seed entry, then no running process corresponds to it.
    //    Only apply to terminal statuses (done/interrupted/idle) — a "running"
    //    entry may be mid-stream before the scanner has seen it.
    if (sessionInstances) {
      for (const [key, event] of sessionInstances) {
        if (claimedKeys.has(key)) continue;
        if (key.includes(":pane:")) continue; // Synthetics handled in step 1
        if (event.liveness !== null && event.liveness !== undefined) continue;
        if (!TERMINAL_STATUSES.has(event.status) && event.status !== "idle" && event.status !== "waiting") continue;
        event.liveness = "exited";
        changed = true;
      }
    }

    return changed;
  }
}
