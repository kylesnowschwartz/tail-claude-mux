import { describe, expect, test } from "bun:test";
import type { LiaisonItem, SessionData } from "@tcm/runtime";
import {
  findLiaisons,
  isLiaisonName,
  liaisonBlockRows,
  formatAge,
  isLiaisonFinished,
  isLiaisonWorker,
  liaisonItems,
  liaisonTitle,
  liaisonWorkerCount,
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
  });

  test("drops the branch suffix", () => {
    expect(liaisonTitle("liaison \u00b7 main")).toBe("LIAISON");
    expect(liaisonTitle("liaison/personal \u00b7 feat/x")).toBe("LIAISON personal");
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
    expect(rows.map((r) => r.agent.threadName)).toEqual(["old", "new"]);
    expect(rows[0]!.session.name).toBe("b");
  });

  test("leaves out the liaison's own conversation for every reason", () => {
    for (const reason of ["input", "finished", "permission", "question", "error"] as const) {
      const sessions = [session("home", [
        agent({ threadName: "liaison/work \u00b7 main", threadId: "L", attention: reason, attentionSince: NOW - 9 * MIN }),
      ])];
      expect({ reason, rows: needsYou(sessions).length }).toEqual({ reason, rows: 0 });
    }
  });

  test("the liaison at its prompt leaves the count and jump order to the others", () => {
    const sessions = [session("home", [
      agent({ threadName: "liaison", threadId: "L", attention: "input", attentionSince: NOW - 30 * MIN }),
      agent({ threadName: "fix-tax", attention: "question", attentionSince: NOW - 5 * MIN }),
      agent({ threadName: "docs", attention: "finished", attentionSince: NOW - 10 * MIN }),
    ])];
    const rows = needsYou(sessions);
    expect(rows.length).toBe(2);
    expect(rows[0]!.agent.threadName).toBe("docs");
    expect(rows.map((r) => r.agent.threadName)).toEqual(["docs", "fix-tax"]);
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
  test("title row, closing rule, and at most three item rows", () => {
    expect([0, 1, 2, 3, 4, 20].map(liaisonBlockRows)).toEqual([2, 3, 4, 5, 5, 5]);
  });
});

describe("liaisonItems", () => {
  const ref = (over: Partial<AttentionAgent>, items: LiaisonItem[] | undefined) => {
    const a = agent({ threadName: "liaison", ...over });
    const s = session("home", [a]);
    s.metadata = { status: null, progress: null, logs: [], liaisonItems: items };
    return { session: s, agent: a };
  };

  test("orders by priority, then oldest first", () => {
    const items = liaisonItems(ref({}, [
      { kind: "reminder", title: "r", since: NOW - 60 * MIN },
      { kind: "answer", title: "a-new", since: NOW - 1 * MIN },
      { kind: "blocked", title: "b", since: NOW - 2 * MIN },
      { kind: "no_report", title: "n-old", since: NOW - 9 * MIN },
    ]));
    expect(items.map((i) => i.title)).toEqual(["b", "n-old", "a-new", "r"]);
  });

  test("adds an asks item when the liaison waits on a permission or a question or has failed", () => {
    for (const reason of ["permission", "question", "error"] as const) {
      const items = liaisonItems(ref({ attention: reason, attentionSince: NOW - 3 * MIN }, [
        { kind: "blocked", title: "b", since: NOW - 9 * MIN },
      ]));
      expect(items).toEqual([
        { kind: "blocked", title: "b", since: NOW - 9 * MIN },
        { kind: "asks", title: reason, since: NOW - 3 * MIN },
      ]);
    }
  });

  test("no asks item for other attention reasons", () => {
    for (const reason of ["input", "finished"] as const) {
      expect(liaisonItems(ref({ attention: reason, attentionSince: NOW }, undefined))).toEqual([]);
    }
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

  test("the count leaves out workers the liaison reports finished", () => {
    expect(liaisonWorkerCount(sessions, liaison, ["a"])).toBe(2);
    expect(liaisonWorkerCount(sessions, liaison, ["a", "b", "c"])).toBe(0);
    expect(liaisonWorkerCount(sessions, liaison, ["f", "d"])).toBe(3);
    const only = [session("home", [liaison, workerA])];
    expect(liaisonWorkerCount(only, liaison, ["a"])).toBe(0);
    const home = session("home", [liaison, workerA]);
    home.metadata = { status: null, progress: null, logs: [], liaisonFinished: ["a"] };
    expect(isLiaisonFinished(workerA, findLiaisons([home]))).toBe(true);
  });

  test("the finished mark needs the liaison's report and its own worker", () => {
    const home = session("home", [liaison, workerA]);
    home.metadata = { status: null, progress: null, logs: [], liaisonFinished: ["a", "f"] };
    const refs = findLiaisons([home, ...sessions.slice(1)]);
    expect(isLiaisonFinished(workerA, refs)).toBe(true);
    expect(isLiaisonFinished(workerB, refs)).toBe(false);
    expect(isLiaisonFinished(plain, refs)).toBe(false);
    expect(isLiaisonFinished(workerA, open)).toBe(false);
  });

  test("the marks leave the rows of each session in their order", () => {
    const before = sessions.map((s) => s.agents.map((a) => a.threadId));
    for (const s of sessions) for (const a of s.agents) isLiaisonWorker(a, open);
    liaisonWorkerCount(sessions, liaison);
    expect(sessions.map((s) => s.agents.map((a) => a.threadId))).toEqual(before);
  });
});

