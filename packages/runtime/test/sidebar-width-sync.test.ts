import { describe, expect, test } from "bun:test";
import { clampSidebarWidth, MIN_SIDEBAR_WIDTH } from "../src/server/sidebar-width-sync";

describe("sidebar width sync", () => {
  test("clampSidebarWidth enforces minimum", () => {
    expect(clampSidebarWidth(10)).toBe(MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(5)).toBe(MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(0)).toBe(MIN_SIDEBAR_WIDTH);
  });

  test("clampSidebarWidth passes through values above minimum", () => {
    expect(clampSidebarWidth(50)).toBe(50);
    expect(clampSidebarWidth(MIN_SIDEBAR_WIDTH)).toBe(MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(100)).toBe(100);
  });
});
