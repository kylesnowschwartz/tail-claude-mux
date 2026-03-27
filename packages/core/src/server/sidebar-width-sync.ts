import type { SidebarPane } from "../contracts/mux";

export interface SidebarResizeContext {
  paneId?: string;
  sessionName?: string;
  windowId?: string;
  width?: number;
  windowWidth?: number;
}

export interface SidebarWindowSnapshot {
  width?: number;
  windowWidth?: number;
}

export interface SidebarResizeSuppression {
  width: number;
  expiresAt: number;
}

export function snapshotSidebarWindows(panes: SidebarPane[]): Map<string, SidebarWindowSnapshot> {
  const snapshots = new Map<string, SidebarWindowSnapshot>();
  for (const pane of panes) {
    snapshots.set(pane.windowId, {
      width: pane.width,
      windowWidth: pane.windowWidth,
    });
  }
  return snapshots;
}

export function resolveSidebarWidthFromResizeContext(params: {
  ctx?: SidebarResizeContext;
  panes: SidebarPane[];
  previousByWindow: Map<string, SidebarWindowSnapshot>;
  suppressedByPane: Map<string, SidebarResizeSuppression>;
  now?: number;
}): number | null {
  const { ctx, panes, previousByWindow, suppressedByPane, now = Date.now() } = params;
  if (!ctx?.paneId) return null;

  const pane = panes.find((candidate) => candidate.paneId === ctx.paneId);
  if (!pane) return null;

  const width = pane.width ?? ctx.width;
  const windowWidth = pane.windowWidth ?? ctx.windowWidth;
  if (width == null || windowWidth == null) return null;

  const suppressed = suppressedByPane.get(pane.paneId);
  if (suppressed) {
    if (suppressed.width === width && suppressed.expiresAt >= now) {
      suppressedByPane.delete(pane.paneId);
      return null;
    }
    if (suppressed.expiresAt < now || suppressed.width !== width) {
      suppressedByPane.delete(pane.paneId);
    }
  }

  const previous = previousByWindow.get(pane.windowId);
  if (!previous || previous.width == null || previous.windowWidth == null) return null;
  if (previous.windowWidth !== windowWidth) return null;
  if (previous.width === width) return null;

  return width;
}
