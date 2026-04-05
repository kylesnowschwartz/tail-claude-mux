/**
 * Hook-based Claude Code agent watcher.
 *
 * Receives lifecycle events (UserPromptSubmit, PreToolUse, PermissionRequest,
 * PostToolUse, Stop, Notification) pushed from Claude Code via POST /hook,
 * instead of polling JSONL files.
 *
 * JSONL reading is kept for two bounded purposes:
 *   1. Cold-start seed: scan recent files once on startup to bootstrap state
 *   2. Thread name resolution: one-time read when a new session_id appears
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
  try { appendFileSync("/tmp/opensessions-debug.log", `[${ts}] [cc-hooks:${tag}] ${msg}${suffix}\n`); } catch {}
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
  return text.slice(0, 80);
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
}

const STALE_MS = 5 * 60 * 1000;

/** Notification subtypes that indicate the user must act. */
const WAITING_NOTIFICATION_TYPES = new Set(["permission_prompt"]);

/** Notification subtypes that confirm the agent is idle at prompt (not waiting for input). */
const IDLE_NOTIFICATION_TYPES = new Set(["idle_prompt"]);

// --- Hook event → status mapping ---

const HOOK_STATUS_MAP: Record<string, AgentStatus> = {
  UserPromptSubmit: "running",
  PreToolUse: "running",
  PermissionRequest: "waiting",
  PostToolUse: "running",
  Stop: "done",
  // Notification is handled separately — status depends on notification_type
};

// --- Adapter ---

export class ClaudeCodeHookAdapter implements AgentWatcher, HookReceiver {
  readonly name = "claude-code";

  private threads = new Map<string, ThreadState>();
  private ctx: AgentWatcherContext | null = null;
  private projectsDir: string;


  constructor(projectsDir?: string) {
    this.projectsDir = projectsDir ?? join(homedir(), ".claude", "projects");
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
    dbg("hook", "received", { event: payload.event, cwd: payload.cwd, session_id: payload.session_id?.slice(0, 8) });
    if (!this.ctx) { dbg("hook", "no-ctx"); return; }

    // Resolve status: Notification branches on subtype, others use the flat map
    const newStatus = this.resolveStatus(payload);
    if (!newStatus) { dbg("hook", "ignored", { event: payload.event, notification_type: payload.notification_type }); return; }

    const session = this.ctx.resolveSession(payload.cwd);
    if (!session) { dbg("hook", "no-session", { cwd: payload.cwd }); return; }

    const threadId = payload.session_id;
    let state = this.threads.get(threadId);

    if (!state) {
      state = {
        status: "idle", // Will be overwritten below
        projectDir: payload.cwd,
        nameResolved: false,
      };
      this.threads.set(threadId, state);
      // Queue one-time thread name resolution
      this.resolveThreadName(threadId, payload.cwd);
    }

    // Deduplicate: don't emit if status hasn't changed
    if (state.status === newStatus) {
      dbg("hook", "dedup", { threadId: threadId.slice(0, 8), status: newStatus });
      return;
    }

    state.status = newStatus;
    dbg("hook", "emit", { threadId: threadId.slice(0, 8), session, status: newStatus });
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

  private emit(threadId: string, state: ThreadState, session: string): void {
    if (!this.ctx) return;
    this.ctx.emit({
      agent: "claude-code",
      session,
      status: state.status,
      ts: Date.now(),
      threadId,
      threadName: state.threadName,
    });
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
