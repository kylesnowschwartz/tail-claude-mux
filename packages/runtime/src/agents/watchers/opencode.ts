/**
 * OpenCode agent watcher
 *
 * Polls the OpenCode SQLite database (~/.local/share/opencode/opencode.db)
 * to determine agent status and emits events mapped to mux sessions
 * via the `directory` field on each OpenCode session row.
 */

import { existsSync } from "fs";
import { homedir } from "os";
import { join } from "path";
import type { AgentStatus } from "../../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../../contracts/agent-watcher";

// --- Types ---

interface SessionRow {
  id: string;
  title: string | null;
  directory: string;
  time_updated: number;
}

interface MessageRow {
  id: string;
  data: string;
}

interface MessageData {
  role?: string;
  finish?: string;
}

interface PartData {
  type?: string;
}

const POLL_MS = 3000;
const STALE_MS = 5 * 60 * 1000;
/** How long to wait before promoting tool-call "running" → "waiting" (permission prompt heuristic) */
const TOOL_USE_WAIT_MS = 3000;

// --- Status detection ---

export function determineStatus(msg: MessageData | null, parts: PartData[]): AgentStatus {
  if (!msg) return "idle";

  if (msg.role === "assistant") {
    if (msg.finish === "tool-calls") return "running";
    if (parts.some((p) => p.type === "tool")) return "running";
    return "done";
  }

  if (msg.role === "user") return "running";

  return "idle";
}

/** Returns true if the status is "running" due to a tool call (not a user message) */
export function isToolCallRunning(msg: MessageData | null, parts: PartData[]): boolean {
  if (!msg || msg.role !== "assistant") return false;
  return msg.finish === "tool-calls" || parts.some((p) => p.type === "tool");
}

// --- Watcher implementation ---

export class OpenCodeAgentWatcher implements AgentWatcher {
  readonly name = "opencode";

  private sessionTimestamps = new Map<string, number>();
  private sessionStatuses = new Map<string, AgentStatus>();
  /** Tracks when a session first entered "running" from a tool call */
  private toolUseSeenAt = new Map<string, number>();
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private ctx: AgentWatcherContext | null = null;
  private db: any = null;
  private dbPath: string;

  constructor() {
    this.dbPath = process.env.OPENCODE_DB_PATH
      ?? join(homedir(), ".local", "share", "opencode", "opencode.db");
  }

  private polling = false;

  start(ctx: AgentWatcherContext): void {
    this.ctx = ctx;
    setTimeout(() => this.poll(), 50);
    this.pollTimer = setInterval(() => this.poll(), POLL_MS);
  }

  stop(): void {
    if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
    try { this.db?.close(); } catch {}
    this.db = null;
    this.ctx = null;
  }

  private openDb(): boolean {
    if (this.db) return true;
    if (!existsSync(this.dbPath)) return false;
    try {
      // Dynamic import to avoid hard dependency on bun:sqlite at module level
      const { Database } = require("bun:sqlite");
      this.db = new Database(this.dbPath, { readonly: true });
      return true;
    } catch {
      return false;
    }
  }

  private seeded = false;

  private poll(): void {
    if (!this.ctx || this.polling) return;
    this.polling = true;

    try {
      if (!this.openDb()) return;

      let sessions: SessionRow[];
      const staleThreshold = Date.now() - STALE_MS;
      try {
        sessions = this.db.query(
          `SELECT id, title, directory, time_updated FROM session WHERE time_updated > ? ORDER BY time_updated DESC`,
        ).all(staleThreshold);
      } catch {
        try { this.db.close(); } catch {}
        this.db = null;
        return;
      }

      // First poll: record timestamps, then emit current state for recent sessions.
      if (!this.seeded) {
        for (const row of sessions) {
          this.sessionTimestamps.set(row.id, row.time_updated);
        }
        this.seeded = true;

        // Emit seeded sessions by reading their current status (like amp watcher does)
        for (const row of sessions) {
          let lastMsg: MessageRow | null = null;
          let lastParts: PartData[] = [];
          try {
            lastMsg = this.db.query(
              `SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`,
            ).get(row.id);
            if (lastMsg) {
              const partRows: { data: string }[] = this.db.query(
                `SELECT data FROM part WHERE message_id = ? ORDER BY time_created ASC`,
              ).all(lastMsg.id);
              for (const pr of partRows) {
                try { lastParts.push(JSON.parse(pr.data)); } catch {}
              }
            }
          } catch { continue; }

          let lastMsgData: MessageData | null = null;
          if (lastMsg) {
            try { lastMsgData = JSON.parse(lastMsg.data); } catch {}
          }

          const status = determineStatus(lastMsgData, lastParts);
          if (status === "idle") continue;
          this.sessionStatuses.set(row.id, status);
          if (isToolCallRunning(lastMsgData, lastParts)) {
            this.toolUseSeenAt.set(row.id, Date.now());
          }

          const session = this.ctx.resolveSession(row.directory);
          if (!session) continue;

          this.ctx.emit({
            agent: "opencode",
            session,
            status,
            ts: Date.now(),
            threadId: row.id,
            ...(row.title && { threadName: row.title }),
          });
        }
        return;
      }

      for (const row of sessions) {
        const prev = this.sessionTimestamps.get(row.id);
        if (prev === row.time_updated) continue;
        this.sessionTimestamps.set(row.id, row.time_updated);

        // Only emit if we had a previous timestamp — a change means
        // OpenCode is actively writing to this session right now.
        if (prev === undefined) continue;

        let lastMsg: MessageRow | null = null;
        let lastParts: PartData[] = [];
        try {
          lastMsg = this.db.query(
            `SELECT id, data FROM message WHERE session_id = ? ORDER BY time_created DESC LIMIT 1`,
          ).get(row.id);

          if (lastMsg) {
            const partRows: { data: string }[] = this.db.query(
              `SELECT data FROM part WHERE message_id = ? ORDER BY time_created ASC`,
            ).all(lastMsg.id);
            for (const pr of partRows) {
              try { lastParts.push(JSON.parse(pr.data)); } catch {}
            }
          }
        } catch {
          continue;
        }

        let lastMsgData: MessageData | null = null;
        if (lastMsg) {
          try { lastMsgData = JSON.parse(lastMsg.data); } catch {}
        }

        const status = determineStatus(lastMsgData, lastParts);
        const prevStatus = this.sessionStatuses.get(row.id);
        if (prevStatus === status) continue;
        this.sessionStatuses.set(row.id, status);

        if (isToolCallRunning(lastMsgData, lastParts)) {
          this.toolUseSeenAt.set(row.id, Date.now());
        } else {
          this.toolUseSeenAt.delete(row.id);
        }

        const session = this.ctx.resolveSession(row.directory);
        if (!session) continue;

        this.ctx.emit({
          agent: "opencode",
          session,
          status,
          ts: Date.now(),
          threadId: row.id,
          ...(row.title && { threadName: row.title }),
        });
      }

      // Promote stale tool-call "running" → "waiting"
      const now = Date.now();
      const sessionDirMap = new Map(sessions.map((s) => [s.id, s]));
      for (const [id, seenAt] of this.toolUseSeenAt) {
        if (now - seenAt < TOOL_USE_WAIT_MS) continue;
        const prevStatus = this.sessionStatuses.get(id);
        if (prevStatus !== "running") { this.toolUseSeenAt.delete(id); continue; }

        this.sessionStatuses.set(id, "waiting");
        this.toolUseSeenAt.delete(id);

        const row = sessionDirMap.get(id);
        if (!row) continue;
        const session = this.ctx.resolveSession(row.directory);
        if (!session) continue;

        this.ctx.emit({
          agent: "opencode",
          session,
          status: "waiting",
          ts: now,
          threadId: id,
          ...(row.title && { threadName: row.title }),
        });
      }
    } finally {
      this.polling = false;
    }
  }
}
