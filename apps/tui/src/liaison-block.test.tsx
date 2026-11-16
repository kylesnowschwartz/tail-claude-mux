import { describe, expect, test } from "bun:test";
import { testRender } from "@opentui/solid";
import { RGBA, TextAttributes, type CapturedFrame } from "@opentui/core";
import { BUILTIN_THEMES, type LiaisonItem, type SessionData } from "@tcm/runtime";
import { liaisonBlockRows, liaisonItems, type AttentionAgent } from "./attention";
import { LiaisonBlock, LiaisonWorkerMarks } from "./liaison-block";
import { tier } from "./tiers";
import { LIAISON_FINISHED_GLYPH, LIAISON_GLYPH, LIAISON_ITEM_GLYPHS, LIAISON_WORKER_GLYPH, LIAISON_WORKERS_MARK } from "./vocab";

const NOW = 1_700_000_000_000;
const MIN = 60_000;
const palette = Object.values(BUILTIN_THEMES)[0]!.palette;

const liaison: AttentionAgent = {
  agent: "claude-code", session: "home", status: "running", liveness: "alive",
  ts: NOW, threadName: "liaison", threadId: "11111111-2222-4333-8444-555555555555",
};

/** Five reported items, one per kind and age, in no particular order. */
const REPORTED: LiaisonItem[] = [
  { kind: "reminder", title: "renew the staging certificate", since: NOW - 50 * MIN },
  { kind: "answer", title: "worker fix-tax answered the schema question", since: NOW - 4 * MIN },
  { kind: "blocked", title: "deploy waits on a production key", since: NOW - 12 * MIN },
  { kind: "no_report", title: "check-dates has not reported", since: NOW - 30 * MIN },
  { kind: "reminder", title: "read the release notes", since: NOW - 20 * MIN },
];

function home(items: LiaisonItem[], statusText = "reading every report"): SessionData {
  return {
    name: "home", createdAt: 0, dir: "/x", branch: "", dirty: false, isWorktree: false, unseen: false,
    panes: 1, windows: 1, uptime: "", agentState: liaison, agents: [liaison], eventTimestamps: [],
    metadata: { status: { text: statusText, ts: NOW }, progress: null, logs: [], liaisonItems: items },
  } as SessionData;
}

interface BlockOpts { width: number; items?: number; workers?: number; asks?: boolean; blocks?: number }

function ref(opts: BlockOpts) {
  const agent: AttentionAgent = opts.asks
    ? { ...liaison, status: "waiting", attention: "permission", attentionSince: NOW - 2 * MIN }
    : liaison;
  return { session: home(REPORTED.slice(0, opts.items ?? 0)), agent };
}

/** The blocks' rendered frame at `width` columns, cut above the END marker. */
async function renderBlocks(opts: BlockOpts): Promise<{ lines: string[]; frame: CapturedFrame }> {
  const blocks = Array.from({ length: opts.blocks ?? 1 }, () => ref(opts));
  const { renderOnce, captureCharFrame, captureSpans, renderer } = await testRender(() => (
    <box flexDirection="column">
      {blocks.map((r) => (
        <LiaisonBlock
          liaison={r}
          palette={() => palette}
          nowMs={() => NOW}
          items={() => liaisonItems(r)}
          workers={() => opts.workers ?? 0}
        />
      ))}
      <text>END</text>
    </box>
  ), { width: opts.width, height: 30 });
  await renderOnce();
  const lines = captureCharFrame().split("\n").map((line) => line.trimEnd());
  const frame = captureSpans();
  renderer.destroy();
  const end = lines.findIndex((line) => line.startsWith("END"));
  expect(end).toBeGreaterThan(0);
  return { lines: lines.slice(0, end), frame };
}

/** A palette colour as the renderer stores it. */
function frameColor(color: string): RGBA {
  return RGBA.fromHex(color);
}

function hex(c: RGBA): string {
  return "#" + [c.r, c.g, c.b].map((v) => Math.round(v * 255).toString(16).padStart(2, "0")).join("");
}

/** Foreground colour of the first span on `row` that contains `text`. */
function fgOf(frame: CapturedFrame, row: number, text: string): { fg: string; dim: boolean } {
  const span = frame.lines[row]!.spans.find((s) => s.text.includes(text));
  expect(span).toBeDefined();
  return { fg: hex(span!.fg), dim: (span!.attributes & TextAttributes.DIM) !== 0 };
}

const RULE = /^\s*─+$/;
const NO_OLD_ROWS = /^\s*(fleet|needs|work|status)\b/;

describe("LiaisonBlock", () => {
  test("rendered rows equal liaisonBlockRows in every case", async () => {
    for (const width of [60, 40, 24, 16, 12]) {
      for (const items of [0, 1, 3, 5]) {
        for (const workers of [0, 2]) {
          for (const asks of [false, true]) {
            for (const blocks of [1, 2]) {
              const opts = { width, items, workers, asks, blocks };
              const { lines } = await renderBlocks(opts);
              const expected = blocks * liaisonBlockRows(items + (asks ? 1 : 0));
              expect({ ...opts, rows: lines.length }).toEqual({ ...opts, rows: expected });
              expect(lines.filter((line) => NO_OLD_ROWS.test(line))).toEqual([]);
              expect(lines.some((line) => line.includes("reading every report"))).toBe(false);
            }
          }
        }
      }
    }
  });

  test("at rest the block is one dim title row and its rule", async () => {
    const { lines, frame } = await renderBlocks({ width: 40 });
    expect(lines).toHaveLength(2);
    expect(lines[0]!.trim()).toBe(`${LIAISON_GLYPH} LIAISON`);
    expect(lines[1]).toMatch(RULE);
    expect(fgOf(frame, 0, "LIAISON")).toEqual({ fg: hex(frameColor(palette.text)), dim: true });
  });

  test("running workers add ↳N at the right of the title row", async () => {
    const { lines, frame } = await renderBlocks({ width: 40, workers: 2 });
    expect(lines).toHaveLength(2);
    expect(lines[0]!.endsWith(`${LIAISON_WORKERS_MARK}2`)).toBe(true);
    expect(fgOf(frame, 0, LIAISON_WORKERS_MARK).dim).toBe(true);
  });

  test("the title takes the colour of the highest priority present", async () => {
    const cases: [LiaisonItem["kind"], string][] = [["reminder", palette.blue], ["answer", palette.yellow], ["blocked", palette.red]];
    for (const [kind, color] of cases) {
      const r = { session: home([{ kind, title: "x", since: NOW }, { kind: "reminder", title: "y", since: NOW }]), agent: liaison };
      const { renderOnce, captureSpans, renderer } = await testRender(() => (
        <LiaisonBlock liaison={r} palette={() => palette} nowMs={() => NOW} items={() => liaisonItems(r)} workers={() => 0} />
      ), { width: 40, height: 10 });
      await renderOnce();
      const frame = captureSpans();
      renderer.destroy();
      expect({ kind, fg: fgOf(frame, 0, "LIAISON").fg }).toEqual({ kind, fg: hex(frameColor(color)) });
    }
  });

  test("an asks item from the liaison's own conversation shows in red", async () => {
    const { lines, frame } = await renderBlocks({ width: 40, asks: true });
    expect(lines).toHaveLength(3);
    expect(lines[1]!.trim()).toMatch(new RegExp(`^${LIAISON_ITEM_GLYPHS[1]} permission\\s+2m$`));
    expect(fgOf(frame, 1, LIAISON_ITEM_GLYPHS[1]).fg).toBe(hex(frameColor(palette.red)));
    expect(fgOf(frame, 0, "LIAISON")).toEqual({ fg: hex(frameColor(palette.red)), dim: false });
  });

  test("items run by priority, then oldest first, with glyph and age", async () => {
    const { lines } = await renderBlocks({ width: 60, items: 3 });
    expect(lines.slice(1, 4).map((line) => line.trim())).toEqual([
      expect.stringMatching(/^■ deploy waits on a production key\s+12m$/),
      expect.stringMatching(/^▲ worker fix-tax answered the schema question\s+4m$/),
      expect.stringMatching(/^● renew the staging certificate\s+50m$/),
    ]);
  });

  test("more than three items end the last row with +K", async () => {
    const { lines } = await renderBlocks({ width: 60, items: 5, asks: true });
    expect(lines).toHaveLength(5);
    expect(lines.slice(1, 4).map((line) => line.trim()[0])).toEqual(["■", "■", "▲"]);
    expect(lines[3]!.endsWith("30m +3")).toBe(true);
    expect(lines.filter((line) => /\+\d+$/.test(line))).toHaveLength(1);
  });

  test("a narrow sidebar cuts the item title and keeps the age", async () => {
    const { lines } = await renderBlocks({ width: 16, items: 1, workers: 2 });
    expect(lines[1]!.trim()).toBe("● renew th 50m");
    expect(lines[0]!.endsWith(`${LIAISON_WORKERS_MARK}2`)).toBe(true);
  });

  test("text renderings at width 36", async () => {
    for (const [name, opts] of [
      ["rest", { width: 36 }],
      ["workers running", { width: 36, workers: 2 }],
      ["items, more than 3", { width: 36, items: 5, workers: 2, asks: true }],
    ] as const) {
      const { lines } = await renderBlocks(opts);
      console.log(`--- ${name}\n${lines.join("\n")}`);
    }
  });

  test("a finished worker row shows a grey mark after the worker mark", async () => {
    for (const finished of [false, true]) {
      const { renderOnce, captureCharFrame, captureSpans, renderer } = await testRender(() => (
        <text wrapMode="none">
          <span>c</span>
          <LiaisonWorkerMarks palette={() => palette} finished={finished} finishedStyle={tier("muted", palette)} />
          <span>{" fix-tax"}</span>
        </text>
      ), { width: 30, height: 2 });
      await renderOnce();
      const line = captureCharFrame().split("\n")[0]!.trimEnd();
      const frame = captureSpans();
      renderer.destroy();
      if (!finished) {
        expect(line).toBe(`c  ${LIAISON_WORKER_GLYPH} fix-tax`);
        continue;
      }
      expect(line).toBe(`c  ${LIAISON_WORKER_GLYPH} ${LIAISON_FINISHED_GLYPH} fix-tax`);
      expect(fgOf(frame, 0, LIAISON_FINISHED_GLYPH).fg).toBe(hex(frameColor(palette.overlay0)));
    }
  });
});
