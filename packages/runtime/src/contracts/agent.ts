export type AgentStatus = "idle" | "running" | "done" | "error" | "waiting" | "interrupted";

/** Whether the agent process is alive (pane exists) or has exited.
 *  "unknown" = watcher-only, no pane info available. */
export type AgentLiveness = "alive" | "exited" | "unknown";

export interface AgentEvent {
  agent: string;
  session: string;
  status: AgentStatus;
  ts: number;
  /** Timestamp of the first event ever recorded for this instance.
   *  Set once by the tracker on insert and preserved across status updates,
   *  so consumers can sort by arrival order without the list reshuffling
   *  every time an agent fires a fresh status. */
  firstSeenTs?: number;
  threadId?: string;
  threadName?: string;
  /** Set by tracker when serializing for the TUI — true if user hasn't seen this terminal state */
  unseen?: boolean;
  /** Set by pane scanner — the tmux pane ID where this agent was detected */
  paneId?: string;
  /** Set by pane scanner — tmux window index where this agent's pane lives.
   *  Used in the TUI agent-row left slot so rows line up with the status-bar
   *  window tabs. Renumbers when windows close if `renumber-windows on`. */
  windowIndex?: number;
  /** Set by pane scanner — tmux pane index within the window. Used as the
   *  secondary sort key so multiple agents in the same window appear in
   *  pane order. */
  paneIndex?: number;
  /** Whether the agent process is alive, exited, or unknown (no pane info) */
  liveness?: AgentLiveness;
  /** OS process id of the long-lived agent process. Resolved by the watcher
   *  via ancestor walk (Claude Code) or reported directly by the extension
   *  (pi). When present, the tracker's liveness sweep uses `process.kill(pid, 0)`
   *  to detect crashes that don't fire a SessionEnd hook. Local sessions only. */
  pid?: number;
  /** Human-readable description of current activity, e.g. "Reading config.ts" or "Bash: git push" */
  toolDescription?: string;
  /** Active subagent name when CC is running a Task tool call (e.g. "rb-orchestrator").
   *  Sourced from ~/.claude/sessions/<pid>.json `agent` field; undefined when the
   *  parent thread is in control. */
  subagent?: string;
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
  /** Agent process PID resolved via descendant walk of the pane's shell.
   *  Matches AgentEvent.pid so the tracker can claim a watcher entry by
   *  PID instead of "first unclaimed by iteration order" — which silently
   *  crisscrossed entries when multiple panes shared an agent name. */
  pid?: number;
  /** tmux window index hosting this pane — surfaced in the TUI to mirror the
   *  status-bar window tabs. */
  windowIndex?: number;
  /** tmux pane index within the window. Secondary sort key for the TUI list. */
  paneIndex?: number;
}
