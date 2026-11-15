import { describe, expect, test } from "bun:test";
import type { SessionData } from "@tcm/runtime";
import {
  findLiaisons,
  fleetFits,
  fleetCounts,
  formatAge,
  formatFleet,
  liaisonTitle,
  needsSummary,
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

describe("findLiaisons", () => {
  test("finds open conversations whose name starts with liaison, sorted by name", () => {
    const sessions = [
      session("a", [agent({ threadName: "liaison/work" }), agent({ threadName: "plugin-work" })]),
      session("b", [agent({ threadName: "liaison/personal", status: "done", liveness: "alive" })]),
      session("c", [agent({ threadName: "liaison-old", status: "done", liveness: "exited" })]),
      session("d", [agent({ threadName: "my-liaison" })]),
    ];
    expect(findLiaisons(sessions).map((r) => r.agent.threadName)).toEqual(["liaison/personal", "liaison/work"]);
  });

  test("returns nothing when no liaison is open", () => {
    expect(findLiaisons([session("a", [agent({ threadName: "x" })])])).toEqual([]);
  });
});

describe("liaisonTitle", () => {
  test("shows the bare word or the word plus the rest of the name", () => {
    expect(liaisonTitle("liaison")).toBe("LIAISON");
    expect(liaisonTitle("liaison/work")).toBe("LIAISON work");
    expect(liaisonTitle("liaison-personal")).toBe("LIAISON personal");
  });
});

describe("fleetCounts", () => {
  test("counts every conversation per label in label order, zeros omitted", () => {
    const sessions = [
      session("a", [agent({}), agent({}), agent({ status: "waiting" })]),
      session("b", [agent({ status: "done", liveness: "alive" }), agent({ status: "done", liveness: "exited" })]),
    ];
    const counts = fleetCounts(sessions);
    expect(counts).toEqual([
      { label: "working", count: 2 },
      { label: "waiting", count: 1 },
      { label: "ready", count: 1 },
      { label: "stopped", count: 1 },
    ]);
    expect(formatFleet(counts)).toBe("2 working · 1 waiting · 1 ready · 1 stopped");
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
        agent({ threadName: "liaison", attention: "question", attentionSince: NOW - 9 * MIN }),
      ]),
    ];
    const rows = needsYou(sessions);
    expect(rows.map((r) => r.agent.threadName)).toEqual(["old", "liaison", "new"]);
    expect(rows[0]!.session.name).toBe("b");
    expect(needsSummary(rows, NOW)).toBe("3 · oldest 14m");
  });

  test("summary has a calm empty form", () => {
    expect(needsSummary([], NOW)).toBe("none");
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

describe("fleetFits", () => {
  test("compares the worded summary with the columns available", () => {
    const counts = [{ label: "working" as const, count: 2 }, { label: "ready" as const, count: 1 }];
    expect(fleetFits(counts, 19)).toBe(true);
    expect(fleetFits(counts, 18)).toBe(false);
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
