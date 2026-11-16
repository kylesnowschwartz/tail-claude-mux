/**
 * Pure layout helpers for the sidebar's "needs you" block.
 */

import type { AgentEvent } from "@tcm/runtime";

/** Display name of a conversation row. */
export function conversationName(agent: AgentEvent): string {
  if (agent.threadName) return agent.threadName;
  if (agent.threadId) return agent.threadId.length <= 4 ? agent.threadId : agent.threadId.slice(-4);
  return agent.agent;
}

/** Width of the reason column: the longest reason word. */
export const REASON_WIDTH = "permission".length;
/** Width of the right-aligned age column ("now", "45s", "14m", "36h"). */
export const AGE_WIDTH = 3;
/** Columns a row spends outside name, reason and age: left pad, glyph, gap, right pad. */
const ROW_CHROME = 4;
const MIN_NAME_WIDTH = 6;

/** Whether a sidebar this wide has room for the reason word beside name and age. */
export function showReasonWord(width: number): boolean {
  return width - ROW_CHROME - (AGE_WIDTH + 1) - (REASON_WIDTH + 1) >= MIN_NAME_WIDTH;
}

/** Right-aligned age cell. */
export function padAge(age: string): string {
  return age.padStart(AGE_WIDTH);
}

/** Left-aligned reason cell. */
export function padReason(reason: string): string {
  return reason.padEnd(REASON_WIDTH);
}

/**
 * Window of `total` rows shown in `maxRows`, scrolled to `offset`. When the
 * rows do not fit, two rows are reserved for the "▲ N more" / "▼ N more"
 * indicators and the offset is clamped so the window stays full. Every row
 * stays reachable by moving the offset.
 */
export function scrollWindow(total: number, maxRows: number, offset: number): { start: number; end: number; above: number; below: number } {
  if (total <= maxRows) return { start: 0, end: total, above: 0, below: 0 };
  const content = Math.max(1, maxRows - 2);
  const start = Math.max(0, Math.min(offset, total - content));
  const end = start + content;
  return { start, end, above: start, below: total - end };
}

/** Rows of the heading above the needs rows plus the closing rule below them. */
export const NEEDS_HEADER_ROWS = 2;
/** Rows one "▲ N more" / "▼ N more" indicator takes. */
export const OVERFLOW_INDICATOR_ROWS = 1;

/** Rows the "needs you" block takes; 0 when it has no rows, so it is not drawn. */
export function needsBlockRows(total: number, maxRows: number, offset: number): number {
  if (total === 0) return 0;
  const win = scrollWindow(total, maxRows, offset);
  return NEEDS_HEADER_ROWS + (win.end - win.start)
    + (win.above > 0 ? OVERFLOW_INDICATOR_ROWS : 0) + (win.below > 0 ? OVERFLOW_INDICATOR_ROWS : 0);
}
