/**
 * Hook-based Claude Code agent watcher.
 *
 * Receives lifecycle events (SessionStart, UserPromptSubmit, PreToolUse,
 * PermissionRequest, PostToolUse, Stop, Notification, SessionEnd) pushed
 * from Claude Code via POST /hook.
 *
 * Tool descriptions (e.g. "Reading config.ts") are extracted from PreToolUse
 * and PermissionRequest payloads and emitted on AgentEvent.toolDescription.
 *
 * JSONL reading is kept for two bounded purposes:
 *   1. Cold-start seed: scan recent files once on startup to bootstrap state
 *   2. Thread name resolution: one-time read when a new session_id appears
 */

import { readdir, stat } from "fs/promises";
import { join, basename } from "path";
import { homedir } from "os";
import { appendFileSync, readdirSync, readFileSync } from "fs";

import type { AgentStatus } from "../../contracts/agent";
import { TERMINAL_STATUSES } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext, HookPayload, HookReceiver } from "../../contracts/agent-watcher";
import { parseProcessSnapshot, resolveAgentSessionPid } from "../resolve-agent-pid";
import { sanitizeForDisplay, truncateToWidth } from "../../text";

// Path-segment aware matcher for the long-lived claude process. Matches
// `claude` or `claude-code` only when preceded by `^` or `/` and followed
// by whitespace, another `/`, or end-of-string — so directory names that
// contain "claude" as a substring (e.g. `meta-claude`) don't false-positive.
const CLAUDE_CMD_RE = /(?:^|\/)claude(?:-code)?(?=\s|\/|$)/i;

function dbg(tag: string, msg: string, data?: Record<string, unknown>) {
  const ts = new Date().toISOString().slice(11, 23);
  const suffix = data ? " " + JSON.stringify(data) : "";
  try { appendFileSync("/tmp/tcm-debug.log", `[${ts}] [cc-hooks:${tag}] ${msg}${suffix}\n`); } catch {}
}

// --- JSONL parsing types (shared with seed + thread name resolution) ---

interface ContentItem {
  type?: string;
  text?: string;
}

interface JournalEntry {
  type?: string;
  customTitle?: string;
  message?: {
    role?: string;
    stop_reason?: string | null;
    content?: ContentItem[] | string;
  };
}

// --- Status detection (extracted from claude-code.ts, used only for seed) ---

const INTERRUPT_PATTERNS = [
  "[Request interrupted by user",
  "[Request interrupted",
];
const EXIT_COMMAND_PATTERN = "<command-name>/exit</command-name>";
const SLASH_COMMAND_PATTERN = "<command-name>/";
const NOISE_USER_PREFIXES = [
  "<local-command-caveat>",
  "<local-command-stdout>",
  "<local-command-stderr>",
  "<bash-input>",
  "<bash-stdout>",
  "<bash-stderr>",
  "<system-reminder>",
  "<task-notification>",
];

/** Determine status from a JSONL entry. Returns null for control/metadata entries. */
function determineStatus(entry: JournalEntry): AgentStatus | null {
  const msg = entry.message;
  if (!msg?.role) return null;

  const content = msg.content;
  const items: ContentItem[] = Array.isArray(content)
    ? content
    : typeof content === "string"
      ? [{ type: "text", text: content }]
      : [];

  if (msg.role === "assistant") {
    if (items.some((c) => c.type === "tool_use")) return "running";
    if (items.some((c) => c.type === "thinking")) return "running";
    if (!msg.stop_reason) return "running";
    if (msg.stop_reason === "end_turn") return "done";
    if (msg.stop_reason === "tool_use") return "running";
    return "done";
  }

  if (msg.role === "user") {
    const text = typeof content === "string"
      ? content
      : items.find((c) => c.type === "text" && c.text)?.text;

    if (text) {
      if (INTERRUPT_PATTERNS.some((p) => text.startsWith(p))) return "interrupted";
      if (text.includes(EXIT_COMMAND_PATTERN)) return "done";
      if (text.includes(SLASH_COMMAND_PATTERN)) return null;
      if (NOISE_USER_PREFIXES.some((p) => text.startsWith(p))) return null;
    }

    if (items.some((c) => c.type === "tool_result")) return "running";
    return "running";
  }

  return null;
}

// --- Thread name extraction (used for seed and one-time JSONL reads) ---

function extractThreadName(entry: JournalEntry): string | undefined {
  const msg = entry.message;
  if (msg?.role !== "user") return undefined;

  const content = msg.content;
  let text: string | undefined;
  if (typeof content === "string") {
    text = content;
  } else if (Array.isArray(content)) {
    text = content.find((c) => c.type === "text" && c.text)?.text;
  }

  if (!text) return undefined;
  if (text.startsWith("<") || text.startsWith("{") || text.startsWith("[Request")) return undefined;
  return truncateToWidth(sanitizeForDisplay(text), 80);
}

function extractCustomTitle(entry: JournalEntry): string | undefined {
  if (entry.type === "custom-title" && entry.customTitle) return entry.customTitle;
  return undefined;
}

/** Decode Claude's encoded project dir name back to a path. */
function decodeProjectDir(encoded: string): string {
  const naive = encoded.replace(/-/g, "/");
  try { if (require("fs").statSync(naive).isDirectory()) return naive; } catch {}
  return `__encoded__:${encoded}`;
}

// --- Thread state ---

interface ThreadState {
  status: AgentStatus;
  threadName?: string;
  projectDir: string;
  nameResolved: boolean;
  /** Resolved long-lived agent pid. Set on first hook for this thread either
   *  via ancestor walk against the hook's process_snapshot (preferred) or via
   *  refreshSubagent's sessions/-walk fallback. Used by the tracker's liveness
   *  sweep and by refreshSubagent to key into sessions/<pid>.json. */
  pid?: number;
  /** Last tool description from PreToolUse/PermissionRequest — cleared on non-tool events */
  lastToolDescription?: string;
  /** Active subagent name from sessions/<pid>.json `agent` field, or undefined
   *  when the parent CC thread is in control. */
  subagent?: string;
}

const STALE_MS = 5 * 60 * 1000;

/** Notification subtypes that indicate the user must act. */
const WAITING_NOTIFICATION_TYPES = new Set(["permission_prompt"]);

/** Notification subtypes that confirm the agent is idle at prompt (not waiting for input). */
const IDLE_NOTIFICATION_TYPES = new Set(["idle_prompt"]);

// --- Tool description generation (ported from seance ctl.zig:1565-1603) ---

/** Generate a human-readable description of the current tool activity.
 *  Every interpolated value from `toolInput` is run through
 *  `sanitizeForDisplay` + `truncateToWidth` at the leaf, so a pasted ANSI
 *  sequence in a Bash command or a wide-char path can't disturb the row's
 *  column budget. */
export function toolDescription(toolName: string | undefined, toolInput: Record<string, unknown> | undefined): string | undefined {
  if (!toolName) return undefined;

  const input = toolInput ?? {};

  switch (toolName) {
    case "Read": return fileDesc("Reading", input);
    case "Edit": return fileDesc("Editing", input);
    case "Write": return fileDesc("Writing", input);
    case "Bash": {
      const cmd = safeStr(input.command);
      if (cmd) return `Running ${truncateToWidth(cmd, 30)}`;
      return "Running command";
    }
    case "Glob":
    case "Grep": {
      const pattern = safeStr(input.pattern);
      if (pattern) return `Searching ${truncateToWidth(pattern, 30)}`;
      return "Searching";
    }
    case "Agent": {
      const desc = safeStr(input.description);
      if (desc) return truncateToWidth(desc, 40);
      return "Agent";
    }
    case "WebFetch": return "Fetching URL";
    case "WebSearch": {
      const query = safeStr(input.query);
      if (query) return `Search: ${truncateToWidth(query, 30)}`;
      return "Searching web";
    }
    case "AskUserQuestion": {
      const q = safeStr(input.question);
      if (q) return `Question: ${truncateToWidth(q, 50)}`;
      return "Asking question";
    }
    default: return toolName;
  }
}

/** Read a string field, sanitize it for safe display, return "" if missing or non-string. */
function safeStr(value: unknown): string {
  if (typeof value !== "string") return "";
  return sanitizeForDisplay(value);
}

function fileDesc(verb: string, input: Record<string, unknown>): string {
  const fp = safeStr(input.file_path);
  if (fp) return `${verb} ${basename(fp)}`;
  return verb;
}

// --- Hook event → status mapping ---

const HOOK_STATUS_MAP: Record<string, AgentStatus> = {
  UserPromptSubmit: "running",
  SessionStart: "idle",
  PreToolUse: "running",
  PermissionRequest: "waiting",
  PostToolUse: "running",
  Stop: "done",
  SessionEnd: "done",
  // Notification is handled separately — status depends on notification_type
};

// --- Adapter ---

export class ClaudeCodeHookAdapter implements AgentWatcher, HookReceiver {
  readonly name = "claude-code";

  private threads = new Map<string, ThreadState>();
  private ctx: AgentWatcherContext | null = null;
  private projectsDir: string;
  private sessionsDir: string;


  constructor(projectsDir?: string, sessionsDir?: string) {
    this.projectsDir = projectsDir ?? join(homedir(), ".claude", "projects");
    this.sessionsDir = sessionsDir ?? join(homedir(), ".claude", "sessions");
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
    // Filter on the optional `agent` discriminator. Missing/undefined falls
    // through as Claude Code (legacy hook payloads have no `agent` field).
    if (payload.agent !== undefined && payload.agent !== "claude-code") {
      dbg("hook", "ignored-foreign-agent", { agent: payload.agent, event: payload.event });
      return;
    }
    dbg("hook", "received", { event: payload.event, cwd: payload.cwd, session_id: payload.session_id?.slice(0, 8) });
    if (!this.ctx) { dbg("hook", "no-ctx"); return; }

    // Resolve status: Notification branches on subtype, others use the flat map
    const newStatus = this.resolveStatus(payload);
    if (!newStatus) { dbg("hook", "ignored", { event: payload.event, notification_type: payload.notification_type }); return; }

    const session = this.ctx.resolveSession(payload.cwd);
    if (!session) { dbg("hook", "no-session", { cwd: payload.cwd }); return; }

    const threadId = payload.session_id;
    let state = this.threads.get(threadId);
    let isNewThread = false;

    if (!state) {
      isNewThread = true;
      state = {
        status: "idle", // Will be overwritten below
        projectDir: payload.cwd,
        nameResolved: false,
      };
      this.threads.set(threadId, state);
      // Queue one-time thread name resolution
      this.resolveThreadName(threadId, payload.cwd);
    }

    // Resolve the long-lived agent pid once per thread. The hook's reported
    // pid ($PPID) is the `sh -c` wrapper; walking ancestry against the
    // snapshot finds the actual claude process. Re-resolve on every hook
    // until we have a pid (a hook without process_snapshot can land first).
    if (state.pid == null && payload.pid != null && payload.process_snapshot) {
      const proc = parseProcessSnapshot(payload.process_snapshot);
      const resolved = resolveAgentSessionPid(payload.pid, CLAUDE_CMD_RE, proc);
      if (resolved !== payload.pid) {
        // Walked up successfully to a claude ancestor.
        state.pid = resolved;
      } else {
        // Walker gave up. Only trust the reported pid if its OWN command in
        // the snapshot matches the claude pattern — otherwise it's the
        // wrapper shell and would cause the liveness sweep to false-fire.
        const info = proc.get(payload.pid);
        if (info && CLAUDE_CMD_RE.test(info.command)) state.pid = payload.pid;
      }
    }

    // Refresh subagent from sessions/<pid>.json. Failures are swallowed —
    // state.subagent stays whatever it was (preserved through transient errors).
    // Independent of the pid above (uses its own sessions/-walk resolver).
    this.refreshSubagent(threadId, state);


    // SessionEnd must bypass the dedup check below: a prior Stop event
    // already set status=done, so the dedup path would otherwise swallow
    // the end signal and leave a ghost entry until the 5-min prune.
    if (payload.event === "SessionEnd") {
      state.status = newStatus;
      state.lastToolDescription = undefined;
      dbg("hook", "emit-ended", { threadId: threadId.slice(0, 8), session });
      this.emit(threadId, state, session, { ended: true });
      this.threads.delete(threadId);
      return;
    }

    // Compute tool description for tool-related events
    const hasToolContext = payload.event === "PreToolUse" || payload.event === "PermissionRequest";
    if (hasToolContext) {
      state.lastToolDescription = toolDescription(payload.tool_name, payload.tool_input);
    } else if (payload.event !== "PostToolUse") {
      // Clear tool description on non-tool events (UserPromptSubmit, Stop, etc.)
      // PostToolUse keeps the prior description since it means a tool just finished
      state.lastToolDescription = undefined;
    }

    // Deduplicate: don't emit if status hasn't changed.
    // Exceptions: always emit for new threads, and for tool events (new tool description).
    if (state.status === newStatus && !isNewThread && !hasToolContext) {
      dbg("hook", "dedup", { threadId: threadId.slice(0, 8), status: newStatus });
      return;
    }

    state.status = newStatus;
    dbg("hook", "emit", { threadId: threadId.slice(0, 8), session, status: newStatus, tool: state.lastToolDescription });
    this.emit(threadId, state, session);
  }

  /** Map a hook payload to a status, or null if the event should be ignored. */
  private resolveStatus(payload: HookPayload): AgentStatus | null {
    if (payload.event === "Notification") {
      if (payload.notification_type && WAITING_NOTIFICATION_TYPES.has(payload.notification_type)) {
        return "waiting";
      }
      if (payload.notification_type && IDLE_NOTIFICATION_TYPES.has(payload.notification_type)) {
        return "done";
      }
      // Unknown or unhandled notification subtypes — ignore rather than guess
      return null;
    }
    return HOOK_STATUS_MAP[payload.event] ?? null;
  }

  private emit(threadId: string, state: ThreadState, session: string, extras?: { ended?: boolean }): void {
    if (!this.ctx) return;
    this.ctx.emit({
      agent: "claude-code",
      session,
      status: state.status,
      ts: Date.now(),
      threadId,
      threadName: state.threadName,
      toolDescription: state.lastToolDescription,
      pid: state.pid,
      subagent: state.subagent,
      ...(extras?.ended ? { ended: true } : {}),
    });
  }

  // --- sessions/<pid>.json resolution for subagent field ---

  /** Walk ~/.claude/sessions/*.json once and return the pid matching `threadId`. */
  private resolvePidFromSessions(threadId: string): number | undefined {
    let entries: string[];
    try { entries = readdirSync(this.sessionsDir); } catch { return undefined; }

    for (const entry of entries) {
      if (!entry.endsWith(".json")) continue;
      const filePath = join(this.sessionsDir, entry);
      try {
        const data = JSON.parse(readFileSync(filePath, "utf-8"));
        if (data.sessionId === threadId && typeof data.pid === "number") {
          return data.pid;
        }
      } catch {}
    }
    return undefined;
  }

  /** Read sessions/<pid>.json and return the parsed payload (or undefined on failure). */
  private readSessionFile(pid: number): { agent?: string; sessionId?: string } | undefined {
    try {
      return JSON.parse(readFileSync(join(this.sessionsDir, `${pid}.json`), "utf-8"));
    } catch { return undefined; }
  }

  /** Refresh `state.subagent` from sessions/<pid>.json.
   *
   *  PID resolution precedence:
   *    1. state.pid set by main's process-ancestry walker (preferred — uses the
   *       hook's $PPID + process_snapshot, doesn't depend on sessions/ files).
   *    2. Fall back to walking ~/.claude/sessions/*.json for a sessionId match.
   *
   *  PID-reuse detection: the file's sessionId must match this thread's id. If
   *  it mismatches, the OS reused the pid for a different CC process — clear
   *  the cache and re-resolve next hook. A *missing* file is NOT treated as
   *  reuse: the file may be transiently unavailable (tests, race) and we
   *  shouldn't clobber main's resolved pid on that signal alone. */
  private refreshSubagent(threadId: string, state: ThreadState): void {
    try {
      // (1) Acquire pid via sessions/-walk only if main didn't already set one.
      if (state.pid === undefined) {
        const resolved = this.resolvePidFromSessions(threadId);
        if (resolved === undefined) {
          state.subagent = undefined;
          return;
        }
        state.pid = resolved;
      }

      const cached = this.readSessionFile(state.pid);

      // Missing file → don't clear state.pid (main may have set it correctly;
      // file may be transient). Just clear subagent.
      if (!cached) {
        state.subagent = undefined;
        return;
      }

      // sessionId mismatch is the authoritative PID-reuse signal — clear cache.
      if (cached.sessionId !== threadId) {
        state.pid = undefined;
        state.subagent = undefined;
        return;
      }

      state.subagent = typeof cached.agent === "string" ? cached.agent : undefined;
    } catch {
      state.subagent = undefined;
    }
  }

  // --- Cold-start seed from JSONL files ---

  private async seedFromJsonl(): Promise<void> {
    if (!this.ctx) return;

    let dirs: string[];
    try { dirs = await readdir(this.projectsDir); } catch { return; }

    const now = Date.now();

    for (const dir of dirs) {
      const dirPath = join(this.projectsDir, dir);
      try { if (!(await stat(dirPath)).isDirectory()) continue; } catch { continue; }

      const projectDir = decodeProjectDir(dir);

      let files: string[];
      try { files = await readdir(dirPath); } catch { continue; }

      for (const file of files) {
        if (!file.endsWith(".jsonl")) continue;
        const filePath = join(dirPath, file);

        let fileStat;
        try { fileStat = await stat(filePath); } catch { continue; }
        if (now - fileStat.mtimeMs > STALE_MS) continue;

        const threadId = basename(file, ".jsonl");

        // Don't overwrite state already established by hooks
        if (this.threads.has(threadId)) continue;

        let text: string;
        try { text = await Bun.file(filePath).text(); } catch { continue; }

        const lines = text.split("\n").filter(Boolean);
        let latestStatus: AgentStatus = "idle";
        let threadName: string | undefined;

        for (const line of lines) {
          let entry: JournalEntry;
          try { entry = JSON.parse(line); } catch { continue; }
          const customTitle = extractCustomTitle(entry);
          if (customTitle) threadName = customTitle;
          else if (!threadName) {
            const name = extractThreadName(entry);
            if (name) threadName = name;
          }
          const s = determineStatus(entry);
          if (s !== null) latestStatus = s;
        }

        if (latestStatus === "idle" || TERMINAL_STATUSES.has(latestStatus)) continue;

        const session = this.ctx?.resolveSession(projectDir);
        if (!session) continue;

        this.threads.set(threadId, {
          status: latestStatus,
          threadName,
          projectDir,
          nameResolved: true,
        });

        this.ctx?.emit({
          agent: "claude-code",
          session,
          status: latestStatus,
          ts: fileStat.mtimeMs,
          threadId,
          threadName,
        });
      }
    }

    dbg("seed", "complete", { threadCount: this.threads.size });
  }

  // --- One-time thread name resolution ---

  private async resolveThreadName(threadId: string, _cwd: string): Promise<void> {
    const state = this.threads.get(threadId);
    if (!state || state.nameResolved) return;
    state.nameResolved = true;

    // Find the JSONL file for this session_id
    let dirs: string[];
    try { dirs = await readdir(this.projectsDir); } catch { return; }

    for (const dir of dirs) {
      const dirPath = join(this.projectsDir, dir);
      const filePath = join(dirPath, `${threadId}.jsonl`);

      let text: string;
      try { text = await Bun.file(filePath).text(); } catch { continue; }

      const lines = text.split("\n").filter(Boolean);
      let threadName: string | undefined;

      for (const line of lines) {
        let entry: JournalEntry;
        try { entry = JSON.parse(line); } catch { continue; }
        const customTitle = extractCustomTitle(entry);
        if (customTitle) { threadName = customTitle; break; }
        if (!threadName) {
          const name = extractThreadName(entry);
          if (name) threadName = name;
        }
      }

      if (threadName && this.ctx) {
        state.threadName = threadName;
        // Re-emit with the resolved name
        const session = this.ctx.resolveSession(state.projectDir);
        if (session) {
          this.emit(threadId, state, session);
        }
      }
      return; // Found the file, done
    }
  }
}
