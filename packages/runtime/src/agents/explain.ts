/**
 * Diagnostic builder for the `GET /explain` endpoint — answers "why is this row
 * stuck on <status>?" by projecting an AgentEvent against the tracker's prune
 * lifecycle and reporting which tier governs it and when that tier will fire.
 *
 * Pure: takes the event, a `now` timestamp, and a fresh liveness probe verdict
 * (the caller runs probeAgentLiveness so this stays I/O-free and unit-testable).
 * The tier math mirrors the guards in:
 *   - AgentTracker.reconcileStaleRunning  (RECONCILE_STALE_MS)
 *   - AgentTracker.pruneStuck             (STUCK_RUNNING_TIMEOUT_MS / ALIVE_PRUNE_CEILING_MS)
 *   - AgentTracker.pruneTerminal          (TERMINAL_PRUNE_MS)
 * If those guards change, update this in lockstep.
 */

import type { AgentEvent, AgentLiveness, AgentStatus } from "../contracts/agent";
import { TERMINAL_STATUSES } from "../contracts/agent";
import { RECONCILE_STALE_MS, STUCK_RUNNING_TIMEOUT_MS } from "../shared";
import { ALIVE_PRUNE_CEILING_MS, TERMINAL_PRUNE_MS } from "./tracker";

export type ExplainProbeVerdict = "working" | "ended" | null;

export interface ExplainReport {
  agent: string;
  session: string;
  status: AgentStatus;
  liveness: AgentLiveness | "unknown";
  threadId: string | null;
  pid: number | null;
  paneId: string | null;
  windowIndex: number | null;
  paneIndex: number | null;
  toolDescription: string | null;
  ts: number;
  ageMs: number;
  /** The reconcile pass's authoritative probe (run fresh by the caller). */
  probe: {
    /** Whether the reconcile guard would currently run a probe for this entry. */
    eligible: boolean;
    verdict: ExplainProbeVerdict;
  };
  lifecycle: {
    /** id of the tier that will next act on this entry, or "stable". */
    governing: string;
    /** human-readable explanation of the governing tier's decision. */
    detail: string;
    tiers: ExplainTier[];
  };
}

export interface ExplainTier {
  /** Stable identifier matching the tracker method that owns this tier. */
  id: "reconcile" | "prune-stuck" | "alive-ceiling" | "prune-terminal" | "prune-idle";
  /** Whether this tier governs the entry given its status + liveness. */
  applies: boolean;
  /** The timeout constant this tier enforces (ms). */
  thresholdMs: number;
  /** ms until this tier becomes eligible (0 = eligible now). null when N/A. */
  eligibleInMs: number | null;
  note: string;
}

function eligibleIn(thresholdMs: number, ageMs: number): number {
  return Math.max(0, thresholdMs - ageMs);
}

/** Build the diagnostic report. `now` and `probeVerdict` are injected so the
 *  function is pure (no Date.now, no disk read). */
export function buildExplain(
  event: AgentEvent,
  now: number,
  probeVerdict: ExplainProbeVerdict,
): ExplainReport {
  const status = event.status;
  const liveness: AgentLiveness | "unknown" = event.liveness ?? "unknown";
  const ageMs = Math.max(0, now - event.ts);
  const isRunning = status === "running";
  const isTerminal = TERMINAL_STATUSES.has(status);
  const isAlive = liveness === "alive";
  const isExited = liveness === "exited";
  const hasPid = event.pid != null;

  // --- reconcile (running + alive + pid): probes the session file once stale ---
  const reconcileApplies = isRunning && isAlive && hasPid;
  const reconcile: ExplainTier = {
    id: "reconcile",
    applies: reconcileApplies,
    thresholdMs: RECONCILE_STALE_MS,
    eligibleInMs: reconcileApplies ? eligibleIn(RECONCILE_STALE_MS, ageMs) : null,
    note: reconcileApplies
      ? "running+alive: probes ~/.claude/sessions/<pid>.json once stale; 'ended'→done (clears spinner), 'working'→resets the clock"
      : "only running+alive entries with a pid are reconciled",
  };

  // --- pruneStuck (running): exited prunes at STUCK_RUNNING_TIMEOUT_MS ---
  const stuckApplies = isRunning && isExited;
  const pruneStuck: ExplainTier = {
    id: "prune-stuck",
    applies: stuckApplies,
    thresholdMs: STUCK_RUNNING_TIMEOUT_MS,
    eligibleInMs: stuckApplies ? eligibleIn(STUCK_RUNNING_TIMEOUT_MS, ageMs) : null,
    note: stuckApplies
      ? "running+exited: pruned once age exceeds the stuck-running timeout"
      : "applies to running+exited entries",
  };

  // --- alive ceiling (running + alive): last-resort backstop ---
  const ceilingApplies = isRunning && isAlive;
  const aliveCeiling: ExplainTier = {
    id: "alive-ceiling",
    applies: ceilingApplies,
    thresholdMs: ALIVE_PRUNE_CEILING_MS,
    eligibleInMs: ceilingApplies ? eligibleIn(ALIVE_PRUNE_CEILING_MS, ageMs) : null,
    note: ceilingApplies
      ? "running+alive: pruned only if it reaches the ceiling with no fresh hook and no 'working' confirmation (lost terminal signal)"
      : "applies to running+alive entries",
  };

  // --- pruneTerminal: terminal+exited at TERMINAL_PRUNE_MS; idle/waiting+exited now ---
  const terminalApplies = isTerminal && isExited;
  const pruneTerminal: ExplainTier = {
    id: "prune-terminal",
    applies: terminalApplies,
    thresholdMs: TERMINAL_PRUNE_MS,
    eligibleInMs: terminalApplies ? eligibleIn(TERMINAL_PRUNE_MS, ageMs) : null,
    note: terminalApplies
      ? "terminal+exited: held so the outcome is visible, then pruned (unseen entries are exempt)"
      : isTerminal
        ? "terminal+alive: not pruned until the process exits"
        : "applies to done/error/interrupted entries that have exited",
  };

  const idleApplies = (status === "idle" || status === "waiting") && isExited;
  const pruneIdle: ExplainTier = {
    id: "prune-idle",
    applies: idleApplies,
    thresholdMs: 0,
    eligibleInMs: idleApplies ? 0 : null,
    note: idleApplies
      ? "idle/waiting+exited: pruned immediately (no narrative to preserve; unseen entries are exempt)"
      : "applies to idle/waiting entries that have exited",
  };

  const tiers = [reconcile, pruneStuck, aliveCeiling, pruneTerminal, pruneIdle];

  // Governing tier: the applicable tier acting soonest. Among applicable tiers,
  // pick the smallest eligibleInMs; ties resolve in declaration order (reconcile
  // before the ceiling, matching the actual pass ordering).
  let governing = "stable";
  let detail = `${status}${liveness !== "unknown" ? `+${liveness}` : ""}: no prune tier currently governs this entry`;
  let bestEligible = Infinity;
  for (const t of tiers) {
    if (!t.applies || t.eligibleInMs == null) continue;
    if (t.eligibleInMs < bestEligible) {
      bestEligible = t.eligibleInMs;
      governing = t.id;
      detail = t.note;
    }
  }

  return {
    agent: event.agent,
    session: event.session,
    status,
    liveness,
    threadId: event.threadId ?? null,
    pid: event.pid ?? null,
    paneId: event.paneId ?? null,
    windowIndex: event.windowIndex ?? null,
    paneIndex: event.paneIndex ?? null,
    toolDescription: event.toolDescription ?? null,
    ts: event.ts,
    ageMs,
    probe: {
      eligible: reconcileApplies && ageMs > RECONCILE_STALE_MS,
      verdict: probeVerdict,
    },
    lifecycle: { governing, detail, tiers },
  };
}
