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

describe("computeMinSidebarWidth", () => {
  test("returns absolute minimum for empty session list", () => {
    expect(computeMinSidebarWidth([])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("fits short session name: index(3) + name + status(2) + pad(1)", () => {
    // "test" = 4 chars → 3 + 4 + 2 + 1 = 10, but floor is 15
    expect(computeMinSidebarWidth([makeSession({ name: "test" })])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("fits a longer session name", () => {
    // "my-cool-project" = 15 chars → 3 + 15 + 2 + 1 = 21
    expect(computeMinSidebarWidth([makeSession({ name: "my-cool-project" })])).toBe(21);
  });

  test("truncates name at 18 chars", () => {
    // 25-char name truncated to 18 → 3 + 18 + 2 + 1 = 24
    const longName = "a".repeat(25);
    expect(computeMinSidebarWidth([makeSession({ name: longName })])).toBe(24);
  });

  test("branch row can drive the width", () => {
    // name "ab" = 2 → name row = 3 + 2 + 2 + 1 = 8
    // branch "feature/long" = 12 → branch row = 3 + 12 + 1 = 16
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feature/long" }),
    ])).toBe(16);
  });

  test("branch + port hint widens the branch row", () => {
    // branch "main" = 4, ports [3000] → portHint "⌁3000" = 5 chars
    // branch row = 3 + 4 + 1 (space) + 5 + 1 = 14, still below floor
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "main", ports: [3000] }),
    ])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);

    // branch "feat/signup" = 11, ports [3000, 3001] → "⌁3000+1" = 7 chars
    // branch row = 3 + 11 + 1 + 7 + 1 = 23
    expect(computeMinSidebarWidth([
      makeSession({ name: "ab", branch: "feat/signup", ports: [3000, 3001] }),
    ])).toBe(23);
  });

  test("ports without branch still account for row 2", () => {
    // No branch, ports [8080] → portHint "⌁8080" = 5 chars
    // branch row = 3 + 0 + 5 + 1 = 9, below floor
    expect(computeMinSidebarWidth([
      makeSession({ name: "a", branch: "", ports: [8080] }),
    ])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);

    // Short name but wide port hint
    // ports [3000, 3001, 3002] → "⌁3000+2" = 7 chars
    // branch row = 3 + 0 + 7 + 1 = 11, still below floor
    expect(computeMinSidebarWidth([
      makeSession({ name: "a", branch: "", ports: [3000, 3001, 3002] }),
    ])).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
  });

  test("uses widest session across multiple", () => {
    const sessions = [
      makeSession({ name: "a" }),
      makeSession({ name: "opensessions" }), // 12 → 3+12+2+1 = 18
      makeSession({ name: "b" }),
    ];
    expect(computeMinSidebarWidth(sessions)).toBe(18);
  });
});
