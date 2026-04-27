import { describe, expect, test } from "bun:test";
import {
  clampSidebarWidth,
  computeMinSidebarWidth,
  ABSOLUTE_MIN_SIDEBAR_WIDTH,
  MAX_SIDEBAR_WIDTH_PERCENT,
} from "../src/server/sidebar-width-sync";
import type { SessionData } from "../src/shared";

describe("sidebar width sync", () => {
  test("clampSidebarWidth enforces minimum", () => {
    expect(clampSidebarWidth(10)).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(5)).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(0)).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("clampSidebarWidth passes through values above minimum", () => {
    expect(clampSidebarWidth(50)).toBe(50);
    expect(clampSidebarWidth(ABSOLUTE_MIN_SIDEBAR_WIDTH)).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(100)).toBe(100);
  });

  test("with windowWidth, clamps to 40% max", () => {
    // 40% of 200 = 80
    expect(clampSidebarWidth(90, 200)).toBe(80);
    expect(clampSidebarWidth(80, 200)).toBe(80);
    expect(clampSidebarWidth(50, 200)).toBe(50);
  });

  test("with small windowWidth, max wins over large values", () => {
    // 40% of 100 = 40
    expect(clampSidebarWidth(60, 100)).toBe(40);
    expect(clampSidebarWidth(40, 100)).toBe(40);
    expect(clampSidebarWidth(30, 100)).toBe(30);
  });

  test("without windowWidth, no max enforced", () => {
    expect(clampSidebarWidth(500)).toBe(500);
    expect(clampSidebarWidth(1000)).toBe(1000);
  });

  test("min boundary passes through exactly", () => {
    expect(clampSidebarWidth(ABSOLUTE_MIN_SIDEBAR_WIDTH)).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("computed max boundary passes through exactly", () => {
    const windowWidth = 200;
    const maxWidth = Math.floor(windowWidth * MAX_SIDEBAR_WIDTH_PERCENT);
    expect(clampSidebarWidth(maxWidth, windowWidth)).toBe(maxWidth);
  });

  test("max takes precedence when window is very small", () => {
    // 40% of 30 = 12, which is below ABSOLUTE_MIN_SIDEBAR_WIDTH.
    // Max wins because a 20-col sidebar in a 30-col window is unusable.
    expect(clampSidebarWidth(15, 30)).toBe(12);
    expect(clampSidebarWidth(25, 30)).toBe(12);
  });
});

function makeSession(overrides: Partial<SessionData> = {}): SessionData {
  const name = overrides.name ?? "test";
  // Default dir's leaf matches the session name so the helper does not
  // accidentally trigger dirMismatch in tests that only override `name`.
  return {
    name,
    createdAt: 0,
    dir: `/tmp/${name}`,
    branch: "",
    dirty: false,
    isWorktree: false,
    unseen: false,
    panes: 1,
    windows: 1,
    uptime: "0s",
    agentState: null,
    agents: [],
    eventTimestamps: [],
    ...overrides,
  };
}

// All content widths include +2 for the focused card's border box.
describe("computeMinSidebarWidth", () => {
  test("returns absolute minimum for empty session list", () => {
    expect(computeMinSidebarWidth([])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("fits short session name: padL(1) + name + status(2) + padR(1) + border(2)", () => {
    // "test" = 4 → content 8 + border 2 = 10, but floor is 15
    expect(computeMinSidebarWidth([makeSession({ name: "test" })])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("fits a longer session name", () => {
    // "my-cool-project" = 15 → 1 + 15 + 2 + 1 + 2 = 21
    expect(computeMinSidebarWidth([makeSession({ name: "my-cool-project" })])).toBe(21);
  });

  test("truncates name at 18 chars", () => {
    // 25-char name truncated to 18 → 1 + 18 + 2 + 1 + 2 = 24
    const longName = "a".repeat(25);
    expect(computeMinSidebarWidth([makeSession({ name: longName })])).toBe(24);
  });

  test("branch row can drive the width", () => {
    // name "ab" = 2 → name row = 1 + 2 + 2 + 1 = 6
    // branch "feature/long" = 12 → branch row = 1 + 2 + 12 + 1 = 16
    // widest content = 16 + border 2 = 18
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feature/long" }),
    ])).toBe(18);
  });

  test("dir mismatch widens the branch row by the glyph cells", () => {
    // name "ab", dir leaf "claude" (≠ "ab") → mismatch glyph adds 2 cols.
    // branch row = 1 + 2 + 12 + 2 + 1 = 18 → + border 2 = 20.
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feature/long", dir: "/Users/k/Code/dotfiles/claude" }),
    ])).toBe(20);
  });

  test("matching dir leaf does not add mismatch cols", () => {
    // dir leaf "ab" === name "ab" → no glyph, branch row stays 16 → + 2 = 18.
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feature/long", dir: "/Users/k/Code/ab" }),
    ])).toBe(18);
  });


  test("agent badge adds to name row", () => {
    // "test-session" = 12, 2 alive agents → badge " ●2" = 3
    // collapsed name row = 1 + 12 + 3 + 2 + 1 = 19
    // expanded agent row "claude" = 6 + 6 + 0 = 12 (name row is widest)
    // + border 2 = 21
    const agents = [
      { agent: "claude", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
      { agent: "amp", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "test-session", agents }),
    ])).toBe(21);
  });

  test("expanded agent row drives width for long agent names", () => {
    // "claude-code" = 11 → agent row = 6 + 11 + 0 = 17 + border 2 = 19
    const agents = [
      { agent: "claude-code", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "short", agents }),
    ])).toBe(19);
  });

  test("thread ID adds 6 cols to agent row", () => {
    // "claude-code" = 11, threadId present → 6 + 11 + 6 + 0 = 23 + border 2 = 25
    const agents = [
      { agent: "claude-code", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const, threadId: "52e9abcd" },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "short", agents }),
    ])).toBe(25);
  });

  test("unseen agent adds 2 cols to agent row", () => {
    // "claude-code" = 11, unseen → 6 + 11 + 2 = 19 + border 2 = 21
    const agents = [
      { agent: "claude-code", session: "x", status: "done" as const, ts: 0, liveness: "exited" as const, unseen: true },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "short", agents }),
    ])).toBe(21);
  });

  test("unseen session badge adds to collapsed name row", () => {
    // "test-session" = 12, 1 alive agent → badge " ●" = 2, unseen → " ●" = 2
    // collapsed name row = 1 + 12 + 2 + 2 + 2 + 1 = 20 + border 2 = 22
    const agents = [
      { agent: "claude", session: "x", status: "waiting" as const, ts: 0, liveness: "alive" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "test-session", agents, unseen: true }),
    ])).toBe(22);
  });

  test("exited agents still contribute to expanded row width", () => {
    // Even exited agents render in the list — "claude" = 6 → 6+6 = 12
    // badge: only "claude" is alive (1) → " ●" = 2
    // name row "test-session" = 12 → 1+12+2+2+1 = 18 (widest content)
    // + border 2 = 20
    const agents = [
      { agent: "claude", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
      { agent: "amp", session: "x", status: "done" as const, ts: 0, liveness: "exited" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "test-session", agents }),
    ])).toBe(20);
  });

  test("uses widest session across multiple", () => {
    const sessions = [
      makeSession({ name: "a" }),
      makeSession({ name: "test-session" }), // 1+12+2+1 = 16 + border 2 = 18
      makeSession({ name: "b" }),
    ];
    expect(computeMinSidebarWidth(sessions)).toBe(18);
  });
});
