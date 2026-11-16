import { describe, expect, test } from "bun:test";
import type { SessionData } from "@tcm/runtime";
import {
  findLiaisons,
  isLiaisonName,
  liaisonBlockRows,
  fleetFits,
  fleetCounts,
  formatAge,
  formatFleet,
  formatWorkers,
  isLiaisonWorker,
  liaisonTitle,
  liaisonWorkerCount,
  needsSummary,
  needsYou,
  statusLabel,
  statusStart,
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
  });

  test("drops the branch suffix", () => {
    expect(liaisonTitle("liaison \u00b7 main")).toBe("LIAISON");
    expect(liaisonTitle("liaison/personal \u00b7 feat/x")).toBe("LIAISON personal");
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
    expect(needsSummary([], NOW)).toBe("");
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

describe("isLiaisonName", () => {
  test("matches the bare word or a liaison/ prefix only", () => {
    expect(isLiaisonName("liaison")).toBe(true);
    expect(isLiaisonName("liaison/work")).toBe(true);
    expect(isLiaisonName("liaisons-notes")).toBe(false);
    expect(isLiaisonName("liaison-old")).toBe(false);
    expect(isLiaisonName("liaison \u00b7 main")).toBe(true);
    expect(isLiaisonName("liaison/personal \u00b7 feat/x")).toBe(true);
    expect(isLiaisonName("liaisonx \u00b7 main")).toBe(false);
    expect(isLiaisonName("my liaison \u00b7 main")).toBe(false);
    expect(isLiaisonName("liaison-project/s11")).toBe(false);
    expect(isLiaisonName(undefined)).toBe(false);
  });
});

describe("liaisonBlockRows", () => {
  test("only the first liaison block carries the fleet and needs rows", () => {
    expect(liaisonBlockRows(0, false, false, true)).toBe(4);
    expect(liaisonBlockRows(0, true, false, true)).toBe(5);
    expect(liaisonBlockRows(1, false, false, true)).toBe(2);
    expect(liaisonBlockRows(1, true, false, true)).toBe(3);
  });

  test("a liaison with workers takes one more row", () => {
    expect(liaisonBlockRows(0, false, true, true)).toBe(5);
    expect(liaisonBlockRows(0, true, true, true)).toBe(6);
    expect(liaisonBlockRows(1, false, true, true)).toBe(3);
    expect(liaisonBlockRows(1, true, true, true)).toBe(4);
  });

  test("the first block drops the needs row when nothing needs the engineer", () => {
    expect(liaisonBlockRows(0, false, false, false)).toBe(3);
    expect(liaisonBlockRows(0, true, true, false)).toBe(5);
    expect(liaisonBlockRows(1, false, false, false)).toBe(2);
  });
});

describe("liaison workers", () => {
  const LIAISON = "11111111-2222-4333-8444-555555555555";
  const CLOSED = "99999999-8888-4777-8666-555555555555";
  const liaison = agent({ threadName: "liaison", threadId: LIAISON });
  const closedLiaison = agent({ threadName: "liaison/old", threadId: CLOSED, status: "done", liveness: "exited" });
  const workerA = agent({ threadName: "check-dates", threadId: "a", liaisonWorkId: "w1", liaisonParentThreadId: LIAISON });
  const workerB = agent({ threadName: "fix-tax", threadId: "b", liaisonWorkId: "w2", liaisonParentThreadId: LIAISON });
  const workerC = agent({ threadName: "docs", threadId: "c", liaisonWorkId: "w3", liaisonParentThreadId: LIAISON });
  const orphan = agent({ threadName: "orphan", threadId: "d", liaisonWorkId: "w9", liaisonParentThreadId: CLOSED });
  const unknownParent = agent({ threadName: "stray", threadId: "e", liaisonWorkId: "w8", liaisonParentThreadId: "not-open" });
  const plain = agent({ threadName: "plain", threadId: "f" });
  const sessions = [
    session("home", [liaison, workerA]),
    session("shop", [workerB, plain, orphan]),
    session("docs", [workerC, unknownParent, closedLiaison]),
  ];
  const open = findLiaisons(sessions);

  test("only the open liaison counts as a liaison here", () => {
    expect(open.map((r) => r.agent.threadId)).toEqual([LIAISON]);
  });

  test("a row whose parent is an open liaison is marked", () => {
    expect(isLiaisonWorker(workerA, open)).toBe(true);
    expect(isLiaisonWorker(workerB, open)).toBe(true);
    expect(isLiaisonWorker(workerC, open)).toBe(true);
  });

  test("no mark for a parent that is no open liaison", () => {
    expect(isLiaisonWorker(orphan, open)).toBe(false);
    expect(isLiaisonWorker(unknownParent, open)).toBe(false);
    expect(isLiaisonWorker(plain, open)).toBe(false);
    expect(isLiaisonWorker(workerA, [])).toBe(false);
  });

  test("the liaison itself is never its own worker", () => {
    const selfParent = agent({ threadName: "liaison", threadId: LIAISON, liaisonParentThreadId: LIAISON });
    const refs = findLiaisons([session("x", [selfParent])]);
    expect(isLiaisonWorker(selfParent, refs)).toBe(false);
    expect(liaisonWorkerCount([session("x", [selfParent])], selfParent)).toBe(0);
  });

  test("a liaison with no thread id has no workers", () => {
    const nameless = agent({ threadName: "liaison" });
    const stray = agent({ threadName: "stray" });
    expect(liaisonWorkerCount([session("x", [nameless, stray])], nameless)).toBe(0);
    expect(isLiaisonWorker(stray, findLiaisons([session("x", [nameless, stray])]))).toBe(false);
  });

  test("the count covers every session and only this liaison's rows", () => {
    expect(liaisonWorkerCount(sessions, liaison)).toBe(3);
    expect(liaisonWorkerCount(sessions, closedLiaison)).toBe(1);
    expect(liaisonWorkerCount([session("x", [liaison, plain])], liaison)).toBe(0);
  });

  test("the marks leave the rows of each session in their order", () => {
    const before = sessions.map((s) => s.agents.map((a) => a.threadId));
    for (const s of sessions) for (const a of s.agents) isLiaisonWorker(a, open);
    liaisonWorkerCount(sessions, liaison);
    expect(sessions.map((s) => s.agents.map((a) => a.threadId))).toEqual(before);
  });

  test("the count reads in words, and as a bare number when the words do not fit", () => {
    expect(formatWorkers(3, 40)).toBe("3 workers");
    expect(formatWorkers(1, 40)).toBe("1 worker");
    expect(formatWorkers(3, 9)).toBe("3 workers");
    expect(formatWorkers(3, 8)).toBe("3");
    expect(formatWorkers(12, 9)).toBe("12");
    expect(formatWorkers(3, 0)).toBe("3");
  });
});

describe("statusStart", () => {
  test("uses statusSince, falling back to ts", () => {
    expect(statusStart(agent({ ts: NOW, statusSince: NOW - 5 * MIN }))).toBe(NOW - 5 * MIN);
    expect(statusStart(agent({ ts: NOW }))).toBe(NOW);
  });
});
