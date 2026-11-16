import { describe, expect, test } from "bun:test";
import type { SessionData } from "@tcm/runtime";
import type { AttentionAgent } from "./attention";
import { needsYou } from "./attention";
import { conversationName, needsBlockRows, padAge, padReason, scrollWindow, showReasonWord } from "./needs-block";

describe("showReasonWord", () => {
  test("drops the reason word on narrow sidebars", () => {
    expect(showReasonWord(40)).toBe(true);
    expect(showReasonWord(25)).toBe(true);
    expect(showReasonWord(24)).toBe(false);
    expect(showReasonWord(16)).toBe(false);
  });
});

describe("cells", () => {
  test("ages right-align and reasons left-align", () => {
    expect(padAge("2m")).toBe(" 2m");
    expect(padAge("14m")).toBe("14m");
    expect(padReason("input")).toBe("input     ");
  });

  test("conversation name falls back to the thread id tail, then the agent", () => {
    expect(conversationName({ agent: "claude-code", session: "s", status: "done", ts: 0, threadName: "x" })).toBe("x");
    expect(conversationName({ agent: "claude-code", session: "s", status: "done", ts: 0, threadId: "abcdef12" })).toBe("ef12");
    expect(conversationName({ agent: "claude-code", session: "s", status: "done", ts: 0 })).toBe("claude-code");
  });
});

describe("scrollWindow", () => {
  test("shows everything when it fits", () => {
    expect(scrollWindow(3, 5, 0)).toEqual({ start: 0, end: 3, above: 0, below: 0 });
  });

  test("reserves indicator rows and keeps every row reachable", () => {
    expect(scrollWindow(10, 5, 0)).toEqual({ start: 0, end: 3, above: 0, below: 7 });
    expect(scrollWindow(10, 5, 4)).toEqual({ start: 4, end: 7, above: 4, below: 3 });
    expect(scrollWindow(10, 5, 99)).toEqual({ start: 7, end: 10, above: 7, below: 0 });
    expect(scrollWindow(10, 5, -3).start).toBe(0);
  });

  test("keeps one row when space is tiny", () => {
    expect(scrollWindow(4, 1, 2)).toEqual({ start: 2, end: 3, above: 2, below: 1 });
  });
});

describe("needsBlockRows", () => {
  const at = (threadName: string, attention: AttentionAgent["attention"]): AttentionAgent => ({
    agent: "claude-code", session: "home", status: "idle", liveness: "alive", ts: 0,
    threadName, threadId: threadName, attention, attentionSince: 0,
  });
  const home = (agents: AttentionAgent[]) => [{ name: "home", agents } as unknown as SessionData];

  test("no rows draws no block, at a narrow width too", () => {
    expect(showReasonWord(20)).toBe(false);
    expect(needsBlockRows(needsYou(home([at("liaison", "input")])).length, 4, 0)).toBe(0);
    expect(needsBlockRows(needsYou(home([at("liaison", "finished")])).length, 4, 0)).toBe(0);
    expect(needsBlockRows(0, 4, 0)).toBe(0);
  });

  test("the liaison's row takes no height beside a worker's", () => {
    const rows = needsYou(home([at("liaison", "input"), at("fix-tax", "question")]));
    expect(needsBlockRows(rows.length, 4, 0)).toBe(3);
  });

  test("rows past the window add the indicator rows", () => {
    expect(needsBlockRows(10, 4, 0)).toBe(2 + 2 + 1);
    expect(needsBlockRows(10, 4, 3)).toBe(2 + 2 + 2);
  });
});
