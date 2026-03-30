/**
 * Codex agent watcher
 *
 * Watches Codex transcript files under ~/.codex/sessions/ (or $CODEX_HOME/sessions),
 * determines agent status from the latest transcript events, and emits events
 * mapped to mux sessions via the working directory captured in turn_context.
 *
 * Detection uses a recursive fs.watch when available plus a periodic poll to
 * catch missed writes and new files.
 */

import { watch, type FSWatcher } from "fs";
import { readdir, stat } from "fs/promises";
import { homedir } from "os";
import { basename, join } from "path";
import type { AgentStatus } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../../contracts/agent-watcher";

interface CodexEntry {
  type?: string;
  payload?: {
    type?: string;
    role?: string;
    phase?: string;
    cwd?: string;
    message?: string;
    content?: Array<{ type?: string; text?: string }>;
  };
}

interface SessionSnapshot {
  status: AgentStatus;
  fileSize: number;
  projectDir?: string;
  threadName?: string;
  /** Timestamp when status first became "running" from a function_call entry */
  toolUseSeenAt?: number;
}

const POLL_MS = 2000;
const STALE_MS = 5 * 60 * 1000;
const THREAD_NAME_MAX = 80;
/** How long to wait before promoting function_call "running" → "waiting" (permission prompt heuristic) */
const TOOL_USE_WAIT_MS = 3000;

function assistantStatus(phase?: string): AgentStatus {
  return phase === "commentary" ? "running" : "done";
}

export function determineStatus(entry: CodexEntry): AgentStatus | null {
  const payload = entry.payload;
  if (!payload) return null;

  if (entry.type === "event_msg") {
    switch (payload.type) {
      case "task_complete":
        return "done";
      case "turn_aborted":
        return "interrupted";
      case "user_message":
        return "running";
      case "agent_message":
        return assistantStatus(payload.phase);
      case "error":
        return "error";
      default:
        return null;
    }
  }

  if (entry.type === "response_item") {
    if (payload.type === "message") {
      if (payload.role === "user") return "running";
      if (payload.role === "assistant") return assistantStatus(payload.phase);
      return null;
    }

    if (payload.type === "function_call" || payload.type === "function_call_output" || payload.type === "reasoning") {
      return "running";
    }
  }

  return null;
}

/** Returns true if the entry is a function_call (tool invocation that may need permission) */
export function isToolCallEntry(entry: CodexEntry): boolean {
  return entry.type === "response_item" && entry.payload?.type === "function_call";
}

function parseThreadId(filePath: string): string {
  const name = basename(filePath, ".jsonl");
  return name.match(/[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i)?.[0] ?? name;
}

function normalizeThreadName(text: string | undefined): string | undefined {
  if (!text) return undefined;
  const line = text
    .split("\n")
    .map((part) => part.trim())
    .find(Boolean);
  return line ? line.slice(0, THREAD_NAME_MAX) : undefined;
}

function extractThreadName(entry: CodexEntry): string | undefined {
  const payload = entry.payload;
  if (!payload) return undefined;

  if (entry.type === "event_msg" && payload.type === "user_message") {
    return normalizeThreadName(payload.message);
  }

  if (entry.type === "response_item" && payload.type === "message" && payload.role === "user") {
    const text = Array.isArray(payload.content)
      ? payload.content
          .filter((item) => item?.type === "input_text")
          .map((item) => item.text ?? "")
          .join("\n")
      : undefined;
    const candidate = normalizeThreadName(text);
    if (!candidate) return undefined;
    if (candidate.startsWith("# AGENTS.md") || candidate.startsWith("<environment_context>")) return undefined;
    return candidate;
  }

  return undefined;
}

function applyEntries(text: string, base: SessionSnapshot, indexedThreadName?: string): SessionSnapshot {
  let status = base.status;
  let projectDir = base.projectDir;
  let threadName = indexedThreadName ?? base.threadName;
  let lastEntryIsToolCall = false;

  for (const rawLine of text.split("\n")) {
    if (!rawLine.trim()) continue;

    let entry: CodexEntry;
    try {
      entry = JSON.parse(rawLine);
    } catch {
      continue;
    }

    if (!projectDir && entry.type === "turn_context" && typeof entry.payload?.cwd === "string") {
      projectDir = entry.payload.cwd;
    }

    if (!threadName) {
      threadName = extractThreadName(entry);
    }

    const nextStatus = determineStatus(entry);
    if (nextStatus) {
      status = nextStatus;
      lastEntryIsToolCall = isToolCallEntry(entry);
    }
  }

  return {
    ...base, status, projectDir, threadName,
    toolUseSeenAt: lastEntryIsToolCall && status === "running" ? Date.now() : undefined,
  };
}

async function collectSessionFiles(dir: string): Promise<string[]> {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return [];
  }

  const files: string[] = [];
  for (const entry of entries) {
    const fullPath = join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...await collectSessionFiles(fullPath));
      continue;
    }
    if (entry.isFile() && entry.name.endsWith(".jsonl")) {
      files.push(fullPath);
    }
  }

  return files;
}

export class CodexAgentWatcher implements AgentWatcher {
  readonly name = "codex";

  private sessions = new Map<string, SessionSnapshot>();
  private threadNames = new Map<string, string>();
  private fsWatcher: FSWatcher | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private ctx: AgentWatcherContext | null = null;
  private sessionsDir: string;
  private sessionIndexFile: string;
  private scanning = false;
  private seeded = false;

  constructor() {
    const codexHome = process.env.CODEX_HOME ?? join(homedir(), ".codex");
    this.sessionsDir = join(codexHome, "sessions");
    this.sessionIndexFile = join(codexHome, "session_index.jsonl");
  }

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    this.setupWatch();
    setTimeout(() => this.scan(), 50);
    this.pollTimer = setInterval(() => this.scan(), POLL_MS);
  }

  stop(): void {
    if (this.fsWatcher) { try { this.fsWatcher.close(); } catch {} this.fsWatcher = null; }
    if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    this.ctx = null;
  }

  private async loadThreadIndex(): Promise<void> {
    let text: string;
    try {
      text = await Bun.file(this.sessionIndexFile).text();
    } catch {
      return;
    }

    const names = new Map<string, string>();
    for (const line of text.split("\n")) {
      if (!line.trim()) continue;
      try {
        const entry = JSON.parse(line) as { id?: string; thread_name?: string };
        if (entry.id && entry.thread_name) {
          names.set(entry.id, entry.thread_name);
        }
      } catch {
      }
    }

    this.threadNames = names;
  }

  private async processFile(filePath: string): Promise<void> {
    if (!this.ctx) return;

    let fileStat;
    try {
      fileStat = await stat(filePath);
    } catch {
      return;
    }

    const threadId = parseThreadId(filePath);
    const prev = this.sessions.get(threadId);

    if (prev && fileStat.size === prev.fileSize) {
      // File unchanged — check if we should promote function_call "running" → "waiting"
      if (prev.status === "running" && prev.toolUseSeenAt && Date.now() - prev.toolUseSeenAt >= TOOL_USE_WAIT_MS) {
        prev.status = "waiting";
        prev.toolUseSeenAt = undefined;
        if (this.seeded && prev.projectDir) {
          const session = this.ctx.resolveSession(prev.projectDir);
          if (session) {
            this.ctx.emit({
              agent: "codex",
              session,
              status: "waiting",
              ts: Date.now(),
              threadId,
              ...(prev.threadName && { threadName: prev.threadName }),
            });
          }
        }
      }
      return;
    }

    const indexedThreadName = this.threadNames.get(threadId);
    let nextSnapshot: SessionSnapshot;

    if (prev && fileStat.size > prev.fileSize) {
      let text: string;
      try {
        const buf = await Bun.file(filePath).arrayBuffer();
        text = new TextDecoder().decode(new Uint8Array(buf).subarray(prev.fileSize, fileStat.size));
      } catch {
        return;
      }

      nextSnapshot = applyEntries(text, { ...prev, fileSize: fileStat.size }, indexedThreadName);
    } else {
      let text: string;
      try {
        text = await Bun.file(filePath).text();
      } catch {
        return;
      }

      nextSnapshot = applyEntries(text, { status: "idle", fileSize: fileStat.size }, indexedThreadName);
    }

    this.sessions.set(threadId, nextSnapshot);

    if (!this.seeded) return;

    const prevStatus = prev?.status;
    if (nextSnapshot.status === prevStatus) return;

    const session = nextSnapshot.projectDir ? this.ctx.resolveSession(nextSnapshot.projectDir) : null;
    if (!session) return;
    if (!prev && nextSnapshot.status === "idle") return;

    this.ctx.emit({
      agent: "codex",
      session,
      status: nextSnapshot.status,
      ts: Date.now(),
      threadId,
      ...(nextSnapshot.threadName && { threadName: nextSnapshot.threadName }),
    });
  }

  private async scan(): Promise<void> {
    if (this.scanning || !this.ctx) return;
    this.scanning = true;

    try {
      await this.loadThreadIndex();

      const files = await collectSessionFiles(this.sessionsDir);
      const now = Date.now();

      for (const filePath of files) {
        let fileStat;
        try {
          fileStat = await stat(filePath);
        } catch {
          continue;
        }

        if (now - fileStat.mtimeMs > STALE_MS) continue;
        await this.processFile(filePath);
      }
    } finally {
      if (!this.seeded) {
        this.seeded = true;
        // Emit seeded sessions with non-idle status (like amp watcher does)
        for (const [threadId, snapshot] of this.sessions) {
          if (snapshot.status === "idle" || !snapshot.projectDir) continue;
          const session = this.ctx?.resolveSession(snapshot.projectDir);
          if (!session) continue;
          this.ctx?.emit({
            agent: "codex",
            session,
            status: snapshot.status,
            ts: Date.now(),
            threadId,
            ...(snapshot.threadName && { threadName: snapshot.threadName }),
          });
        }
      }
      this.scanning = false;
    }
  }

  private setupWatch(): void {
    try {
      this.fsWatcher = watch(this.sessionsDir, { recursive: true }, (_eventType, filename) => {
        if (!filename?.endsWith(".jsonl")) return;
        this.processFile(join(this.sessionsDir, filename));
      });
    } catch {
    }
  }
}
