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
 * Built-in: ClaudeCodeHookAdapter receives lifecycle hooks via POST /hook.
 * Community agents use the AgentWatcher plugin interface with file watching.
 *
 * To add a new watcher:
 *   1. Create a file implementing AgentWatcher
 *   2. Register it via PluginAPI.registerWatcher() or in start.ts
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

/** Payload sent by Claude Code lifecycle hooks via POST /hook. */
export interface HookPayload {
  /** Hook event name: "UserPromptSubmit" | "PreToolUse" | "PermissionRequest" | "PostToolUse" | "Stop" | "Notification" */
  event: string;
  /** Claude Code session UUID — used as threadId */
  session_id: string;
  /** Project directory the agent is working in */
  cwd: string;
  /** Tool name (PreToolUse, PermissionRequest, PostToolUse) */
  tool_name?: string;
  /** Notification subtype (Notification only): "permission_prompt" | "idle_prompt" | "auth_success" | "elicitation_dialog" */
  notification_type?: string;
}

/** A watcher that can receive hook events pushed from the agent process. */
export interface HookReceiver {
  handleHook(payload: HookPayload): void;
}

/** Type guard: does this watcher accept hook payloads? */
export function isHookReceiver(w: AgentWatcher): w is AgentWatcher & HookReceiver {
  return typeof (w as any).handleHook === "function";
}
