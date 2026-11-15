/**
 * Pure logic for the sidebar's liaison block and "needs you" block.
 *
 * The server adds two optional fields to each agent in `/state`:
 * `attention` (why the conversation needs the engineer) and
 * `attentionSince` (epoch ms when that reason started). The shared
 * `AgentEvent` type does not declare them, so this module reads them
 * through a local intersection type.
 */

import type { AgentEvent, SessionData } from "@tcm/runtime";

export type AttentionReason = "permission" | "question" | "input" | "error" | "finished";

export type AttentionAgent = AgentEvent & {
  attention?: AttentionReason;
  attentionSince?: number;
  /** When the row's current status started, in epoch ms. */
  statusSince?: number;
};

/** When the conversation entered its current status; the last event time when the server does not say. */
export function statusStart(agent: AttentionAgent): number {
  return agent.statusSince ?? agent.ts;
}

/** The sidebar's five row labels, derived from tracker status + liveness. */
export type StatusLabel = "working" | "waiting" | "ready" | "stopped" | "error";

export const STATUS_LABEL_ORDER: readonly StatusLabel[] = ["working", "waiting", "ready", "stopped", "error"];

export function statusLabel(agent: Pick<AgentEvent, "status" | "liveness">): StatusLabel {
  const s = agent.status;
  if (s === "running") return "working";
  if (s === "waiting") return "waiting";
  if (s === "error") return "error";
  // done/interrupted/idle: liveness decides ready vs stopped.
  // "alive" means the pane scanner confirmed the process exists.
  // undefined means no pane data: terminal statuses read as stopped,
  // the synthetic cold-start idle reads as ready.
  if (agent.liveness === "alive") return "ready";
  if (s === "done" || s === "interrupted") return "stopped";
  return "ready";
}

export function isLiveAgent(agent: Pick<AgentEvent, "status" | "liveness">): boolean {
  return agent.liveness === "alive"
    || (agent.liveness !== "exited" && !["done", "error", "interrupted"].includes(agent.status));
}

/** A conversation paired with the tmux session that holds it. */
export interface SessionAgentRef {
  session: SessionData;
  agent: AttentionAgent;
}

function allAgents(sessions: readonly SessionData[]): SessionAgentRef[] {
  const out: SessionAgentRef[] = [];
  for (const session of sessions) {
    for (const agent of session.agents ?? []) out.push({ session, agent: agent as AttentionAgent });
  }
  return out;
}

export const LIAISON_PREFIX = "liaison";

/** A liaison is named exactly `liaison` or `liaison/<rest>`. */
export function isLiaisonName(name: string | undefined): boolean {
  return name === LIAISON_PREFIX || (!!name && name.startsWith(`${LIAISON_PREFIX}/`));
}

/** Header title: "LIAISON", plus the rest of the name when there is more. */
export function liaisonTitle(name: string): string {
  const rest = name.slice(LIAISON_PREFIX.length + 1);
  return rest ? `LIAISON ${rest}` : "LIAISON";
}

/** Open liaison conversations, ordered by name so blocks keep their place. */
export function findLiaisons(sessions: readonly SessionData[]): SessionAgentRef[] {
  return allAgents(sessions)
    .filter(({ agent }) => isLiaisonName(agent.threadName) && isLiveAgent(agent))
    .sort((a, b) => a.agent.threadName!.localeCompare(b.agent.threadName!));
}

/**
 * Whether `agent` claims that the liaison `liaison` started it: its
 * `liaisonParentThreadId` equals the liaison's thread ID. The server reads
 * the claim from tmux pane options that any program can set, so it is for
 * display only.
 */
function startedBy(agent: AttentionAgent, liaison: AttentionAgent): boolean {
  return !!liaison.threadId && agent.liaisonParentThreadId === liaison.threadId && agent !== liaison;
}

/** Whether a row shows the worker mark: an open liaison started it. */
export function isLiaisonWorker(agent: AttentionAgent, liaisons: readonly SessionAgentRef[]): boolean {
  return liaisons.some((liaison) => startedBy(agent, liaison.agent));
}

/** Count of conversations, across every session, that the liaison started. */
export function liaisonWorkerCount(sessions: readonly SessionData[], liaison: AttentionAgent): number {
  return allAgents(sessions).filter(({ agent }) => startedBy(agent, liaison)).length;
}

/** The `work` row value: "3 workers", or the bare count when the words do not fit in `columns`. */
export function formatWorkers(count: number, columns: number): string {
  const words = `${count} ${count === 1 ? "worker" : "workers"}`;
  return words.length <= columns ? words : String(count);
}

/** Count of conversations per status label across every session, in label order, zeros omitted. */
export function fleetCounts(sessions: readonly SessionData[]): { label: StatusLabel; count: number }[] {
  const counts = new Map<StatusLabel, number>();
  for (const { agent } of allAgents(sessions)) {
    const label = statusLabel(agent);
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }
  return STATUS_LABEL_ORDER
    .filter((label) => (counts.get(label) ?? 0) > 0)
    .map((label) => ({ label, count: counts.get(label)! }));
}

export function formatFleet(counts: readonly { label: StatusLabel; count: number }[]): string {
  return counts.map(({ label, count }) => `${count} ${label}`).join(" · ");
}

/** Whether the worded fleet summary fits in `columns`. */
export function fleetFits(counts: readonly { label: StatusLabel; count: number }[], columns: number): boolean {
  return formatFleet(counts).length <= columns;
}

/** Every live conversation with an attention reason, oldest reason first. */
export function needsYou(sessions: readonly SessionData[]): SessionAgentRef[] {
  return allAgents(sessions)
    .filter(({ agent }) => !!agent.attention && isLiveAgent(agent))
    .sort((a, b) => (a.agent.attentionSince ?? 0) - (b.agent.attentionSince ?? 0));
}

/** Compact age: "now", "45s", "14m", "3h", "2d". */
export function formatAge(sinceMs: number | undefined, nowMs: number): string {
  if (sinceMs == null || !Number.isFinite(sinceMs)) return "";
  const s = Math.max(0, Math.floor((nowMs - sinceMs) / 1000));
  if (s < 5) return "now";
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

/** The `needs` row value: "3 · oldest 14m", or "none" when nothing waits. */
export function needsSummary(rows: readonly SessionAgentRef[], nowMs: number): string {
  if (rows.length === 0) return "none";
  return `${rows.length} · oldest ${formatAge(rows[0]!.agent.attentionSince, nowMs)}`;
}

/**
 * Rows one liaison block takes: its header and closing rule, the fleet and
 * needs rows on the first block only, the work row when the liaison has
 * workers, and the status-text row when set. Each row is one line at every
 * width, because no row wraps.
 */
export function liaisonBlockRows(index: number, hasStatusText: boolean, hasWorkers: boolean): number {
  return 2 + (index === 0 ? 2 : 0) + (hasWorkers ? 1 : 0) + (hasStatusText ? 1 : 0);
}
