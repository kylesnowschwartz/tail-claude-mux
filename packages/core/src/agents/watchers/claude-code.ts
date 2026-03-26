/**
 * Claude Code agent watcher
 *
 * Watches ~/.claude/projects/ for JSONL file changes,
 * determines agent status from journal entries, and emits events
 * mapped to mux sessions via the project directory encoded in folder names.
 *
 * Directory structure: ~/.claude/projects/<encoded-path>/<session-id>.jsonl
 * Encoded path: /Users/foo/myproject → -Users-foo-myproject
 */

import { readdirSync, readFileSync, statSync, watch, type FSWatcher } from "fs";
import { join, basename } from "path";
import { homedir } from "os";
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
    content?: ContentItem[] | string;
  };
}

interface SessionState {
  status: AgentStatus;
  fileSize: number;
  threadName?: string;
  projectDir?: string;
}

const POLL_MS = 2000;

// --- Status detection ---

export function determineStatus(entry: JournalEntry): AgentStatus {
  const msg = entry.message;
  if (!msg?.role) return "idle";

  const content = msg.content;
  const items: ContentItem[] = Array.isArray(content)
    ? content
    : typeof content === "string"
      ? [{ type: "text", text: content }]
      : [];

  if (msg.role === "assistant") {
    const hasToolUse = items.some((c) => c.type === "tool_use");
    return hasToolUse ? "running" : "waiting";
  }

  if (msg.role === "user") return "running";

  return "idle";
}

function extractThreadName(entry: JournalEntry): string | undefined {
  const msg = entry.message;
  if (msg?.role !== "user") return undefined;

  const content = msg.content;
  if (typeof content === "string") return content.slice(0, 80);

  if (Array.isArray(content)) {
    const textItem = content.find((c) => c.type === "text" && c.text);
    return textItem?.text?.slice(0, 80);
  }

  return undefined;
}

/** Decode Claude's encoded project dir name back to a path */
function decodeProjectDir(encoded: string): string {
  // "-Users-foo-myproject" → "/Users/foo/myproject"
  return encoded.replace(/-/g, "/");
}

// --- Watcher implementation ---

export class ClaudeCodeAgentWatcher implements AgentWatcher {
  readonly name = "claude-code";

  private sessions = new Map<string, SessionState>();
  private fsWatchers: FSWatcher[] = [];
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private ctx: AgentWatcherContext | null = null;
  private projectsDir: string;

  constructor() {
    this.projectsDir = join(homedir(), ".claude", "projects");
  }

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    this.scan();
    this.setupWatchers();
    this.pollTimer = setInterval(() => this.scan(), POLL_MS);
  }

  stop(): void {
    for (const w of this.fsWatchers) { try { w.close(); } catch {} }
    this.fsWatchers = [];
    if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    this.ctx = null;
  }

  private processFile(filePath: string, projectDir: string): void {
    if (!this.ctx) return;

    let size: number;
    try { size = statSync(filePath).size; } catch { return; }

    const threadId = basename(filePath, ".jsonl");
    const prev = this.sessions.get(threadId);

    if (prev && size === prev.fileSize) return;

    const offset = prev?.fileSize ?? 0;
    if (size <= offset) return;

    let text: string | null;
    try {
      const raw = readFileSync(filePath);
      text = new TextDecoder().decode(raw.subarray(offset, size));
    } catch {
      return;
    }

    const lines = text.split("\n").filter(Boolean);
    let latestStatus: AgentStatus = prev?.status ?? "idle";
    let threadName = prev?.threadName;

    for (const line of lines) {
      let entry: JournalEntry;
      try { entry = JSON.parse(line); } catch { continue; }

      if (!threadName) {
        const name = extractThreadName(entry);
        if (name) threadName = name;
      }

      latestStatus = determineStatus(entry);
    }

    const prevStatus = prev?.status;
    this.sessions.set(threadId, { status: latestStatus, fileSize: size, threadName, projectDir });

    if (latestStatus !== prevStatus) {
      const session = this.ctx.resolveSession(projectDir);
      if (session) {
        this.ctx.emit({
          agent: "claude-code",
          session,
          status: latestStatus,
          ts: Date.now(),
          threadId,
          threadName,
        });
      }
    }
  }

  private scan(): void {
    let dirs: string[];
    try { dirs = readdirSync(this.projectsDir); } catch { return; }

    for (const dir of dirs) {
      const dirPath = join(this.projectsDir, dir);
      try { if (!statSync(dirPath).isDirectory()) continue; } catch { continue; }

      const projectDir = decodeProjectDir(dir);

      let files: string[];
      try { files = readdirSync(dirPath); } catch { continue; }

      for (const file of files) {
        if (!file.endsWith(".jsonl")) continue;
        this.processFile(join(dirPath, file), projectDir);
      }
    }
  }

  private setupWatchers(): void {
    let dirs: string[];
    try { dirs = readdirSync(this.projectsDir); } catch { return; }

    for (const dir of dirs) {
      const dirPath = join(this.projectsDir, dir);
      try { if (!statSync(dirPath).isDirectory()) continue; } catch { continue; }

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
        try { if (!statSync(dirPath).isDirectory()) return; } catch { return; }

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
