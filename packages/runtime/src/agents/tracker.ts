import type { AgentEvent, PanePresenceInput } from "../contracts/agent";
import { TERMINAL_STATUSES } from "../contracts/agent";

const MAX_EVENT_TIMESTAMPS = 30;
const TERMINAL_PRUNE_MS = 5 * 60 * 1000;

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

export class AgentTracker {
  // Outer key: session name, inner key: instance key (agent or agent:threadId)
  private instances = new Map<string, Map<string, AgentEvent>>();
  private eventTimestamps = new Map<string, number[]>();
  // Per-instance unseen tracking: "session\0instanceKey"
  private unseenInstances = new Set<string>();
  private active = new Set<string>();

  private unseenKey(session: string, key: string): string {
    return `${session}\0${key}`;
  }

  applyEvent(event: AgentEvent, options?: { seed?: boolean }): void {
    const key = instanceKey(event.agent, event.threadId);

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
    }
    sessionInstances.set(key, event);

    // Clean up any synthetic pane-keyed entries for this agent
    // (pane scanner may have created a minimal synthetic before the watcher seeded)
    for (const [k, ev] of sessionInstances) {
      if (k !== key && ev.agent === event.agent && k.includes(":pane:")) {
        // Transfer pane info from the synthetic to the watcher entry
        if (ev.paneId && !event.paneId) {
          event.paneId = ev.paneId;
          event.liveness = ev.liveness;
        }
        sessionInstances.delete(k);
        this.unseenInstances.delete(this.unseenKey(event.session, k));
      }
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
    if (TERMINAL_STATUSES.has(event.status)) {
      if (options?.seed || !this.active.has(event.session)) {
        this.unseenInstances.add(ukey);
      }
    } else {
      // Non-terminal status for this instance = user is interacting, mark seen
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
      .sort((a, b) => b.ts - a.ts);
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
        }
      }
      if (sessionInstances.size === 0) {
        this.instances.delete(session);
      }
    }
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
   *  1. Entries with liveness "alive" whose paneId is missing from the scan → "exited"
   *  2. Each pane agent: find existing entry for this agent → stamp paneId + liveness.
   *     If none exists, create a minimal synthetic (status: "idle", liveness: "alive").
   *  Returns true if anything changed (caller uses this for broadcast decisions). */
  applyPanePresence(session: string, paneAgents: PanePresenceInput[]): boolean {
    let changed = false;
    let sessionInstances = this.instances.get(session);

    // Index incoming pane IDs for fast lookup
    const activePaneIds = new Set<string>();
    for (const pa of paneAgents) activePaneIds.add(pa.paneId);

    // 1. Handle previously-alive entries whose pane disappeared
    //    - Synthetics (pane-keyed, no watcher data): delete outright — they have no
    //      meaningful state to preserve and become zombie indicators if left behind.
    //    - Watcher-sourced entries: transition liveness to "exited" so the watcher
    //      can still manage their status lifecycle.
    if (sessionInstances) {
      for (const [key, event] of sessionInstances) {
        if (event.liveness === "alive" && event.paneId && !activePaneIds.has(event.paneId)) {
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

      // Find an existing entry for this agent that hasn't already been claimed.
      // Prefer watcher-sourced entries (no ":pane:" in key) over synthetics.
      let bestKey: string | undefined;
      let bestEvent: AgentEvent | undefined;
      for (const [k, ev] of sessionInstances) {
        if (ev.agent !== pa.agent) continue;
        if (claimedKeys.has(k)) continue;
        if (!bestEvent || !k.includes(":pane:")) {
          bestKey = k;
          bestEvent = ev;
          if (!k.includes(":pane:")) break;
        }
      }

      if (bestEvent && bestKey) {
        claimedKeys.add(bestKey);
        const wasDifferent = bestEvent.paneId !== pa.paneId || bestEvent.liveness !== "alive";
        bestEvent.paneId = pa.paneId;
        bestEvent.liveness = "alive";
        if (wasDifferent) changed = true;

        continue;
      }

      // No existing entry — create minimal synthetic
      const syntheticKey = `${pa.agent}:pane:${pa.paneId}`;

      if (!sessionInstances.has(syntheticKey)) {
        sessionInstances.set(syntheticKey, {
          agent: pa.agent,
          session,
          status: "idle",
          ts: Date.now(),
          paneId: pa.paneId,
          liveness: "alive",
        });
        changed = true;
      } else {
        const existing = sessionInstances.get(syntheticKey)!;
        const wasDifferent = existing.paneId !== pa.paneId || existing.liveness !== "alive";
        existing.paneId = pa.paneId;
        existing.liveness = "alive";
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
