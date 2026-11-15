import { describe, expect, test } from "bun:test";
import { conversationName, padAge, padReason, scrollWindow, showReasonWord } from "./needs-block";

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
