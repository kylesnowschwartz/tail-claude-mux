import { describe, expect, test } from "bun:test";

import {
  type ActivityLog,
  SPARKLINE_BUCKET_MS,
  BUCKET_COLS,
  SPARK_ROWS,
  SPARK_STEPS,
  BLANK_SLOT,
  seismographGeometry,
  windowMs,
  bucketIndex,
  bucketSparklineLogs,
  sparklineRows,
  expandSparklineRows,
  bucketIconLogs,
  iconSlot,
  formatRelTime,
  isSystemTag,
  SYSTEM_TAG_GLYPH,
  VERB_GLYPHS,
} from "./activity";
import { ACTIVITY_VERB_ERROR, ACTIVITY_VERB_MISC } from "./vocab";

const NOW = 1_750_000_000_000;

function log(overrides: Partial<ActivityLog> & { ts: number }): ActivityLog {
  return { message: "Reading file.ts", tone: "neutral", ...overrides };
}

describe("bucketIndex", () => {
  test("newest bucket is the last index; each 8 s step is one bucket left", () => {
    expect(bucketIndex(NOW, NOW, 4)).toBe(3);
    expect(bucketIndex(NOW - SPARKLINE_BUCKET_MS, NOW, 4)).toBe(2);
    expect(bucketIndex(NOW - SPARKLINE_BUCKET_MS - 1, NOW, 4)).toBe(2);
  });

  test("logs older than the window return -1", () => {
    expect(bucketIndex(NOW - windowMs(4), NOW, 4)).toBe(-1);
    expect(bucketIndex(NOW - windowMs(4) + 1, NOW, 4)).toBe(0);
  });

  test("future timestamps clamp into the freshest bucket, never vanish", () => {
    // Wall clock stepped backwards (NTP), or a log arrived after a quantised
    // `now` — live activity must not render as silence.
    expect(bucketIndex(NOW + 120_000, NOW, 4)).toBe(3);
  });
});

describe("bucketSparklineLogs", () => {
  test("counts per bucket, newest at the last index", () => {
    const logs = [
      log({ ts: NOW }),
      log({ ts: NOW - SPARKLINE_BUCKET_MS }),
      log({ ts: NOW - SPARKLINE_BUCKET_MS - 1 }),
    ];
    expect(bucketSparklineLogs(logs, NOW, 4)).toEqual([0, 0, 2, 1]);
  });

  test("all-quiet window is all zeros", () => {
    expect(bucketSparklineLogs([], NOW, 3)).toEqual([0, 0, 0]);
  });
});

describe("sparklineRows", () => {
  test("returns SPARK_ROWS rows, top first, same width as buckets", () => {
    const rows = sparklineRows([0, 1, 2]);
    expect(rows).toHaveLength(SPARK_ROWS);
    for (const row of rows) expect([...row]).toHaveLength(3);
  });

  test("zero renders as baseline on the bottom row, blank above", () => {
    const rows = sparklineRows([0]);
    expect(rows[SPARK_ROWS - 1]).toBe("▁");
    expect(rows[0]).toBe(" ");
  });

  test("sqrt scale: small counts are visible bumps, not baseline", () => {
    const [top, bottom] = sparklineRows([1, 4]);
    expect(bottom).toBe("▅█"); // 1 → 4/14, 4 → 7/14 (full bottom row)
    expect(top).toBe("  ");
  });

  test("counts past one row stack into the row above", () => {
    const [top, bottom] = sparklineRows([6, SPARK_STEPS]);
    expect(bottom).toBe("██");
    expect(top).toBe("▃█"); // 6 → 9/14 (2 steps into top row), 14 → full tower
  });

  test("bursts beyond the frame rescale instead of clipping neighbours flat", () => {
    const [, bottom] = sparklineRows([1, 56]); // cap = 56; 1 → round(14·√(1/56)) = 2
    expect(bottom[0]).toBe("▃");
    expect(bottom[1]).toBe("█");
  });
});

describe("geometry and column expansion", () => {
  test("rows never exceed contentWidth; leftover columns pad the left edge", () => {
    for (const cw of [8, 10, 11, 12, 30, 44, 68]) {
      const { buckets, leftoverCols } = seismographGeometry(cw);
      expect(buckets).toBeGreaterThanOrEqual(1);
      expect(buckets * BUCKET_COLS + leftoverCols).toBeLessThanOrEqual(cw);
      const rows = expandSparklineRows(sparklineRows(new Array(buckets).fill(1)), cw);
      for (const row of rows) expect([...row].length).toBeLessThanOrEqual(cw);
    }
  });

  test("histogram slot and icon slot share the same columns (alignment invariant)", () => {
    // One log, 40 s old → one occupied bucket. The expanded bar's columns
    // must be exactly the columns the icon row assigns to that bucket.
    const cw = 44;
    const { buckets, leftoverCols } = seismographGeometry(cw);
    const logs = [log({ ts: NOW - 40_000 })];
    const idx = bucketIndex(NOW - 40_000, NOW, buckets);

    const bottom = expandSparklineRows(sparklineRows(bucketSparklineLogs(logs, NOW, buckets)), cw).at(-1)!;
    const barCols = [...bottom].flatMap((ch, col) => (ch !== "▁" ? [col] : []));
    const slotStart = leftoverCols + idx * BUCKET_COLS;
    expect(barCols).toEqual([slotStart, slotStart + 1, slotStart + 2]);

    // The icon row places its glyph centred inside the same slot.
    const icons = bucketIconLogs(logs, NOW, buckets);
    const rowText = " ".repeat(leftoverCols) + icons.map((c) => (c ? iconSlot(c.glyph) : BLANK_SLOT)).join("");
    expect(rowText[slotStart + Math.floor((BUCKET_COLS - 1) / 2)]).toBe(VERB_GLYPHS.read);
    expect([...rowText].length).toBe(leftoverCols + buckets * BUCKET_COLS);
  });

  test("iconSlot centres the glyph in a BUCKET_COLS-wide slot", () => {
    expect(iconSlot("X")).toHaveLength(BUCKET_COLS);
    expect(BLANK_SLOT).toHaveLength(BUCKET_COLS);
  });
});

describe("bucketIconLogs", () => {
  test("empty buckets are null; occupied buckets carry the newest log's verb", () => {
    const icons = bucketIconLogs(
      [
        log({ ts: NOW, message: "Editing main.ts", verb: "edit" }),
        log({ ts: NOW - 1, message: "Reading main.ts", verb: "read" }),
      ],
      NOW,
      3,
    );
    expect(icons[0]).toBeNull();
    expect(icons[1]).toBeNull();
    expect(icons[2]).toEqual({ glyph: VERB_GLYPHS.edit, kind: "verb" });
  });

  test("producer verb tag wins over message classification", () => {
    const [icon] = bucketIconLogs([log({ ts: NOW, message: "Reading foo.ts", verb: "web" })], NOW, 1);
    expect(icon).toEqual({ glyph: VERB_GLYPHS.web, kind: "verb" });
  });

  test("falls back to message classification, then to the misc glyph", () => {
    const [classified] = bucketIconLogs([log({ ts: NOW, message: "Reading foo.ts" })], NOW, 1);
    expect(classified).toEqual({ glyph: VERB_GLYPHS.read, kind: "verb" });

    const [misc] = bucketIconLogs([log({ ts: NOW, message: "zorble the plumbus" })], NOW, 1);
    expect(misc).toEqual({ glyph: ACTIVITY_VERB_MISC, kind: "verb" });
  });

  test("any error in the bucket wins over a newer success", () => {
    const icons = bucketIconLogs(
      [
        log({ ts: NOW, message: "Reading foo.ts" }),
        log({ ts: NOW - 1, message: "Running tests (failed)", tone: "error" }),
      ],
      NOW,
      1,
    );
    expect(icons[0]).toEqual({ glyph: ACTIVITY_VERB_ERROR, kind: "error" });
  });

  test("a system tag wins over BOTH a newer log and an error in the bucket", () => {
    // Attention signals must never be swallowed — descendant of old Rule 0.
    const icons = bucketIconLogs(
      [
        log({ ts: NOW, message: "Reading foo.ts" }),
        log({ ts: NOW - 500, message: "Running tests (failed)", tone: "error" }),
        log({ ts: NOW - 1_000, message: "awaiting confirmation", source: "[bell]", tone: "warn" }),
      ],
      NOW,
      1,
    );
    expect(icons[0]).toEqual({ glyph: SYSTEM_TAG_GLYPH, kind: "system", tone: "warn" });
  });
});

describe("system tags", () => {
  test("isSystemTag matches bracketed sources only", () => {
    expect(isSystemTag("[bell]")).toBe(true);
    expect(isSystemTag("[event:foo]")).toBe(true);
    expect(isSystemTag("pi db92")).toBe(false);
    expect(isSystemTag(undefined)).toBe(false);
  });
});

describe("formatRelTime", () => {
  test("rounds to the next-coarser unit at each boundary", () => {
    expect(formatRelTime(45_000)).toBe("45s");
    expect(formatRelTime(60_000)).toBe("1m");
    expect(formatRelTime(59 * 60_000)).toBe("59m");
    expect(formatRelTime(60 * 60_000)).toBe("1h");
    expect(formatRelTime(24 * 3_600_000)).toBe("1d");
  });
});
