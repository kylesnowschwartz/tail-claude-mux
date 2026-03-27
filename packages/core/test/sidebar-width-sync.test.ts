import { describe, expect, test } from "bun:test";

import type { SidebarPane } from "../src/contracts/mux";
import {
  resolveSidebarWidthFromResizeContext,
  snapshotSidebarWindows,
  type SidebarResizeSuppression,
} from "../src/server/sidebar-width-sync";

describe("sidebar width sync", () => {
  test("adopts a sidebar drag when the window width stayed the same", () => {
    const previous = snapshotSidebarWindows([
      { paneId: "%1", sessionName: "alpha", windowId: "@1", width: 26, windowWidth: 198 },
    ] satisfies SidebarPane[]);
    const suppressed = new Map<string, SidebarResizeSuppression>();

    const nextWidth = resolveSidebarWidthFromResizeContext({
      ctx: { paneId: "%1", width: 30, windowWidth: 198 },
      panes: [{ paneId: "%1", sessionName: "alpha", windowId: "@1", width: 30, windowWidth: 198 }],
      previousByWindow: previous,
      suppressedByPane: suppressed,
      now: 1_000,
    });

    expect(nextWidth).toBe(30);
  });

  test("ignores width changes that arrived with a different window width", () => {
    const previous = snapshotSidebarWindows([
      { paneId: "%1", sessionName: "alpha", windowId: "@1", width: 30, windowWidth: 198 },
    ] satisfies SidebarPane[]);
    const suppressed = new Map<string, SidebarResizeSuppression>();

    const nextWidth = resolveSidebarWidthFromResizeContext({
      ctx: { paneId: "%1", width: 24, windowWidth: 155 },
      panes: [{ paneId: "%1", sessionName: "alpha", windowId: "@1", width: 24, windowWidth: 155 }],
      previousByWindow: previous,
      suppressedByPane: suppressed,
      now: 1_000,
    });

    expect(nextWidth).toBeNull();
  });

  test("ignores the immediate acknowledgement of a server-issued resize", () => {
    const previous = snapshotSidebarWindows([
      { paneId: "%1", sessionName: "alpha", windowId: "@1", width: 26, windowWidth: 198 },
    ] satisfies SidebarPane[]);
    const suppressed = new Map<string, SidebarResizeSuppression>([
      ["%1", { width: 30, expiresAt: 2_000 }],
    ]);

    const nextWidth = resolveSidebarWidthFromResizeContext({
      ctx: { paneId: "%1", width: 30, windowWidth: 198 },
      panes: [{ paneId: "%1", sessionName: "alpha", windowId: "@1", width: 30, windowWidth: 198 }],
      previousByWindow: previous,
      suppressedByPane: suppressed,
      now: 1_000,
    });

    expect(nextWidth).toBeNull();
    expect(suppressed.has("%1")).toBe(false);
  });
});
