/**
 * Pure logic for the sidebar's "needs you" block.
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

/** The sidebar's five row labels, derived from tracker status + liveness. */
export type StatusLabel = "working" | "waiting" | "ready" | "stopped" | "error";

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

/**
 * Every live conversation with an attention reason, oldest reason first.
 */
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
