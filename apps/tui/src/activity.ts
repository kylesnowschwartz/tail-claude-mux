/**
 * Activity-zone seismograph logic — the pure half of the sidebar's histogram
 * (the ActivityZone component in index.tsx renders it).
 *
 * Everything here is deterministic given (logs, now, geometry): time
 * bucketing, sqrt-scaled multi-row bar rendering, per-bucket icon selection,
 * and column expansion. Kept free of solid-js/opentui so it can be
 * unit-tested (index.tsx starts rendering on import and cannot be imported
 * by tests).
 *
 * Historical note: the original activity zone was a text stream (eyebrow
 * labels + verb glyphs + tool descriptions) specified in
 * docs/simmer/activity-zone/result.md. The seismograph supersedes it; the
 * sparkline bucket contract (8 s buckets, newest-right, ▁ floor never blank)
 * and the verb-glyph vocabulary carry over unchanged.
 */

import type { MetadataLogEntry } from "@tcm/runtime";
import {
  SEV_WAITING,
  ACTIVITY_VERB_READ,
  ACTIVITY_VERB_LIST,
  ACTIVITY_VERB_SEARCH,
  ACTIVITY_VERB_EDIT,
  ACTIVITY_VERB_RUN,
  ACTIVITY_VERB_WEB,
  ACTIVITY_VERB_TASK,
  ACTIVITY_VERB_SKILL,
  ACTIVITY_VERB_THINKING,
  ACTIVITY_VERB_ERROR,
  ACTIVITY_VERB_MISC,
} from "./vocab";
import { classifyVerb, type Verb } from "./classify";

/** The wire type for one activity-log entry, named for this domain. */
export type ActivityLog = MetadataLogEntry;

// ── Geometry ────────────────────────────────────────────────────────────────

/** Sparkline alphabet: U+2581…U+2588 (▁▂▃▄▅▆▇█). EAW Neutral, single-cell. */
export const SPARKLINE_GLYPHS = ["▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"] as const;

/** Sparkline geometry: one bucket per 8 s of activity. */
export const SPARKLINE_BUCKET_MS = 8_000;

/** Zoom factor: terminal columns per time bucket. A terminal can't scale a
 *  glyph, so "zoom" means giving each bucket more cells — the histogram bar
 *  gets chunkier and each verb glyph sits centred in its own slot with air
 *  either side instead of smushing against its neighbours. Trade-off: the
 *  visible window shrinks by the same factor (44 cols at 3 ≈ 14 buckets
 *  ≈ 2 min of history). */
export const BUCKET_COLS = 3;

/** Histogram height in terminal rows. Rows stack: the bottom row fills first
 *  (▂…█), then the row above starts growing on top of a full block below. */
export const SPARK_ROWS = 2;

/** Distinct non-zero heights per row: ▂…█ (▁ is reserved for the zero
 *  baseline so "one event" is never confused with "no events"). */
const STEPS_PER_ROW = SPARKLINE_GLYPHS.length - 1; // 7

/** Total distinct non-zero heights across all rows. */
export const SPARK_STEPS = SPARK_ROWS * STEPS_PER_ROW;

/** Glyph placement inside a BUCKET_COLS-wide slot: centred, biased left when
 *  the slot width is even (3 → " X ", 2 → "X "). */
const SLOT_PAD_LEFT = " ".repeat(Math.floor((BUCKET_COLS - 1) / 2));
const SLOT_PAD_RIGHT = " ".repeat(BUCKET_COLS - 1 - SLOT_PAD_LEFT.length);
export const BLANK_SLOT = " ".repeat(BUCKET_COLS);

/** Cell layout for a content area `contentWidth` columns wide. Columns that
 *  don't fill a whole bucket pad the LEFT edge, so the newest bucket always
 *  hugs the right edge where the eye expects "now". Rows never exceed
 *  contentWidth (buckets floors at 1, so panes narrower than one slot —
 *  under ~5 total columns — degrade by right-edge truncation only there). */
export function seismographGeometry(contentWidth: number): { buckets: number; leftoverCols: number } {
  const buckets = Math.max(1, Math.floor(contentWidth / BUCKET_COLS));
  return { buckets, leftoverCols: Math.max(0, contentWidth - buckets * BUCKET_COLS) };
}

/** Visible window duration for a bucket count. */
export function windowMs(buckets: number): number {
  return buckets * SPARKLINE_BUCKET_MS;
}

// ── Time bucketing ──────────────────────────────────────────────────────────

/**
 * Map a log timestamp to its bucket index (0 = oldest, `cells-1` = freshest),
 * or -1 when the log has aged out of the window.
 *
 * Both the histogram and the icon row route through this ONE function — the
 * design contract that bar slot N and glyph slot N cover the same 8 s bucket
 * holds by construction, not by keeping two copies in sync.
 *
 * Future timestamps (wall-clock stepped backwards, e.g. NTP correction, or a
 * log that landed just after a quantised `now`) clamp into the freshest
 * bucket instead of vanishing: live activity must never render as silence.
 */
export function bucketIndex(ts: number, now: number, cells: number): number {
  const ageMs = now - ts;
  if (ageMs < 0) return cells - 1;
  if (ageMs >= windowMs(cells)) return -1;
  return cells - 1 - Math.floor(ageMs / SPARKLINE_BUCKET_MS);
}

/**
 * Bucket log entries into a `cells`-wide window. Returns `cells` counts,
 * index 0 oldest, index `cells-1` freshest.
 */
export function bucketSparklineLogs(logs: readonly { ts: number }[], now: number, cells: number): number[] {
  const buckets = new Array(cells).fill(0);
  for (const log of logs) {
    const idx = bucketIndex(log.ts, now, cells);
    if (idx >= 0) buckets[idx]++;
  }
  return buckets;
}

// ── Histogram rendering ─────────────────────────────────────────────────────

/**
 * Render bucket counts to SPARK_ROWS strings of block glyphs, top row first
 * (one glyph per bucket — see expandSparklineRows for column expansion).
 *
 * Y-axis: square-root scale against a fixed frame of SPARK_STEPS events
 * (stretching to the local max only when a burst exceeds it). Sqrt spreads
 * the typical 1–6 events/bucket range across most of the frame (1 → 4/14,
 * 4 → 7/14, 6 → 9/14) instead of leaving it hugging the baseline, while
 * still compressing bursts — a linear axis renders agent activity as
 * mostly-flat-with-rare-spikes. Zero counts render as `▁` on the bottom row
 * (visible flat baseline, never blank — calm reads as a continuous line)
 * and as blank space on the rows above.
 */
export function sparklineRows(buckets: readonly number[]): string[] {
  const cap = Math.max(SPARK_STEPS, ...buckets);
  const levels = buckets.map((c) => {
    if (c <= 0) return 0;
    return Math.max(1, Math.round(SPARK_STEPS * Math.sqrt(c / cap)));
  });
  const rows: string[] = [];
  for (let r = SPARK_ROWS - 1; r >= 0; r--) { // top row first
    let out = "";
    for (const lvl of levels) {
      const filled = lvl - r * STEPS_PER_ROW; // this row's share of the bar
      if (filled <= 0) out += r === 0 ? SPARKLINE_GLYPHS[0] : " ";
      else out += SPARKLINE_GLYPHS[Math.min(STEPS_PER_ROW, filled)];
    }
    rows.push(out);
  }
  return rows;
}

/**
 * Expand per-bucket histogram rows to full content width: left-pad the
 * leftover columns (baseline on the bottom row, blank above) and widen each
 * bucket glyph to its BUCKET_COLS-wide slot.
 */
export function expandSparklineRows(rows: readonly string[], contentWidth: number): string[] {
  const { leftoverCols } = seismographGeometry(contentWidth);
  return rows.map((row, i) => {
    const isBottom = i === rows.length - 1;
    let out = (isBottom ? SPARKLINE_GLYPHS[0] : " ").repeat(leftoverCols);
    for (const ch of row) out += ch.repeat(BUCKET_COLS);
    return out;
  });
}

// ── Icon row ────────────────────────────────────────────────────────────────

/**
 * Per-bucket icon for the glyph row rendered directly beneath the histogram.
 * Shares the histogram's time axis via bucketIndex, so each glyph sits under
 * the bar it contributed to and scrolls left with it as the window slides.
 *
 * Bucket collapse rules (a bucket can hold many logs, the row shows one glyph):
 *   1. any system tag in the bucket → tag glyph (bell), tone-coloured.
 *      Attention signals must never be swallowed by busier neighbours —
 *      this is the seismograph's descendant of the old spec's Rule 0.
 *   2. any error in the bucket     → error cross
 *   3. otherwise                   → newest log's verb glyph (misc fallback)
 * Empty buckets stay blank — the row reads as a timeline, not a list.
 */
export type BucketIcon = { glyph: string; kind: "verb" | "error" | "system"; tone?: ActivityLog["tone"] };

export function bucketIconLogs(logs: readonly ActivityLog[], now: number, cells: number): (BucketIcon | null)[] {
  const newest: (ActivityLog | null)[] = new Array(cells).fill(null);
  const newestTag: (ActivityLog | null)[] = new Array(cells).fill(null);
  const hasError: boolean[] = new Array(cells).fill(false);
  for (const log of logs) {
    const idx = bucketIndex(log.ts, now, cells);
    if (idx < 0) continue;
    if (isSystemTag(log.source) && (!newestTag[idx] || log.ts > newestTag[idx]!.ts)) newestTag[idx] = log;
    if (log.tone === "error") hasError[idx] = true;
    if (!newest[idx] || log.ts > newest[idx]!.ts) newest[idx] = log;
  }
  return newest.map((log, idx) => {
    if (!log) return null;
    const tag = newestTag[idx];
    if (tag) return { glyph: SYSTEM_TAG_GLYPH, kind: "system" as const, tone: tag.tone ?? "info" };
    if (hasError[idx]) return { glyph: ACTIVITY_VERB_ERROR, kind: "error" as const };
    const verb = log.verb ?? classifyVerb(flattenMessage(log.message));
    return { glyph: verb ? VERB_GLYPHS[verb] : ACTIVITY_VERB_MISC, kind: "verb" as const };
  });
}

/** A glyph centred in its BUCKET_COLS-wide slot ("X" → " X " at width 3). */
export function iconSlot(glyph: string): string {
  return SLOT_PAD_LEFT + glyph + SLOT_PAD_RIGHT;
}

// ── Helpers ─────────────────────────────────────────────────────────────────

/**
 * Format a positive duration as a ≤3-char `·Nm`-style suffix payload.
 *
 * Returns `45s`, `2m`, `15m`, `1h`, `2d` etc. Caller prepends `·`.
 * Rounds to the next-coarser unit at 60s/60m/24h boundaries.
 */
export function formatRelTime(deltaMs: number): string {
  const sec = Math.max(0, Math.floor(deltaMs / 1000));
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h`;
  const day = Math.floor(hr / 24);
  return `${day}d`;
}

/** Test if a source string is a system tag like `[bell]` or `[event:foo]`. */
export function isSystemTag(source: string | undefined): source is string {
  return !!source && source.startsWith("[") && source.endsWith("]");
}

/** Icon for system-tagged sources. Every tag currently maps to the bell-alert
 *  glyph as a generic "system event" mark (reuses the severity-glyph entry —
 *  no new glyph budget); grow this into a lookup when the runtime emits tags
 *  that deserve distinct icons. */
export const SYSTEM_TAG_GLYPH = SEV_WAITING;

/** 10-entry verb glyph dictionary. See vocab.ts and classify.ts. */
export const VERB_GLYPHS: Record<Verb, string> = {
  read:     ACTIVITY_VERB_READ,
  list:     ACTIVITY_VERB_LIST,
  search:   ACTIVITY_VERB_SEARCH,
  edit:     ACTIVITY_VERB_EDIT,
  run:      ACTIVITY_VERB_RUN,
  web:      ACTIVITY_VERB_WEB,
  task:     ACTIVITY_VERB_TASK,
  skill:    ACTIVITY_VERB_SKILL,
  thinking: ACTIVITY_VERB_THINKING,
  error:    ACTIVITY_VERB_ERROR,
};

/**
 * Flatten newlines and collapse internal whitespace runs to single spaces
 * before verb classification (classify.ts patterns are single-line).
 */
function flattenMessage(s: string): string {
  return s.replace(/\s+/g, ' ').trim();
}
