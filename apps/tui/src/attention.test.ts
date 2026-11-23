import { describe, expect, test } from "bun:test";
import type { SessionData } from "@tcm/runtime";
import {
  formatAge,
  needsYou,
  statusLabel,
  type AttentionAgent,
} from "./attention";
const NOW = 1_700_000_000_000;
const MIN = 60_000;

function agent(over: Partial<AttentionAgent>): AttentionAgent {
  return { agent: "claude-code", session: "s", status: "running", liveness: "alive", ts: NOW, ...over };
}

function session(name: string, agents: AttentionAgent[]): SessionData {
  return {
    name, createdAt: 0, dir: "/x", branch: "", dirty: false, isWorktree: false, unseen: false,
    panes: 1, windows: 1, uptime: "", agentState: agents[0] ?? null, agents, eventTimestamps: [],
  };
}

describe("statusLabel", () => {
  test("maps tracker status and liveness to the sidebar labels", () => {
    expect(statusLabel({ status: "running" })).toBe("working");
    expect(statusLabel({ status: "waiting" })).toBe("waiting");
    expect(statusLabel({ status: "error" })).toBe("error");
    expect(statusLabel({ status: "done", liveness: "alive" })).toBe("ready");
    expect(statusLabel({ status: "done" })).toBe("stopped");
    expect(statusLabel({ status: "idle" })).toBe("ready");
  });
});

describe("needsYou", () => {
  test("lists only conversations with a reason, oldest first", () => {
    const sessions = [
      session("a", [
        agent({ threadName: "new", attention: "permission", attentionSince: NOW - 2 * MIN }),
        agent({ threadName: "calm" }),
      ]),
      session("b", [
        agent({ threadName: "old", attention: "finished", attentionSince: NOW - 14 * MIN }),
        agent({ threadName: "mid", attention: "question", attentionSince: NOW - 9 * MIN }),
      ]),
    ];
    const rows = needsYou(sessions);
    expect(rows.map((r) => r.agent.threadName)).toEqual(["old", "mid", "new"]);
    expect(rows[0]!.session.name).toBe("b");
  });
});

describe("formatAge", () => {
  test("formats compact ages", () => {
    expect(formatAge(NOW, NOW)).toBe("now");
    expect(formatAge(NOW - 45_000, NOW)).toBe("45s");
    expect(formatAge(NOW - 14 * MIN, NOW)).toBe("14m");
    expect(formatAge(NOW - 3 * 60 * MIN, NOW)).toBe("3h");
    expect(formatAge(NOW - 3 * 24 * 60 * MIN, NOW)).toBe("3d");
    expect(formatAge(undefined, NOW)).toBe("");
    expect(formatAge(NOW + 10_000, NOW)).toBe("now");
  });
});

describe("needsYou liveness", () => {
  test("leaves out conversations that have ended or are not alive", () => {
    const sessions = [session("a", [
      agent({ threadName: "live", attention: "finished", attentionSince: NOW - MIN, status: "done", liveness: "alive" }),
      agent({ threadName: "gone", attention: "finished", attentionSince: NOW - 2 * MIN, status: "done", liveness: "exited" }),
      agent({ threadName: "unknown", attention: "error", attentionSince: NOW - 3 * MIN, status: "error", liveness: undefined }),
    ])];
    expect(needsYou(sessions).map((r) => r.agent.threadName)).toEqual(["live"]);
  });
});
