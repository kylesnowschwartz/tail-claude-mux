/**
 * Pure logic for the sidebar's liaison block and "needs you" block.
 *
 * The server adds two optional fields to each agent in `/state`:
 * `attention` (why the conversation needs the engineer) and
 * `attentionSince` (epoch ms when that reason started). The shared
 * `AgentEvent` type does not declare them, so this module reads them
 * through a local intersection type.
 */

import type { AgentEvent, LiaisonItem, LiaisonItemKind, SessionData } from "@tcm/runtime";

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

export const LIAISON_PREFIX = "liaison";

/** Separator between a conversation's name and the git branch Claude Code appends to it. */
const BRANCH_SEPARATOR = " \u00b7 ";

/** The name without its ` · <branch>` suffix. */
function nameBeforeBranch(name: string): string {
  const at = name.indexOf(BRANCH_SEPARATOR);
  return at < 0 ? name : name.slice(0, at);
}

/**
 * A liaison is named `liaison` or `liaison/<rest>`, optionally followed by a
 * ` · <branch>` suffix.
 */
export function isLiaisonName(name: string | undefined): boolean {
  if (!name) return false;
  const base = nameBeforeBranch(name);
  return base === LIAISON_PREFIX || base.startsWith(`${LIAISON_PREFIX}/`);
}

/** Header title: "LIAISON", plus the rest of the name, without its branch suffix, when there is more. */
export function liaisonTitle(name: string): string {
  const rest = nameBeforeBranch(name).slice(LIAISON_PREFIX.length + 1);
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

/**
 * Count of conversations, across every session, that the liaison started and
 * that are not in `finished` (the liaison's `liaisonFinished` report).
 */
export function liaisonWorkerCount(
  sessions: readonly SessionData[], liaison: AttentionAgent, finished: readonly string[] = [],
): number {
  return allAgents(sessions)
    .filter(({ agent }) => startedBy(agent, liaison) && !(agent.threadId && finished.includes(agent.threadId)))
    .length;
}

/**
 * Every live conversation with an attention reason, oldest reason first.
 * An open liaison's own conversation is left out: at its prompt (`input`) or
 * done (`finished`) it is at rest, and its `permission`, `question` and
 * `error` reasons show as the `asks` item in its own liaison block.
 */
export function needsYou(sessions: readonly SessionData[]): SessionAgentRef[] {
  return allAgents(sessions)
    .filter(({ agent }) => !!agent.attention && isLiveAgent(agent) && !isLiaisonName(agent.threadName))
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

/** Kinds of liaison item in priority order: 1 for the user's action now, 3 for a reminder. */
export const LIAISON_ITEM_PRIORITY: Record<LiaisonItemKind, 1 | 2 | 3> = {
  asks: 1, blocked: 1, no_report: 2, answer: 2, reminder: 3,
};

/** Item rows a liaison block shows at most; a `+K` on the last row counts the rest. */
export const LIAISON_ITEM_ROWS = 3;

/**
 * The items one liaison block shows, in priority order and oldest first
 * within a priority: an `asks` item when the liaison's own conversation waits
 * on a permission or a question or has failed, then the items its session reports through
 * `POST /set-liaison-items`.
 */
export function liaisonItems(ref: SessionAgentRef): LiaisonItem[] {
  const items: LiaisonItem[] = [];
  const reason = ref.agent.attention;
  if (reason === "permission" || reason === "question" || reason === "error") {
    items.push({ kind: "asks", title: reason, since: ref.agent.attentionSince ?? ref.agent.ts });
  }
  items.push(...(ref.session.metadata?.liaisonItems ?? []));
  return items
    .map((item, i) => ({ item, i }))
    .sort((a, b) =>
      LIAISON_ITEM_PRIORITY[a.item.kind] - LIAISON_ITEM_PRIORITY[b.item.kind]
      || a.item.since - b.item.since
      || a.i - b.i)
    .map(({ item }) => item);
}

/**
 * Whether a row shows the finished mark: an open liaison started it and
 * reports its conversation as finished and still open.
 */
export function isLiaisonFinished(agent: AttentionAgent, liaisons: readonly SessionAgentRef[]): boolean {
  return !!agent.threadId && liaisons.some((liaison) =>
    startedBy(agent, liaison.agent) && (liaison.session.metadata?.liaisonFinished ?? []).includes(agent.threadId!));
}

/**
 * Rows one liaison block takes: its title row, one row per shown item (at
 * most LIAISON_ITEM_ROWS), and the closing rule. Each row is one line at
 * every width, because no row wraps.
 */
export function liaisonBlockRows(itemCount: number): number {
  return 2 + Math.min(itemCount, LIAISON_ITEM_ROWS);
}
