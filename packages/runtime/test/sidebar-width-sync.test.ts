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
  return {
    name: "test",
    createdAt: 0,
    dir: "/tmp/test",
    branch: "",
    dirty: false,
    isWorktree: false,
    unseen: false,
    panes: 1,
    ports: [],
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

  test("fits short session name: index(3) + name + status(2) + pad(1) + border(2)", () => {
    // "test" = 4 → content 10 + border 2 = 12, but floor is 15
    expect(computeMinSidebarWidth([makeSession({ name: "test" })])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("fits a longer session name", () => {
    // "my-cool-project" = 15 → 3 + 15 + 2 + 1 + 2 = 23
    expect(computeMinSidebarWidth([makeSession({ name: "my-cool-project" })])).toBe(23);
  });

  test("truncates name at 18 chars", () => {
    // 25-char name truncated to 18 → 3 + 18 + 2 + 1 + 2 = 26
    const longName = "a".repeat(25);
    expect(computeMinSidebarWidth([makeSession({ name: longName })])).toBe(26);
  });

  test("branch row can drive the width", () => {
    // name "ab" = 2 → name row = 3 + 2 + 2 + 1 = 8
    // branch "feature/long" = 12 → branch row = 3 + 12 + 1 = 16
    // widest content = 16 + border 2 = 18
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feature/long" }),
    ])).toBe(18);
  });

  test("branch + port hint widens the branch row", () => {
    // branch "main" = 4, ports [3000] → "⌁3000" = 5
    // branch row = 3 + 4 + 1 + 5 + 1 = 14, + border 2 = 16
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "main", ports: [3000] }),
    ])).toBe(16);

    // branch "feat/signup" = 11, ports [3000, 3001] → "⌁3000+1" = 7
    // branch row = 3 + 11 + 1 + 7 + 1 = 23 + border 2 = 25
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feat/signup", ports: [3000, 3001] }),
    ])).toBe(25);
  });

  test("ports without branch still account for row 2", () => {
    // ports [8080] → "⌁8080" = 5. branch row = 3 + 0 + 5 + 1 = 9 + border 2 = 11
    // name "a" → name row = 3 + 1 + 2 + 1 = 7 + border 2 = 9. floor wins.
    expect(computeMinSidebarWidth([
      makeSession({ name: "a", branch: "", ports: [8080] }),
    ])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("agent badge adds to name row", () => {
    // "opensessions" = 12, 2 alive agents → badge " ●2" = 3
    // collapsed name row = 3 + 12 + 3 + 2 + 1 = 21
    // expanded agent row "claude" = 9 + 6 + 7 = 22 (widest content)
    // + border 2 = 24
    const agents = [
      { agent: "claude", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
      { agent: "amp", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "opensessions", agents }),
    ])).toBe(24);
  });

  test("expanded agent row drives width for long agent names", () => {
    // "claude-code" = 11 → agent row = 9 + 11 + 7 = 27 + border 2 = 29
    const agents = [
      { agent: "claude-code", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "short", agents }),
    ])).toBe(29);
  });

  test("thread ID adds 6 cols to agent row", () => {
    // "claude-code" = 11, threadId present → 9 + 11 + 6 + 7 = 33 + border 2 = 35
    const agents = [
      { agent: "claude-code", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const, threadId: "52e9abcd" },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "short", agents }),
    ])).toBe(35);
  });

  test("exited agents still contribute to expanded row width", () => {
    // Even exited agents render in the list — "claude" = 6 → 9+6+7 = 22
    // + border 2 = 24
    const agents = [
      { agent: "claude", session: "x", status: "running" as const, ts: 0, liveness: "alive" as const },
      { agent: "amp", session: "x", status: "done" as const, ts: 0, liveness: "exited" as const },
    ];
    expect(computeMinSidebarWidth([
      makeSession({ name: "opensessions", agents }),
    ])).toBe(24);
  });

  test("uses widest session across multiple", () => {
    const sessions = [
      makeSession({ name: "a" }),
      makeSession({ name: "opensessions" }), // 3+12+2+1 = 18 + border 2 = 20
      makeSession({ name: "b" }),
    ];
    expect(computeMinSidebarWidth(sessions)).toBe(20);
  });
});
