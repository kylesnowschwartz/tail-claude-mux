/**
 * Claude Code agent watcher
 *
 * Watches ~/.claude/projects/ for JSONL file changes,
 * determines agent status from journal entries, and emits events
 * mapped to mux sessions via the project directory encoded in folder names.
 *
 * Directory structure: ~/.claude/projects/<encoded-path>/<session-id>.jsonl
 * Encoded path: /Users/foo/myproject → -Users-foo-myproject
 *
 * All file I/O is async to avoid blocking the server event loop.
 *
 * ## Claude Code JSONL Lifecycle (observed v2.1.87)
 *
 * Each JSONL file represents one Claude Code session. Entries are appended
 * as the session progresses. The top-level `type` field determines the
 * entry category:
 *
 * ### Control entries (no message.role — SKIP, do not change status)
 *   - `queue-operation`       — enqueue/dequeue markers for headless/queued prompts
 *   - `file-history-snapshot` — file state snapshot between turns (interactive mode)
 *   - `last-prompt`           — written at end of headless `--print` sessions
 *
 * ### Message entries (have message.role)
 *   - `user`      role=user      — user prompt OR tool_result OR interrupt marker
 *   - `assistant`  role=assistant — model response (streamed as multiple entries)
 *
 * ### Assistant streaming: one API turn → multiple JSONL entries
 *   Claude Code splits each assistant turn into separate entries:
 *     1. `thinking`  entry (content=[{type:"thinking"}], stop_reason=null)
 *     2. `text`      entry (content=[{type:"text"}], stop_reason=null)  — partial
 *     3. `tool_use`  entry (content=[{type:"tool_use"}], stop_reason="tool_use"|null)
 *     4. `text`      entry (content=[{type:"text"}], stop_reason="end_turn") — final
 *   Not all entries appear in every turn. Multi-tool calls produce separate entries
 *   (first with stop=null, last with stop="tool_use").
 *
 * ### Status mapping:
 *   - assistant + content has tool_use  → "running"  (tool call in progress)
 *   - assistant + content has thinking  → "running"  (model is reasoning)
 *   - assistant + stop_reason=null      → "running"  (streaming, more entries coming)
 *   - assistant + stop_reason=end_turn  → "done"     (turn complete)
 *   - user + content is tool_result     → "running"  (tool executed, next turn coming)
 *   - user + text matches interrupt     → "interrupted" (user pressed Esc or Ctrl+C)
 *   - user + text matches /exit command → "done"     (user quit the session)
 *   - user + normal text                → "running"  (new prompt submitted)
 *
 * ### Termination scenarios:
 *   - Normal completion: last entry is assistant with stop_reason="end_turn"
 *   - Headless completion: same, followed by `last-prompt` (ignored)
 *   - User interrupt (Escape): writes user entry with "[Request interrupted by user...]"
 *   - SIGINT (Ctrl+C): writes `last-prompt` then user with "[Request interrupted by user]"
 *   - SIGKILL / crash: NO entry written — file just stops growing
 *   - /exit command: writes user entries with XML command markup
 *   - Idle between turns: last entry is assistant stop=end_turn or file-history-snapshot
 *
 * ### Permission prompt detection:
 *   When Claude awaits permission, the last entry is assistant with tool_use
 *   and the file stops growing. After TOOL_USE_WAIT_MS with no growth,
 *   we promote "running" → "waiting".
 *
 * ### Stuck process detection:
 *   If status is "running" or "waiting" and the file hasn't grown for
 *   STUCK_RUNNING_MS, we assume the process died and emit "done".
 */

import { watch, appendFileSync, type FSWatcher } from "fs";
import { readdir, stat } from "fs/promises";
import { join, basename } from "path";
import { homedir } from "os";

// Temporary debug logging — writes to same file as server
function dbg(tag: string, msg: string, data?: Record<string, unknown>) {
  const ts = new Date().toISOString().slice(11, 23);
  const suffix = data ? " " + JSON.stringify(data) : "";
  try { appendFileSync("/tmp/opensessions-debug.log", `[${ts}] [cc-watcher:${tag}] ${msg}${suffix}\n`); } catch {}
}
import type { AgentStatus } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../../contracts/agent-watcher";

// --- Types ---

interface ContentItem {
  type?: string;
  text?: string;
}

interface JournalEntry {
  type?: string;
  message?: {
    role?: string;
    stop_reason?: string | null;
    content?: ContentItem[] | string;
  };
}

interface SessionState {
  status: AgentStatus;
  fileSize: number;
  threadName?: string;
  projectDir?: string;
  /** Timestamp when status first became "running" from a tool_use entry */
  toolUseSeenAt?: number;
  /** Timestamp when the file was last observed to have grown (for stuck detection) */
  lastGrowthAt?: number;
}

const POLL_MS = 2000;
const STALE_MS = 5 * 60 * 1000;
/** How long to wait before promoting tool_use "running" → "waiting" (permission prompt heuristic) */
const TOOL_USE_WAIT_MS = 3000;
/** How long a "running" session can go without file growth before we assume the process died */
const STUCK_RUNNING_MS = 15_000;

// --- Interrupt / exit detection patterns ---

const INTERRUPT_PATTERNS = [
  "[Request interrupted by user",
  "[Request interrupted",
];

const EXIT_COMMAND_PATTERN = "<command-name>/exit</command-name>";
/** Slash commands like /vim, /clear, /model write entries with XML markup — not agent activity */
const SLASH_COMMAND_PATTERN = "<command-name>/";
const LOCAL_COMMAND_CAVEAT = "<local-command-caveat>";

// --- Status detection ---

/**
 * Returns the status implied by a journal entry, or `null` if the entry
 * is a control/metadata record that should not change the current status.
 *
 * Control entries (no message.role): queue-operation, file-history-snapshot, last-prompt
 */
export function determineStatus(entry: JournalEntry): AgentStatus | null {
  const msg = entry.message;
  if (!msg?.role) return null;

  const content = msg.content;
  const items: ContentItem[] = Array.isArray(content)
    ? content
    : typeof content === "string"
      ? [{ type: "text", text: content }]
      : [];

  if (msg.role === "assistant") {
    // tool_use → running (tool call pending or executing)
    if (items.some((c) => c.type === "tool_use")) return "running";
    // thinking → running (model is reasoning, more entries will follow)
    if (items.some((c) => c.type === "thinking")) return "running";
    // Streaming partial (stop_reason is null/absent) → still running
    if (!msg.stop_reason) return "running";
    // Finished turn
    if (msg.stop_reason === "end_turn") return "done";
    // tool_use stop_reason without tool_use content = edge case, treat as running
    if (msg.stop_reason === "tool_use") return "running";
    // Any other stop reason (max_tokens, etc.)
    return "done";
  }

  if (msg.role === "user") {
    // Check for interrupt markers written by Escape/SIGINT
    const text = typeof content === "string"
      ? content
      : items.find((c) => c.type === "text" && c.text)?.text;

    if (text) {
      if (INTERRUPT_PATTERNS.some((p) => text.startsWith(p))) return "interrupted";
      // /exit command → done
      if (text.includes(EXIT_COMMAND_PATTERN)) return "done";
      // Slash commands (/vim, /clear, /model, etc.) and their caveats → skip
      if (text.includes(SLASH_COMMAND_PATTERN) || text.startsWith(LOCAL_COMMAND_CAVEAT)) return null;
    }

    // tool_result → running (tool just executed, next turn coming)
    if (items.some((c) => c.type === "tool_result")) return "running";

    // Normal user message → running (new prompt)
    return "running";
  }

  return null;
}

/** Returns true if the entry is an assistant message containing a tool_use block */
export function isToolUseEntry(entry: JournalEntry): boolean {
  const msg = entry.message;
  if (msg?.role !== "assistant") return false;
  const content = msg.content;
  if (!Array.isArray(content)) return false;
  return content.some((c) => c.type === "tool_use");
}

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
  // Skip system/internal messages, interrupt markers, and command outputs
  if (text.startsWith("<") || text.startsWith("{") || text.startsWith("[Request")) return undefined;
  return text.slice(0, 80);
}

/** Encode a path the same way Claude Code does (replace /, ., _ with -) */
function encodeProjectDir(path: string): string {
  return path.replace(/[/._]/g, "-");
}

/**
 * Decode Claude's encoded project dir name back to a path.
 *
 * The encoding is ambiguous: both path separators and literal hyphens
 * become `-`. We try the naive decode first, then check if the
 * directory exists. If not, we leave the encoded form and rely on
 * resolveSession to match via encodeProjectDir on session dirs.
 */
function decodeProjectDir(encoded: string): string {
  const naive = encoded.replace(/-/g, "/");
  // Fast path: if the naive decode is a real directory, use it
  try { if (require("fs").statSync(naive).isDirectory()) return naive; } catch {}
  // Return the raw encoded form — resolveSession will match by encoding session dirs
  return `__encoded__:${encoded}`;
}

// --- Watcher implementation ---

export class ClaudeCodeAgentWatcher implements AgentWatcher {
  readonly name = "claude-code";

  private sessions = new Map<string, SessionState>();
  private fsWatchers: FSWatcher[] = [];
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private ctx: AgentWatcherContext | null = null;
  private projectsDir: string;
  private scanning = false;
  private seeded = false;

  constructor() {
    this.projectsDir = join(homedir(), ".claude", "projects");
  }

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    this.setupWatchers();
    setTimeout(() => this.scan(), 50);
    this.pollTimer = setInterval(() => this.scan(), POLL_MS);
  }

  stop(): void {
    for (const w of this.fsWatchers) { try { w.close(); } catch {} }
    this.fsWatchers = [];
    if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    this.ctx = null;
  }

  /** Emit a status change event if we have a valid session mapping */
  private emitStatus(threadId: string, state: SessionState): void {
    if (!this.ctx || !this.seeded || !state.projectDir) {
      dbg("emitStatus", "bail-early", { hasCtx: !!this.ctx, seeded: this.seeded, hasDir: !!state.projectDir });
      return;
    }
    const session = this.ctx.resolveSession(state.projectDir);
    if (!session) {
      dbg("emitStatus", "no-session-match", { projectDir: state.projectDir, status: state.status });
      return;
    }
    dbg("emitStatus", "emitting", { session, status: state.status, threadId: threadId.slice(0, 8) });
    this.ctx.emit({
      agent: "claude-code",
      session,
      status: state.status,
      ts: Date.now(),
      threadId,
      threadName: state.threadName,
    });
  }

  private async processFile(filePath: string, projectDir: string): Promise<void> {
    if (!this.ctx) return;

    let size: number;
    let mtimeMs: number;
    try { const s = await stat(filePath); size = s.size; mtimeMs = s.mtimeMs; } catch { return; }

    const threadId = basename(filePath, ".jsonl");
    const prev = this.sessions.get(threadId);

    // --- File unchanged ---
    if (prev && size === prev.fileSize) {
      const now = Date.now();

      // Promote tool_use "running" → "waiting" (permission prompt heuristic)
      if (prev.status === "running" && prev.toolUseSeenAt && now - prev.toolUseSeenAt >= TOOL_USE_WAIT_MS) {
        prev.status = "waiting";
        prev.toolUseSeenAt = undefined;
        this.emitStatus(threadId, prev);
      }

      // Stuck detection: no file growth while running/waiting → assume process died
      if ((prev.status === "running" || prev.status === "waiting") && prev.lastGrowthAt && now - prev.lastGrowthAt >= STUCK_RUNNING_MS) {
        prev.status = "done";
        prev.toolUseSeenAt = undefined;
        prev.lastGrowthAt = undefined;
        this.emitStatus(threadId, prev);
      }

      return;
    }

    // --- Seed mode: read full file to capture current status ---
    if (!this.seeded) {
      let text: string;
      try {
        text = await Bun.file(filePath).text();
      } catch { return; }

      const lines = text.split("\n").filter(Boolean);
      let latestStatus: AgentStatus = "idle";
      let threadName: string | undefined;
      let lastEntryIsToolUse = false;

      for (const line of lines) {
        let entry: JournalEntry;
        try { entry = JSON.parse(line); } catch { continue; }
        if (!threadName) {
          const name = extractThreadName(entry);
          if (name) threadName = name;
        }
        const s = determineStatus(entry);
        if (s !== null) latestStatus = s;
        lastEntryIsToolUse = isToolUseEntry(entry);
      }

      this.sessions.set(threadId, {
        status: latestStatus, fileSize: size, threadName, projectDir,
        toolUseSeenAt: lastEntryIsToolUse && latestStatus === "running" ? mtimeMs : undefined,
        lastGrowthAt: (latestStatus === "running" || latestStatus === "waiting") ? mtimeMs : undefined,
      });
      return;
    }

    // --- Incremental read: only new bytes ---
    const offset = prev?.fileSize ?? 0;
    if (size <= offset) return;

    let text: string;
    try {
      const buf = await Bun.file(filePath).arrayBuffer();
      text = new TextDecoder().decode(new Uint8Array(buf).subarray(offset, size));
    } catch {
      return;
    }

    const lines = text.split("\n").filter(Boolean);
    let latestStatus: AgentStatus = prev?.status ?? "idle";
    let threadName = prev?.threadName;
    let lastEntryIsToolUse = false;

    for (const line of lines) {
      let entry: JournalEntry;
      try { entry = JSON.parse(line); } catch { continue; }

      if (!threadName) {
        const name = extractThreadName(entry);
        if (name) threadName = name;
      }

      const s = determineStatus(entry);
      if (s !== null) latestStatus = s;
      lastEntryIsToolUse = isToolUseEntry(entry);
    }

    const prevStatus = prev?.status;
    const now = Date.now();
    const toolUseSeenAt = lastEntryIsToolUse && latestStatus === "running" ? now : undefined;
    this.sessions.set(threadId, { status: latestStatus, fileSize: size, threadName, projectDir, toolUseSeenAt, lastGrowthAt: now });

    if (latestStatus !== prevStatus) {
      this.emitStatus(threadId, this.sessions.get(threadId)!);
    }
  }

  private async scan(): Promise<void> {
    dbg("scan", "enter", { scanning: this.scanning, hasCtx: !!this.ctx, seeded: this.seeded });
    if (this.scanning || !this.ctx) return;
    this.scanning = true;

    try {
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
          await this.processFile(filePath, projectDir);
        }
      }
    } finally {
      if (!this.seeded) {
        this.seeded = true;
        dbg("seed", "complete", { sessionCount: this.sessions.size });
        // Emit seeded sessions with non-idle status (like amp watcher does)
        for (const [threadId, state] of this.sessions) {
          dbg("seed", "check", { threadId: threadId.slice(0, 8), status: state.status, projectDir: state.projectDir });
          if (state.status === "idle" || !state.projectDir) continue;
          const session = this.ctx?.resolveSession(state.projectDir);
          dbg("seed", "resolve", { projectDir: state.projectDir, session: session ?? "NULL" });
          if (!session) continue;
          this.ctx?.emit({
            agent: "claude-code",
            session,
            status: state.status,
            ts: Date.now(),
            threadId,
            threadName: state.threadName,
          });
        }
      }
      this.scanning = false;
    }
  }

  private setupWatchers(): void {
    let dirs: string[];
    try { dirs = require("fs").readdirSync(this.projectsDir); } catch { return; }

    for (const dir of dirs) {
      const dirPath = join(this.projectsDir, dir);
      try { if (!require("fs").statSync(dirPath).isDirectory()) continue; } catch { continue; }

      const projectDir = decodeProjectDir(dir);
      try {
        const w = watch(dirPath, (_eventType, filename) => {
          if (!filename?.endsWith(".jsonl")) return;
          this.processFile(join(dirPath, filename), projectDir);
        });
        this.fsWatchers.push(w);
      } catch {}
    }

    // Watch projects dir for new project directories
    try {
      const w = watch(this.projectsDir, (eventType, filename) => {
        if (eventType !== "rename" || !filename) return;
        const dirPath = join(this.projectsDir, filename);
        try { if (!require("fs").statSync(dirPath).isDirectory()) return; } catch { return; }

        const projectDir = decodeProjectDir(filename);
        try {
          const sub = watch(dirPath, (_et, fn) => {
            if (!fn?.endsWith(".jsonl")) return;
            this.processFile(join(dirPath, fn), projectDir);
          });
          this.fsWatchers.push(sub);
        } catch {}
      });
      this.fsWatchers.push(w);
    } catch {}
  }
}
