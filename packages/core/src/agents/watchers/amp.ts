/**
 * Amp agent watcher
 *
 * Watches ~/.local/share/amp/threads/ for JSON file changes,
 * determines agent status from the last message, and emits events
 * mapped to mux sessions via the project directory in each thread.
 */

import { readdirSync, readFileSync, statSync, watch, type FSWatcher } from "fs";
import { join, basename } from "path";
import { homedir } from "os";
import type { AgentStatus } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../../contracts/agent-watcher";

// --- Thread file types ---

interface MessageState {
  type?: "complete" | "cancelled" | "streaming";
  stopReason?: "end_turn" | "tool_use";
}

interface Message {
  role?: string;
  state?: MessageState;
}

interface ThreadSnapshot {
  status: AgentStatus;
  version: number;
  title?: string;
  projectDir?: string;
}

const STALE_MS = 5 * 60 * 1000;
const POLL_MS = 2000;

// --- Status detection ---

export function determineStatus(lastMsg: { role?: string; state?: MessageState } | null): AgentStatus {
  if (!lastMsg?.role) return "idle";

  if (lastMsg.role === "user") return "running";

  if (lastMsg.role === "assistant") {
    if (!lastMsg.state) return "running";
    if (lastMsg.state.type === "streaming") return "running";
    if (lastMsg.state.type === "cancelled") return "interrupted";
    if (lastMsg.state.type === "complete") {
      if (lastMsg.state.stopReason === "tool_use") return "running";
      if (lastMsg.state.stopReason === "end_turn") return "done";
    }
    return "waiting";
  }

  return "idle";
}

// --- Thread file parsing ---

interface ParsedThread {
  version: number;
  title?: string;
  projectDir?: string;
  lastMessage: Message | null;
}

function parseThreadFile(filePath: string): ParsedThread | null {
  try {
    const raw = readFileSync(filePath, "utf-8");
    const thread = JSON.parse(raw);
    const messages = thread.messages ?? [];
    const lastMsg = messages.length > 0 ? messages[messages.length - 1] : null;
    const uri: string = thread.env?.initial?.trees?.[0]?.uri ?? "";
    const projectDir = uri.startsWith("file://") ? uri.slice(7) : undefined;

    return {
      version: thread.v ?? 0,
      title: thread.title || undefined,
      projectDir,
      lastMessage: lastMsg ? { role: lastMsg.role, state: lastMsg.state } : null,
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
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private ctx: AgentWatcherContext | null = null;
  private threadsDir: string;

  constructor() {
    this.threadsDir = join(homedir(), ".local", "share", "amp", "threads");
  }

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    this.scan();
    this.setupWatch();
    this.pollTimer = setInterval(() => this.scan(), POLL_MS);
  }

  stop(): void {
    if (this.fsWatcher) { try { this.fsWatcher.close(); } catch {} this.fsWatcher = null; }
    if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    this.ctx = null;
  }

  private processThread(filePath: string): void {
    if (!this.ctx) return;
    try { statSync(filePath); } catch { return; }

    const threadId = basename(filePath, ".json");
    const prev = this.threads.get(threadId);

    const parsed = parseThreadFile(filePath);
    if (!parsed) return;

    if (prev && parsed.version === prev.version) return;

    const status = determineStatus(parsed.lastMessage);
    const session = parsed.projectDir
      ? this.ctx.resolveSession(parsed.projectDir) ?? "unknown"
      : "unknown";

    const prevStatus = prev?.status;
    this.threads.set(threadId, {
      status,
      version: parsed.version,
      title: parsed.title,
      projectDir: parsed.projectDir,
    });

    if (status !== prevStatus && session !== "unknown") {
      this.ctx.emit({
        agent: "amp",
        session,
        status,
        ts: Date.now(),
        threadId,
        threadName: parsed.title,
      });
    }
  }

  private scan(): void {
    let files: string[];
    try { files = readdirSync(this.threadsDir); } catch { return; }

    const now = Date.now();
    for (const file of files) {
      if (!file.startsWith("T-") || !file.endsWith(".json")) continue;
      const filePath = join(this.threadsDir, file);
      let stat;
      try { stat = statSync(filePath); } catch { continue; }
      if (now - stat.mtimeMs > STALE_MS) continue;
      this.processThread(filePath);
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
}
