import { describe, expect, test } from "bun:test";
import { sessionRows, sessionWindow, visibleAgentCounts } from "./session-list-rows";

const agents = (n: number) => Array.from({ length: n }, () => ({})) as never[];
const session = (n: number, branch = "") => ({ branch, agents: agents(n) });

describe("sessionRows", () => {
  test("cards: border, name row, branch row, agents", () => {
    expect(sessionRows("cards", session(2), 2)).toBe(5);
    expect(sessionRows("cards", session(2, "main"), 2)).toBe(6);
  });
  test("compact: one header row plus one row per agent; branch adds nothing", () => {
    expect(sessionRows("compact", session(2), 2)).toBe(3);
    expect(sessionRows("compact", session(2, "main"), 2)).toBe(3);
    expect(sessionRows("compact", session(0), 0)).toBe(1);
  });
  test("both modes add the +N more row when agents are hidden", () => {
    expect(sessionRows("cards", session(5), 3)).toBe(7);
    expect(sessionRows("compact", session(5), 3)).toBe(5);
  });
});

describe("visibleAgentCounts", () => {
  test("every agent shows when the budget allows", () => {
    expect(visibleAgentCounts("cards", [session(3), session(1)], 40)).toEqual([3, 1]);
    expect(visibleAgentCounts("compact", [session(3), session(1)], 40)).toEqual([3, 1]);
  });
  test("a session taller than the budget trims to what fits beside its chrome", () => {
    // 10 rows - chrome - "+N more" - two indicators.
    expect(visibleAgentCounts("cards", [session(20, "b"), session(1)], 10)).toEqual([3, 1]);
    expect(visibleAgentCounts("compact", [session(20, "b"), session(1)], 10)).toEqual([6, 1]);
  });
  test("a lone session needs no indicator allowance", () => {
    expect(visibleAgentCounts("compact", [session(20)], 10)).toEqual([8]);
  });
});

describe("sessionWindow", () => {
  test("everything fits", () => {
    expect(sessionWindow([3, 3, 3], 9, 0)).toEqual({ start: 0, end: 3, above: 0, below: 0 });
  });
  test("grows around the focused session and reserves indicator rows", () => {
    expect(sessionWindow([3, 3, 3, 3], 8, 2)).toEqual({ start: 2, end: 4, above: 2, below: 0 });
  });
  test("compact heights fit more sessions than card heights in the same budget", () => {
    const counts = [2, 2, 2, 2, 2];
    const heights = (mode: "cards" | "compact") => counts.map((n) => sessionRows(mode, session(n), n));
    const cards = sessionWindow(heights("cards"), 16, 0);
    const compact = sessionWindow(heights("compact"), 16, 0);
    expect(cards.end - cards.start).toBe(3);
    expect(compact).toEqual({ start: 0, end: 5, above: 0, below: 0 });
  });
});
