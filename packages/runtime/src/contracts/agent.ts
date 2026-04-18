export type AgentStatus = "idle" | "running" | "done" | "error" | "waiting" | "interrupted";

/** Whether the agent process is alive (pane exists) or has exited.
 *  "unknown" = watcher-only, no pane info available. */
export type AgentLiveness = "alive" | "exited" | "unknown";

export interface AgentEvent {
  agent: string;
  session: string;
  status: AgentStatus;
  ts: number;
  threadId?: string;
  threadName?: string;
  /** Set by tracker when serializing for the TUI — true if user hasn't seen this terminal state */
  unseen?: boolean;
  /** Set by pane scanner — the tmux pane ID where this agent was detected */
  paneId?: string;
  /** Whether the agent process is alive, exited, or unknown (no pane info) */
  liveness?: AgentLiveness;
  /** Human-readable description of current activity, e.g. "Reading config.ts" or "Bash: git push" */
  toolDescription?: string;
  /** Signals the tracker to remove this instance immediately instead of holding
   *  it in the terminal-prune window. Set by watchers when the underlying
   *  agent session has definitively ended (e.g. SessionEnd hook). */
  ended?: boolean;
}

export const TERMINAL_STATUSES = new Set<AgentStatus>(["done", "error", "interrupted"]);

/** Input from the pane scanner to applyPanePresence().
 *  The scanner only answers "is there a live agent process in this pane?"
 *  Status, threadId, and threadName come exclusively from watchers. */
export interface PanePresenceInput {
  agent: string;
  paneId: string;
}
