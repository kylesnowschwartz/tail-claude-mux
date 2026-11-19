/**
 * Row maths for the session list: how many agent rows each session shows,
 * how tall each session draws, and which sessions fit the pane around the
 * focused one. Both display modes share it; only the per-session chrome differs.
 */

import type { DisplayMode, SessionData } from "@tcm/runtime";

const CARD_CHROME_ROWS = 3; // Session frame: rounded border top/bottom plus the name row.
const BRANCH_ROWS = 1; // A card adds exactly one row when session.branch is present.
const COMPACT_HEADER_ROWS = 1; // A compact session is one header row above its agent rows.
const HIDDEN_AGENTS_ROWS = 1; // Exactly one "+N more" row when agents are trimmed.
export const OVERFLOW_INDICATOR_ROWS = 1; // "▲ N more" / "▼ N more" line when the session stack scrolls.

type SessionShape = Pick<SessionData, "branch" | "agents">;

/** Rows a session draws above and around its agent rows. */
function chromeRows(mode: DisplayMode, session: SessionShape): number {
  if (mode === "compact") return COMPACT_HEADER_ROWS;
  return CARD_CHROME_ROWS + (session.branch ? BRANCH_ROWS : 0);
}

/** Height of one session showing `visible` of its agents. */
export function sessionRows(mode: DisplayMode, session: SessionShape, visible: number): number {
  const total = session.agents?.length ?? 0;
  return chromeRows(mode, session) + visible + (visible < total ? HIDDEN_AGENTS_ROWS : 0);
}

/**
 * Agent rows each session shows. Every agent shows; the scrolling session
 * window absorbs the height. "+N more" appears only when a single session on
 * its own cannot fit the list alongside the scroll indicators.
 */
export function visibleAgentCounts(mode: DisplayMode, sessions: readonly SessionShape[], budgetRows: number): number[] {
  const indicatorAllowance = sessions.length > 1 ? 2 * OVERFLOW_INDICATOR_ROWS : 0;
  return sessions.map((session) => {
    const total = session.agents?.length ?? 0;
    const maxAgents = Math.max(1, budgetRows - chromeRows(mode, session) - HIDDEN_AGENTS_ROWS - indicatorAllowance);
    return Math.min(total, maxAgents);
  });
}

export interface SessionWindow { start: number; end: number; above: number; below: number }

/**
 * The run of sessions that fits `budgetRows`, grown outward from `anchorIdx`
 * (the focused session), leaving room for the ▲/▼ indicators it needs.
 */
export function sessionWindow(heights: readonly number[], budgetRows: number, anchorIdx: number): SessionWindow {
  const len = heights.length;
  if (heights.reduce((total, h) => total + h, 0) <= budgetRows) {
    return { start: 0, end: len, above: 0, below: 0 };
  }
  const indicatorRows = (start: number, end: number) =>
    (start > 0 ? OVERFLOW_INDICATOR_ROWS : 0) + (end < len ? OVERFLOW_INDICATOR_ROWS : 0);
  const anchor = Math.min(Math.max(0, anchorIdx), len - 1);
  let start = anchor;
  let end = anchor + 1;
  let used = heights[anchor] ?? 0;
  let grew = true;
  while (grew) {
    grew = false;
    if (end < len && used + heights[end]! + indicatorRows(start, end + 1) <= budgetRows) {
      used += heights[end]!;
      end++;
      grew = true;
    }
    if (start > 0 && used + heights[start - 1]! + indicatorRows(start - 1, end) <= budgetRows) {
      start--;
      used += heights[start]!;
      grew = true;
    }
  }
  return { start, end, above: start, below: len - end };
}
