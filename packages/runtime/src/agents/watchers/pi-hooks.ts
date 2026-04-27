/**
 * Hook-based pi agent watcher.
 *
 * Receives lifecycle events pushed from pi via POST /hook (see
 * `integrations/pi-extension/`). Each payload carries `agent: "pi"`
 * so the single `/hook` endpoint can fan out to multiple adapters.
 *
 * Pi-specific behavior worth knowing:
 *   - Tool-level errors keep status `running`. The LLM routinely recovers
 *     from tool failures, so surfacing them as `error` creates noise.
 *   - Thread-level errors come in via `agent_end` with `stop_reason: "error"`
 *     and carry `error_message` — that is surfaced as `toolDescription`.
 *   - `stop_reason: "aborted"` → `interrupted` (Escape during a turn).
 *   - `session_shutdown` is treated like Claude Code's `SessionEnd`: it
 *     bypasses dedup and drops the thread so the tracker cleans up
 *     immediately instead of waiting for the terminal-prune window.
 *
 * JSONL is read for two bounded purposes only:
 *   1. Cold-start seed: scan recent pi session files once on startup so the
 *      sidebar shows pre-existing pi instances before any new hook arrives.
 *   2. Thread name resolution: one-time read when a hook introduces an
 *      unknown session UUID and we want a better label than "pi:<uuid>".
 */

import { readdir, stat } from "fs/promises";
import { join, basename } from "path";
import { homedir } from "os";
import { appendFileSync } from "fs";

import type { AgentStatus } from "../../contracts/agent";
import { TERMINAL_STATUSES } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext, HookPayload, HookReceiver } from "../../contracts/agent-watcher";

function dbg(tag: string, msg: string, data?: Record<string, unknown>) {
  const ts = new Date().toISOString().slice(11, 23);
  const suffix = data ? " " + JSON.stringify(data) : "";
  try { appendFileSync("/tmp/tcm-debug.log", `[${ts}] [pi-hooks:${tag}] ${msg}${suffix}\n`); } catch {}
}

// --- JSONL parsing types (shared between seed + thread name resolution) ---

interface PiContentItem {
  type?: string;
  text?: string;
}

interface PiMessage {
  role?: "user" | "assistant" | "toolResult" | string;
  content?: PiContentItem[] | string;
  stopReason?: "stop" | "length" | "toolUse" | "error" | "aborted";
  errorMessage?: string;
}

interface PiJournalEntry {
  type?: string;           // "session" | "message" | "session_info" | ...
  name?: string;           // session_info display name
  message?: PiMessage;     // message entries
  cwd?: string;            // SessionHeader (type === "session")
  id?: string;             // SessionHeader session UUID
}

/** Map pi assistant stopReason to AgentStatus. */
function statusFromStopReason(sr: PiMessage["stopReason"]): AgentStatus | null {
  if (!sr) return "running";
  switch (sr) {
    case "stop":
    case "length":
      return "done";
    case "toolUse":
      // toolUse means a tool call is about to be executed → still working.
      return "running";
    case "aborted":
      return "interrupted";
    case "error":
      return "error";
    default:
      return null;
  }
}

/** Determine status from a JSONL entry. Returns null for non-conversational entries. */
function determineStatusFromEntry(entry: PiJournalEntry): AgentStatus | null {
  if (entry.type !== "message") return null;
  const msg = entry.message;
  if (!msg?.role) return null;
  if (msg.role === "assistant") return statusFromStopReason(msg.stopReason);
  if (msg.role === "user" || msg.role === "toolResult") return "running";
  return null;
}

/** Extract a thread name from the first text of a user message, if any. */
function extractThreadNameFromUser(entry: PiJournalEntry): string | undefined {
  if (entry.type !== "message") return undefined;
  const msg = entry.message;
  if (msg?.role !== "user") return undefined;

  const content = msg.content;
  let text: string | undefined;
  if (typeof content === "string") text = content;
  else if (Array.isArray(content)) text = content.find((c) => c.type === "text" && c.text)?.text;

  if (!text) return undefined;
  if (text.startsWith("<") || text.startsWith("{")) return undefined;
  return text.slice(0, 80);
}

/** Session display name set via `pi.setSessionName()` / `/name`. */
function extractSessionInfoName(entry: PiJournalEntry): string | undefined {
  if (entry.type === "session_info" && typeof entry.name === "string" && entry.name.length > 0) {
    return entry.name;
  }
  return undefined;
}

/** Extract the UUID suffix from a pi session filename. Format is
 *  `<timestamp>_<uuid>.jsonl`; we take the part after the final `_`. */
function extractSessionIdFromFilename(filename: string): string | null {
  if (!filename.endsWith(".jsonl")) return null;
  const base = filename.slice(0, -".jsonl".length);
  const us = base.lastIndexOf("_");
  if (us < 0) return null;
  const uuid = base.slice(us + 1);
  return uuid.length > 0 ? uuid : null;
}

// --- Thread state ---

interface ThreadState {
  status: AgentStatus;
  threadName?: string;
  projectDir: string;
  nameResolved: boolean;
  /** Last tool description from tool_execution_start; kept across tool_execution_end. */
  lastToolDescription?: string;
}

const STALE_MS = 5 * 60 * 1000;

// --- Tool description generation (pi-native snake_case) ---

export function piToolDescription(toolName: string | undefined, toolInput: Record<string, unknown> | undefined): string | undefined {
  if (!toolName) return undefined;
  const input = toolInput ?? {};

  switch (toolName) {
    case "read": return pathDesc("Reading", input);
    case "edit": return pathDesc("Editing", input);
    case "write": return pathDesc("Writing", input);
    case "ls": return pathDesc("Listing", input);
    case "bash": {
      const cmd = typeof input.command === "string" ? input.command : undefined;
      if (cmd) return `Running ${cmd.slice(0, 30)}`;
      return "Running command";
    }
    case "find":
    case "glob":
    case "grep": {
      const pattern = typeof input.pattern === "string" ? input.pattern : undefined;
      if (pattern) return `Searching ${pattern.slice(0, 30)}`;
      return "Searching";
    }
    case "agent": {
      const desc = typeof input.description === "string" ? input.description : undefined;
      if (desc) return desc.slice(0, 40);
      return "Agent";
    }
    case "web_fetch": return "Fetching URL";
    case "web_search": {
      const query = typeof input.query === "string" ? input.query : undefined;
      if (query) return `Search: ${query.slice(0, 30)}`;
      return "Searching web";
    }
    case "ask_user_question": {
      const q = typeof input.question === "string" ? input.question : undefined;
      if (q) return `Question: ${q.slice(0, 50)}`;
      return "Asking question";
    }
    case "todo_write": return "Updating todos";
    default: return toolName;
  }
}

function pathDesc(verb: string, input: Record<string, unknown>): string {
  const p = typeof input.path === "string" ? input.path : undefined;
  if (p) return `${verb} ${basename(p)}`;
  return verb;
}

// --- Error message truncation ---

const ERROR_MESSAGE_LIMIT = 80;

function truncateError(msg: string | undefined): string | undefined {
  if (!msg) return undefined;
  const trimmed = msg.trim();
  if (trimmed.length === 0) return undefined;
  return trimmed.length > ERROR_MESSAGE_LIMIT
    ? trimmed.slice(0, ERROR_MESSAGE_LIMIT - 1) + "\u2026"
    : trimmed;
}

// --- Adapter ---

export class PiHookAdapter implements AgentWatcher, HookReceiver {
  readonly name = "pi";

  private threads = new Map<string, ThreadState>();
  private ctx: AgentWatcherContext | null = null;
  private sessionsDir: string;

  constructor(sessionsDir?: string) {
    this.sessionsDir = sessionsDir ?? join(homedir(), ".pi", "agent", "sessions");
  }

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    this.seedFromJsonl();
  }

  stop(): void {
    this.threads.clear();
    this.ctx = null;
  }

  handleHook(payload: HookPayload): void {
    // Only accept payloads explicitly marked as pi. Missing/other agents
    // belong to other adapters.
    if (payload.agent !== "pi") {
      dbg("hook", "ignored-foreign-agent", { agent: payload.agent, event: payload.event });
      return;
    }
    dbg("hook", "received", { event: payload.event, cwd: payload.cwd, session_id: payload.session_id?.slice(0, 8) });
    if (!this.ctx) { dbg("hook", "no-ctx"); return; }
    if (!payload.session_id || !payload.cwd) { dbg("hook", "missing-ids"); return; }

    const session = this.ctx.resolveSession(payload.cwd);
    if (!session) { dbg("hook", "no-session", { cwd: payload.cwd }); return; }

    const threadId = payload.session_id;
    let state = this.threads.get(threadId);
    let isNewThread = false;

    if (!state) {
      isNewThread = true;
      state = {
        status: "idle", // overwritten below if the event maps to something else
        projectDir: payload.cwd,
        // If session_start carries a name, it already counts as resolved.
        threadName: payload.event === "session_start" ? payload.session_name : undefined,
        nameResolved: payload.event === "session_start" && Boolean(payload.session_name),
      };
      this.threads.set(threadId, state);
      if (!state.nameResolved) this.resolveThreadName(threadId);
    } else if (payload.event === "session_start" && payload.session_name && !state.threadName) {
      state.threadName = payload.session_name;
      state.nameResolved = true;
    }

    // session_shutdown is the definitive end signal and must bypass dedup
    // so the tracker releases the instance immediately rather than waiting
    // out the terminal-prune window.
    if (payload.event === "session_shutdown") {
      state.status = "done";
      state.lastToolDescription = undefined;
      dbg("hook", "emit-ended", { threadId: threadId.slice(0, 8), session });
      this.emit(threadId, state, session, { ended: true });
      this.threads.delete(threadId);
      return;
    }

    const { status: newStatus, description } = this.resolveEvent(payload, state);
    if (newStatus === null) { dbg("hook", "ignored", { event: payload.event }); return; }

    // A "description update" is a visible change in the user-facing label.
    // Clearing an already-empty description, or setting the same value, does
    // not count and should be suppressed by dedup.
    const prevDescription = state.lastToolDescription;
    let newDescription = prevDescription;
    if (description.kind === "set") newDescription = description.value;
    else if (description.kind === "clear") newDescription = undefined;
    const hadToolUpdate = newDescription !== prevDescription;
    state.lastToolDescription = newDescription;

    // Dedup: suppress emission when nothing meaningful changed. Always emit
    // for a new thread, for status changes, or when a tool event updates the
    // visible description.
    if (state.status === newStatus && !isNewThread && !hadToolUpdate) {
      dbg("hook", "dedup", { threadId: threadId.slice(0, 8), status: newStatus });
      return;
    }

    state.status = newStatus;
    dbg("hook", "emit", { threadId: threadId.slice(0, 8), session, status: newStatus, tool: state.lastToolDescription });
    this.emit(threadId, state, session);
  }

  /** Map a pi hook payload to a new status plus a tool-description directive.
   *  Returning `{ status: null }` means ignore. */
  private resolveEvent(payload: HookPayload, _state: ThreadState): {
    status: AgentStatus | null;
    description: { kind: "keep" } | { kind: "set"; value: string | undefined } | { kind: "clear" };
  } {
    switch (payload.event) {
      case "session_start":
        return { status: "idle", description: { kind: "clear" } };

      case "agent_start":
        return { status: "running", description: { kind: "clear" } };

      case "tool_execution_start":
        return {
          status: "running",
          description: { kind: "set", value: piToolDescription(payload.tool_name, payload.tool_input) },
        };

      case "tool_execution_end":
        // Tool-level errors are routine; keep the user-visible description so
        // they can still see which tool the agent was running.
        return { status: "running", description: { kind: "keep" } };

      case "agent_end": {
        const sr = payload.stop_reason;
        if (sr === "aborted") {
          return { status: "interrupted", description: { kind: "clear" } };
        }
        if (sr === "error") {
          const truncated = truncateError(payload.error_message);
          return { status: "error", description: { kind: "set", value: truncated } };
        }
        // stop | length | toolUse | undefined → done
        return { status: "done", description: { kind: "clear" } };
      }

      default:
        return { status: null, description: { kind: "keep" } };
    }
  }

  private emit(threadId: string, state: ThreadState, session: string, extras?: { ended?: boolean }): void {
    if (!this.ctx) return;
    this.ctx.emit({
      agent: this.name,
      session,
      status: state.status,
      ts: Date.now(),
      threadId,
      threadName: state.threadName,
      toolDescription: state.lastToolDescription,
      ...(extras?.ended ? { ended: true } : {}),
    });
  }

  // --- Cold-start seed from JSONL files ---

  private async seedFromJsonl(): Promise<void> {
    if (!this.ctx) return;

    let dirs: string[];
    try { dirs = await readdir(this.sessionsDir); } catch { return; }

    const now = Date.now();

    for (const dir of dirs) {
      const dirPath = join(this.sessionsDir, dir);
      try { if (!(await stat(dirPath)).isDirectory()) continue; } catch { continue; }

      let files: string[];
      try { files = await readdir(dirPath); } catch { continue; }

      for (const file of files) {
        const threadId = extractSessionIdFromFilename(file);
        if (!threadId) continue;
        const filePath = join(dirPath, file);

        let fileStat;
        try { fileStat = await stat(filePath); } catch { continue; }
        if (now - fileStat.mtimeMs > STALE_MS) continue;

        // Hooks always win: if we already know this thread, skip the seed.
        if (this.threads.has(threadId)) continue;

        let text: string;
        try { text = await Bun.file(filePath).text(); } catch { continue; }

        const { status, threadName, cwd: projectDir } = parseJsonlTrailingStatus(text);
        if (status === "idle" || TERMINAL_STATUSES.has(status)) continue;
        if (!projectDir) continue;

        const session = this.ctx?.resolveSession(projectDir);
        if (!session) continue;

        this.threads.set(threadId, {
          status,
          threadName,
          projectDir,
          nameResolved: true,
        });

        this.ctx?.emit({
          agent: this.name,
          session,
          status,
          ts: fileStat.mtimeMs,
          threadId,
          threadName,
        });
      }
    }

    dbg("seed", "complete", { threadCount: this.threads.size });
  }

  // --- One-time thread name resolution ---

  private async resolveThreadName(threadId: string): Promise<void> {
    const state = this.threads.get(threadId);
    if (!state || state.nameResolved) return;
    state.nameResolved = true;

    let dirs: string[];
    try { dirs = await readdir(this.sessionsDir); } catch { return; }

    for (const dir of dirs) {
      const dirPath = join(this.sessionsDir, dir);
      let files: string[];
      try { files = await readdir(dirPath); } catch { continue; }

      const match = files.find((f) => f.endsWith(`_${threadId}.jsonl`));
      if (!match) continue;
      const filePath = join(dirPath, match);

      let text: string;
      try { text = await Bun.file(filePath).text(); } catch { continue; }

      const { threadName } = parseJsonlTrailingStatus(text);
      if (threadName && this.ctx) {
        state.threadName = threadName;
        const session = this.ctx.resolveSession(state.projectDir);
        if (session) this.emit(threadId, state, session);
      }
      return;
    }
  }
}

// --- Helpers exported for tests / future reuse ---

/** Walk a pi JSONL text and return the trailing conversational status, the
 *  best thread-name we can derive (session_info wins, else first user msg),
 *  and the `cwd` recorded in the SessionHeader.
 *
 *  Using `SessionHeader.cwd` avoids decoding pi's lossy directory-name scheme
 *  (e.g. `--Users-kyle-meta-claude-tcm--` can't reliably distinguish
 *  `meta/claude` from `meta-claude`). The header is authoritative. */
export function parseJsonlTrailingStatus(text: string): {
  status: AgentStatus;
  threadName?: string;
  cwd?: string;
} {
  let status: AgentStatus = "idle";
  let sessionInfoName: string | undefined;
  let firstUserText: string | undefined;
  let cwd: string | undefined;

  for (const line of text.split("\n")) {
    if (!line) continue;
    let entry: PiJournalEntry;
    try { entry = JSON.parse(line) as PiJournalEntry; } catch { continue; }

    if (entry.type === "session" && typeof entry.cwd === "string") {
      cwd = entry.cwd;
      continue;
    }

    const info = extractSessionInfoName(entry);
    if (info) sessionInfoName = info;
    else if (!firstUserText) {
      const name = extractThreadNameFromUser(entry);
      if (name) firstUserText = name;
    }

    const s = determineStatusFromEntry(entry);
    if (s !== null) status = s;
  }

  return { status, threadName: sessionInfoName ?? firstUserText, cwd };
}
