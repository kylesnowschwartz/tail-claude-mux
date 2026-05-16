#!/usr/bin/env bun
// Spike 3 instrumentation: log Claude Code ~/.claude/sessions/*.json snapshots
// to /tmp/cc-sessions-trace.ndjson. One-off; NOT production code.
// Usage:
//   bun run scripts/spike/cc-sessions-status-logger.ts [--self-check]

import { readdirSync, readFileSync, existsSync, appendFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const SESSIONS_DIR = join(homedir(), ".claude", "sessions");
const TRACE_PATH = "/tmp/cc-sessions-trace.ndjson";
const POLL_MS = 2000;

type Snapshot = {
  pid: number;
  sessionId?: string;
  status?: string;
  updatedAt?: number;
  agent?: string;
  cwd?: string;
};

type LastSeen = {
  status?: string;
  updatedAt?: number;
};

function readSnapshot(pid: number): Snapshot | null {
  const path = join(SESSIONS_DIR, `${pid}.json`);
  try {
    const raw = readFileSync(path, "utf8");
    const obj = JSON.parse(raw);
    return {
      pid: obj.pid ?? pid,
      sessionId: obj.sessionId,
      status: obj.status,
      updatedAt: obj.updatedAt,
      agent: obj.agent,
      cwd: obj.cwd,
    };
  } catch {
    return null;
  }
}

function listPids(): number[] {
  if (!existsSync(SESSIONS_DIR)) return [];
  return readdirSync(SESSIONS_DIR)
    .filter((n) => n.endsWith(".json"))
    .map((n) => Number(n.replace(/\.json$/, "")))
    .filter((n) => Number.isFinite(n));
}

function emit(line: Record<string, unknown>): void {
  appendFileSync(TRACE_PATH, JSON.stringify(line) + "\n");
}

function trim(id?: string): string | null {
  return id ? id.slice(0, 8) : null;
}

function selfCheck(): number {
  if (!existsSync(SESSIONS_DIR)) {
    console.error(`FAIL: ${SESSIONS_DIR} does not exist`);
    return 1;
  }
  const pids = listPids();
  if (pids.length === 0) {
    console.error(`FAIL: no sessions files in ${SESSIONS_DIR}`);
    return 1;
  }
  let parsed = 0;
  for (const pid of pids) {
    const snap = readSnapshot(pid);
    if (snap) parsed += 1;
  }
  if (parsed === 0) {
    console.error(`FAIL: 0/${pids.length} sessions files parsed`);
    return 1;
  }
  console.log(`OK: ${parsed}/${pids.length} sessions files parsed (trace -> ${TRACE_PATH})`);
  return 0;
}

function poll(prev: Map<number, LastSeen>): Map<number, LastSeen> {
  const ts = new Date().toISOString();
  const nowPids = new Set(listPids());
  const next = new Map<number, LastSeen>();

  for (const pid of nowPids) {
    const snap = readSnapshot(pid);
    const last = prev.get(pid);
    const status = snap?.status ?? null;
    const updatedAt = snap?.updatedAt ?? null;

    const base = {
      ts,
      pid,
      sessionId: trim(snap?.sessionId),
      status,
      updatedAt,
      agent: snap?.agent ?? null,
      cwd: snap?.cwd ?? null,
    };

    if (!last) {
      emit({ ...base, event: "appeared", initialStatus: status });
    } else {
      emit(base);
      if (last.status !== status || last.updatedAt !== updatedAt) {
        emit({
          ...base,
          transition: true,
          prevStatus: last.status ?? null,
          prevUpdatedAt: last.updatedAt ?? null,
        });
      }
    }
    next.set(pid, { status: status ?? undefined, updatedAt: updatedAt ?? undefined });
  }

  for (const [pid, last] of prev) {
    if (!nowPids.has(pid)) {
      emit({ ts, pid, event: "vanished", lastStatus: last.status ?? null });
    }
  }
  return next;
}

const args = process.argv.slice(2);
if (args.includes("--self-check")) {
  process.exit(selfCheck());
}

console.log(`logging ${SESSIONS_DIR} -> ${TRACE_PATH} every ${POLL_MS}ms (Ctrl-C to stop)`);
let state = new Map<number, LastSeen>();
state = poll(state);
setInterval(() => {
  state = poll(state);
}, POLL_MS);
