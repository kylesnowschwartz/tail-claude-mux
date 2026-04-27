import type { AgentEvent } from "./agent";

/**
 * Callback context provided by the server to each watcher.
 * Lets watchers resolve project directories to mux session names
 * and emit events without knowing about server internals.
 */
export interface AgentWatcherContext {
  /** Resolve a project directory path to a mux session name, or null if unmatched */
  resolveSession(projectDir: string): string | null;
  /** Emit an agent event (applied to tracker + broadcast automatically) */
  emit(event: AgentEvent): void;
}

/**
 * Interface for agent watchers that detect agent status by watching
 * external data sources (thread files, databases, etc).
 *
 * Built-in: ClaudeCodeHookAdapter and PiHookAdapter receive lifecycle hooks via POST /hook.
 *
 * To add a new watcher:
 *   1. Create a file implementing AgentWatcher
 *   2. Register it directly in apps/server/src/main.ts
 */
export interface AgentWatcher {
  /** Unique name for this watcher (e.g. "amp", "claude-code") */
  readonly name: string;

  /** Start watching. Called once by the server with the watcher context. */
  start(ctx: AgentWatcherContext): void;

  /** Stop watching and clean up resources. */
  stop(): void;
}

// --- Hook-based detection ---

/** Payload sent by agent lifecycle hooks via POST /hook.
 *  All registered hook receivers see every payload; each adapter filters on
 *  the optional `agent` discriminator to claim its own events. Payloads with
 *  no `agent` field are treated as Claude Code for backward compatibility. */
export interface HookPayload {
  /** Optional agent discriminator. Missing/undefined → Claude Code (legacy).
   *  Known values: "claude-code", "pi". */
  agent?: string;
  /** Hook event name. Claude Code: "UserPromptSubmit" | "PreToolUse" |
   *  "PermissionRequest" | "PostToolUse" | "Stop" | "Notification" |
   *  "SessionStart" | "SessionEnd". Pi (snake_case): "session_start" |
   *  "agent_start" | "agent_end" | "tool_execution_start" |
   *  "tool_execution_end" | "session_shutdown". */
  event: string;
  /** Agent session UUID — used as threadId */
  session_id: string;
  /** Project directory the agent is working in */
  cwd: string;
  /** Tool name (PreToolUse, PermissionRequest, PostToolUse, tool_execution_*) */
  tool_name?: string;
  /** Tool input parameters — contains file_path, command, pattern, etc. */
  tool_input?: Record<string, unknown>;
  /** Notification subtype (Claude Code Notification only). */
  notification_type?: string;
  /** Pi session_start only: optional display name from SessionInfo or first user message. */
  session_name?: string;
  /** Pi tool_execution_end only: whether the tool reported an error. Tool-level
   *  errors do not change status; the LLM routinely recovers from them. */
  tool_is_error?: boolean;
  /** Pi agent_end only: why the assistant stopped. */
  stop_reason?: "stop" | "length" | "toolUse" | "error" | "aborted";
  /** Pi agent_end only: error message when stop_reason === "error". */
  error_message?: string;
  /** Pi session_shutdown only: why the session is ending. */
  shutdown_reason?: "quit" | "reload" | "new" | "resume" | "fork";
}

/** A watcher that can receive hook events pushed from the agent process. */
export interface HookReceiver {
  handleHook(payload: HookPayload): void;
}

/** Type guard: does this watcher accept hook payloads? */
export function isHookReceiver(w: AgentWatcher): w is AgentWatcher & HookReceiver {
  return typeof (w as any).handleHook === "function";
}
