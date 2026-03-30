/**
 * Amp agent watcher
 *
 * Watches ~/.local/share/amp/threads/ for JSON file changes,
 * determines agent status from the last message, and emits events
 * mapped to mux sessions via the project directory in each thread.
 *
 * Also watches ~/.local/share/amp/session.json for thread focus
 * changes — when the user switches threads in Amp, we emit "idle"
 * for terminal threads to clear unseen flags.
 *
 * All file I/O is async to avoid blocking the server event loop.
 *
 * ## Amp Thread JSON Lifecycle (observed v2025-03)
 *
 * Each thread file (~/.local/share/amp/threads/T-*.json) is a full
 * JSON document rewritten atomically on every change.  Top-level
 * fields: `v` (version counter), `id`, `title`, `messages`, `env`.
 * The `env.initial.trees[0].uri` contains the project directory as
 * a `file://` URI.
 *
 * ### Message structure
 *   - role: "user" | "assistant"
 *   - state?: { type: string; stopReason?: string }  (assistant only)
 *   - interrupted?: boolean  (user only — set when user sent a new
 *     message while the agent was still running, causing a cancel)
 *   - content: ContentItem[]  (tool_use, tool_result, text, thinking)
 *
 * ### State types (assistant messages)
 *   - `streaming`  — model is actively generating (thinking, text,
 *     or tool_use content may already be present, grows over time)
 *   - `complete`   — turn finished, check stopReason:
 *       - `end_turn`   → "done"   (final response delivered)
 *       - `tool_use`   → "running" (tool call pending execution)
 *       - other        → "error"   (e.g. max_tokens)
 *   - `cancelled`  — user interrupted the assistant mid-turn
 *     (Escape key or new message while streaming)
 *
 * ### User messages
 *   - role=user with content=[tool_result]  → "running"
 *     (tool just executed, next assistant turn coming)
 *   - role=user with content=[text]  → "running"
 *     (new prompt submitted)
 *   - role=user with interrupted=true  → "running"
 *     (user sent new message while agent was running — the preceding
 *     assistant message will have state.type=cancelled)
 *
 * ### Lifecycle flow
 *   1. Thread created: v=0, msgs=0 (empty)
 *   2. User prompt: role=user, state=null
 *   3. Title set: version bump (title populated)
 *   4. Streaming: role=assistant, state.type=streaming
 *      (content grows: thinking → text → tool_use)
 *   5a. Complete: state.type=complete, stopReason=end_turn → "done"
 *   5b. Tool call: state.type=complete, stopReason=tool_use → "running"
 *   6. Tool result: role=user, content=[tool_result] → "running"
 *   7. Repeat from step 4 for next turn
 *
 * ### Interrupt scenarios
 *   - User presses Escape or sends new message while streaming:
 *     assistant gets state.type=cancelled, followed by user message
 *     (with interrupted=true if it was a new prompt)
 *   - cancelled is the LAST message: thread was abandoned mid-stream
 *
 * ### Process death (SIGKILL / crash)
 *   - Thread file stops being updated. Last message stays in whatever
 *     state it was: typically state.type=streaming (killed mid-generation)
 *     or role=user with tool_result (killed between turns).
 *   - The file mtime stops advancing. After STUCK_MS (15s) of no
 *     mtime changes while in a "running" state, we emit "done".
 *
 * ### session.json
 *   Contains { lastThreadId, lastExecuteThreadId, agentMode, ... }.
 *   When lastThreadId changes, the user focused a different thread
 *   in the Amp UI. If that thread was in a terminal state (done/error/
 *   interrupted), we emit "idle" to clear the unseen flag.
 */

import { watch, type FSWatcher } from "fs";
import { readdir, stat } from "fs/promises";
import { join, basename } from "path";
import { homedir } from "os";
import type { AgentStatus } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../../contracts/agent-watcher";

// --- Thread file types ---

interface MessageState {
  type?: string;
  stopReason?: string;
}

interface Message {
  role?: string;
  state?: MessageState;
  interrupted?: boolean;
  content?: ContentItem[] | string;
}

interface ContentItem {
  type?: string;
}

interface ThreadSnapshot {
  status: AgentStatus;
  version: number;
  title?: string;
  projectDir?: string;
  mtimeMs: number;
  /** Timestamp when we last saw the file grow (mtime advance). For stuck detection. */
  lastGrowthAt?: number;
}

const STALE_MS = 5 * 60 * 1000;
const POLL_MS = 2000;
/** How long a "running" thread can go without file growth before we assume the process died */
const STUCK_MS = 15_000;

// --- Status detection ---

/**
 * Determine the agent status from the last message in a thread.
 *
 * Returns the status implied by the message. Called with the last
 * element of the `messages` array from the thread JSON.
 */
export function determineStatus(lastMsg: { role?: string; state?: MessageState; interrupted?: boolean; content?: ContentItem[] | string } | null): AgentStatus {
  if (!lastMsg?.role) return "idle";

  if (lastMsg.role === "user") return "running";

  if (lastMsg.role === "assistant") {
    const state = lastMsg.state;
    if (!state || !state.type) return "running";

    if (state.type === "streaming") return "running";
    if (state.type === "cancelled") return "interrupted";

    if (state.type === "complete") {
      if (state.stopReason === "tool_use") return "running";
      if (state.stopReason === "end_turn") return "done";
      // Other stop reasons (max_tokens, etc.) are terminal failures.
      return "error";
    }

    // Unknown state type — defensive, treat as running
    return "running";
  }

  return "idle";
}

// --- Async thread file parsing ---

async function parseThreadFile(filePath: string): Promise<{ version: number; title?: string; projectDir?: string; lastMessage: Message | null } | null> {
  try {
    const raw = await Bun.file(filePath).text();
    const thread = JSON.parse(raw);
    const messages = thread.messages ?? [];
    const lastMsg = messages.length > 0 ? messages[messages.length - 1] : null;
    const uri: string = thread.env?.initial?.trees?.[0]?.uri ?? "";
    const projectDir = uri.startsWith("file://") ? uri.slice(7) : undefined;

    return {
      version: thread.v ?? 0,
      title: thread.title || undefined,
      projectDir,
      lastMessage: lastMsg ? { role: lastMsg.role, state: lastMsg.state, interrupted: lastMsg.interrupted, content: lastMsg.content } : null,
    };
  } catch {
    return null;
  }
}

// --- Watcher implementation ---

export class AmpAgentWatcher implements AgentWatcher {
  readonly name = "amp";

  private threads = new Map<string, ThreadSnapshot>();
  private fsWatcher: FSWatcher | null = null;
  private sessionWatcher: FSWatcher | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private ctx: AgentWatcherContext | null = null;
  private threadsDir: string;
  private sessionFile: string;
  private scanning = false;
  private seeded = false;
  private lastFocusedThread: string | null = null;

  constructor() {
    const dataDir = join(homedir(), ".local", "share", "amp");
    this.threadsDir = join(dataDir, "threads");
    this.sessionFile = join(dataDir, "session.json");
  }

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    this.setupWatch();
    this.setupSessionWatch();
    setTimeout(() => this.scan(), 50);
    this.pollTimer = setInterval(() => this.scan(), POLL_MS);
  }

  stop(): void {
    if (this.fsWatcher) { try { this.fsWatcher.close(); } catch {} this.fsWatcher = null; }
    if (this.sessionWatcher) { try { this.sessionWatcher.close(); } catch {} this.sessionWatcher = null; }
    if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    this.ctx = null;
  }

  /** Emit a status change event if we have a valid session mapping */
  private emitStatus(threadId: string, snapshot: ThreadSnapshot): boolean {
    if (!this.ctx || !snapshot.projectDir || snapshot.status === "idle") return false;

    const session = this.ctx.resolveSession(snapshot.projectDir);
    if (!session || session === "unknown") return false;

    this.ctx.emit({
      agent: "amp",
      session,
      status: snapshot.status,
      ts: Date.now(),
      threadId,
      threadName: snapshot.title,
    });
    return true;
  }

  private async processThread(filePath: string): Promise<boolean> {
    if (!this.ctx) return false;

    let fileStat;
    try { fileStat = await stat(filePath); } catch { return false; }

    const threadId = basename(filePath, ".json");
    const prev = this.threads.get(threadId);
    const now = Date.now();

    // Quick mtime check — skip if file hasn't changed since we last saw this version
    if (prev && fileStat.mtimeMs <= prev.mtimeMs) {
      // File unchanged — check for stuck detection
      if (this.seeded && prev.status === "running" && prev.lastGrowthAt && now - prev.lastGrowthAt >= STUCK_MS) {
        prev.status = "done";
        prev.lastGrowthAt = undefined;
        this.emitStatus(threadId, prev);
      }
      return false;
    }

    const parsed = await parseThreadFile(filePath);
    if (!parsed) return false;

    const status = determineStatus(parsed.lastMessage);
    const statusChanged = prev?.status !== status;
    const titleChanged = prev?.title !== parsed.title;
    const projectDirChanged = prev?.projectDir !== parsed.projectDir;

    if (prev && parsed.version === prev.version && !statusChanged && !titleChanged && !projectDirChanged) {
      // Update mtime even if version unchanged to avoid re-reading
      prev.mtimeMs = fileStat.mtimeMs;
      if (prev.status === "running") prev.lastGrowthAt = now;
      return false;
    }

    const snapshot: ThreadSnapshot = {
      status,
      version: parsed.version,
      title: parsed.title,
      projectDir: parsed.projectDir,
      mtimeMs: fileStat.mtimeMs,
      lastGrowthAt: status === "running" ? now : undefined,
    };
    this.threads.set(threadId, snapshot);

    // Seed mode: record state without emitting
    if (!this.seeded) return false;

    return (statusChanged || titleChanged) && this.emitStatus(threadId, snapshot);
  }

  private async scan(): Promise<void> {
    if (this.scanning || !this.ctx) return;
    this.scanning = true;
    const initialSeed = !this.seeded;

    try {
      let files: string[];
      try { files = await readdir(this.threadsDir); } catch { return; }

      const now = Date.now();
      for (const file of files) {
        if (!file.startsWith("T-") || !file.endsWith(".json")) continue;
        const filePath = join(this.threadsDir, file);
        let fileStat;
        try { fileStat = await stat(filePath); } catch { continue; }
        if (now - fileStat.mtimeMs > STALE_MS) continue;
        await this.processThread(filePath);
      }
    } finally {
      if (initialSeed) {
        this.seeded = true;
        for (const [threadId, snapshot] of this.threads) {
          this.emitStatus(threadId, snapshot);
        }
      }
      this.scanning = false;
    }
  }

  private setupWatch(): void {
    try {
      this.fsWatcher = watch(this.threadsDir, (_eventType, filename) => {
        if (!filename?.startsWith("T-") || !filename.endsWith(".json")) return;
        this.processThread(join(this.threadsDir, filename));
      });
    } catch {
      // fs.watch failed; polling handles it
    }
  }

  /** Watch Amp's session.json for lastThreadId changes — thread-level "seen" signal */
  private setupSessionWatch(): void {
    // Seed the initial focused thread
    this.checkSessionFocus();

    try {
      this.sessionWatcher = watch(this.sessionFile, () => {
        this.checkSessionFocus();
      });
    } catch {
      // session.json doesn't exist yet or can't be watched; ignore
    }
  }

  /** Read session.json and emit "idle" for a terminal thread the user has focused in Amp */
  private async checkSessionFocus(): Promise<void> {
    if (!this.ctx || !this.seeded) return;

    try {
      const raw = await Bun.file(this.sessionFile).text();
      const session = JSON.parse(raw);
      const threadId: string | undefined = session.lastThreadId;
      if (!threadId || threadId === this.lastFocusedThread) return;

      this.lastFocusedThread = threadId;

      // If this thread is tracked and in a terminal state, the user just "saw" it
      const snapshot = this.threads.get(threadId);
      if (!snapshot || !snapshot.projectDir) return;
      if (snapshot.status !== "done" && snapshot.status !== "error" && snapshot.status !== "interrupted") return;

      const muxSession = this.ctx.resolveSession(snapshot.projectDir);
      if (!muxSession || muxSession === "unknown") return;

      // Emit "idle" to clear the unseen flag for this specific thread
      this.ctx.emit({
        agent: "amp",
        session: muxSession,
        status: "idle",
        ts: Date.now(),
        threadId,
        threadName: snapshot.title,
      });
    } catch {
      // session.json unreadable; ignore
    }
  }
}
