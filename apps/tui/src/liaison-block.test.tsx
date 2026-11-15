import { describe, expect, test } from "bun:test";
import { testRender } from "@opentui/solid";
import { BUILTIN_THEMES, type SessionData } from "@tcm/runtime";
import { liaisonBlockRows, type AttentionAgent } from "./attention";
import { LiaisonBlock } from "./liaison-block";

const NOW = 1_700_000_000_000;
const palette = Object.values(BUILTIN_THEMES)[0]!.palette;

const liaison: AttentionAgent = {
  agent: "claude-code", session: "home", status: "running", liveness: "alive",
  ts: NOW, threadName: "liaison", threadId: "11111111-2222-4333-8444-555555555555",
};

function home(statusText: string): SessionData {
  return {
    name: "home", createdAt: 0, dir: "/x", branch: "", dirty: false, isWorktree: false, unseen: false,
    panes: 1, windows: 1, uptime: "", agentState: liaison, agents: [liaison], eventTimestamps: [],
    metadata: statusText ? { status: { text: statusText, ts: NOW }, progress: null, logs: [] } : undefined,
  } as SessionData;
}

/** The block's rendered lines at `width` columns, without the blank lines under it. */
async function renderBlock(opts: { width: number; workers: number; showFleet: boolean; statusText: string }): Promise<string[]> {
  const { renderOnce, captureCharFrame, renderer } = await testRender(() => (
    <box flexDirection="column">
      <LiaisonBlock
        liaison={{ session: home(opts.statusText), agent: liaison }}
        showFleet={opts.showFleet}
        palette={() => palette}
        nowMs={() => NOW}
        fleet={() => [{ label: "working", count: 4 }, { label: "ready", count: 2 }]}
        workers={() => opts.workers}
        width={() => opts.width}
        needs={() => "2 · oldest 14m"}
        needsCount={() => 2}
      />
      <text>END</text>
    </box>
  ), { width: opts.width, height: 20 });
  await renderOnce();
  const lines = captureCharFrame().split("\n").map((line) => line.trimEnd());
  renderer.destroy();
  const end = lines.findIndex((line) => line.startsWith("END"));
  expect(end).toBeGreaterThan(0);
  return lines.slice(0, end);
}

describe("LiaisonBlock", () => {
  const STATUS = "reading the reports of every worker in this place";

  test("shows the count of workers in words at a wide width", async () => {
    const lines = await renderBlock({ width: 40, workers: 3, showFleet: true, statusText: STATUS });
    expect(lines.filter((line) => line.includes("3 workers"))).toHaveLength(1);
    expect(lines.find((line) => line.includes("3 workers"))!.trimStart().startsWith("work")).toBe(true);
  });

  test("shows no work row for a liaison with no workers", async () => {
    const lines = await renderBlock({ width: 40, workers: 0, showFleet: true, statusText: STATUS });
    expect(lines.some((line) => line.trimStart().startsWith("work "))).toBe(false);
    expect(lines).toHaveLength(liaisonBlockRows(0, true, false));
  });

  test("takes the rows that liaisonBlockRows counts, at a wide and at a narrow width", async () => {
    for (const width of [60, 40, 24, 16, 12]) {
      for (const workers of [0, 1, 3, 12]) {
        for (const showFleet of [true, false]) {
          for (const statusText of [STATUS, ""]) {
            const lines = await renderBlock({ width, workers, showFleet, statusText });
            expect({ width, workers, showFleet, statusText, rows: lines.length }).toEqual({
              width, workers, showFleet, statusText,
              rows: liaisonBlockRows(showFleet ? 0 : 1, !!statusText, workers > 0),
            });
          }
        }
      }
    }
  });

  test("a sidebar too narrow for the words shows the number only", async () => {
    const lines = await renderBlock({ width: 16, workers: 3, showFleet: true, statusText: "" });
    const row = lines.find((line) => line.trimStart().startsWith("work"));
    expect(row).toBeDefined();
    expect(row!.includes("workers")).toBe(false);
    expect(row!.trimEnd().endsWith("3")).toBe(true);
  });
});
