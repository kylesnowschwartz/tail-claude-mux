import { describe, test, expect } from "bun:test";
import {
  clampSidebarWidth,
  ABSOLUTE_MIN_SIDEBAR_WIDTH,
  SAVE_DEBOUNCE_MS,
} from "../src/server/sidebar-width-sync";

describe("report-width debounce logic", () => {
  // These tests validate the building blocks used by the server's report-width handler.
  // The handler itself lives in a server closure and is tested via manual verification
  // (session switch doesn't persist, drag does).

  test("SAVE_DEBOUNCE_MS is long enough to catch reflow", () => {
    // Enforcement fires within ~500ms of a session switch. The save debounce must
    // be longer so the enforcement can cancel it.
    expect(SAVE_DEBOUNCE_MS).toBeGreaterThanOrEqual(1000);
  });

  test("clamp with window width rejects garbage reflow widths", () => {
    // A 200-col window reflows the sidebar to 177 during a session switch.
    // 40% of 200 = 80. The clamp catches it even if the debounce doesn't.
    expect(clampSidebarWidth(177, 200)).toBe(80);
  });

  test("clamp without window width still enforces floor", () => {
    // On startup, window width is unknown. Floor-only.
    expect(clampSidebarWidth(5)).toBe(ABSOLUTE_MIN_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(50)).toBe(50);
  });

  test("normal drag widths pass through with window width", () => {
    // User drags to 45 in a 200-col window: 45 < 80, passes.
    expect(clampSidebarWidth(45, 200)).toBe(45);
    expect(clampSidebarWidth(26, 200)).toBe(26);
  });

  test("debounce cancellation pattern prevents save on reflow", async () => {
    // Simulate: report-width fires, then enforcement arrives within debounce window.
    let saved = false;
    let saveTimer: ReturnType<typeof setTimeout> | null = null;

    function cancelPendingSave() {
      if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
    }

    // Report arrives — start debounce timer
    saveTimer = setTimeout(() => { saved = true; }, SAVE_DEBOUNCE_MS);

    // Enforcement arrives 100ms later — cancels the save
    await new Promise((resolve) => setTimeout(resolve, 100));
    cancelPendingSave();

    // Wait past the debounce window
    await new Promise((resolve) => setTimeout(resolve, SAVE_DEBOUNCE_MS + 100));
    expect(saved).toBe(false);
  });

  test("debounce fires when no enforcement cancels it", async () => {
    let saved = false;
    const saveTimer = setTimeout(() => { saved = true; }, SAVE_DEBOUNCE_MS);

    // Wait past the debounce window
    await new Promise((resolve) => setTimeout(resolve, SAVE_DEBOUNCE_MS + 100));
    expect(saved).toBe(true);
    clearTimeout(saveTimer); // cleanup
  });

  test("rapid reports: only the last timer survives", async () => {
    const values: number[] = [];
    let saveTimer: ReturnType<typeof setTimeout> | null = null;

    function cancelPendingSave() {
      if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
    }

    // Simulate 3 rapid reports
    for (const width of [30, 35, 40]) {
      cancelPendingSave();
      saveTimer = setTimeout(() => { values.push(width); }, SAVE_DEBOUNCE_MS);
    }

    await new Promise((resolve) => setTimeout(resolve, SAVE_DEBOUNCE_MS + 100));
    expect(values).toEqual([40]);
  });
});
