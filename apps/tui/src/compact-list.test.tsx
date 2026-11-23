import { describe, expect, test } from "bun:test";
import { testRender } from "@opentui/solid";
import { RGBA, type CapturedFrame } from "@opentui/core";
import { BUILTIN_THEMES, type SessionData } from "@tcm/runtime";
import type { AttentionAgent } from "./attention";
import { CompactSession } from "./compact-list";
import { getScenario } from "./mocks/scenarios";
import { sessionRows, visibleAgentCounts } from "./session-list-rows";
import { AGENT_GLYPHS, SEV_READY } from "./vocab";

const NOW = 1_700_000_000_000;
const palette = Object.values(BUILTIN_THEMES)[0]!.palette;

function agent(threadName: string, over: Partial<AttentionAgent> = {}): AttentionAgent {
  return {
    agent: "claude-code", session: "s", status: "done", liveness: "alive", ts: NOW,
    threadName, threadId: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeee1234", windowIndex: 1, ...over,
  };
}

function session(name: string, agents: AttentionAgent[]): SessionData {
  return {
    name, createdAt: 0, dir: "/x", branch: "main", dirty: false, isWorktree: false, unseen: false,
    panes: 1, windows: 1, uptime: "", agentState: agents[0] ?? null, agents, eventTimestamps: [],
  } as SessionData;
}

interface ListOpts { width: number; sessions: SessionData[]; focused?: string; visible?: number[] }

/** The compact list's frame at `width` columns, cut above the END marker. */
async function renderList(opts: ListOpts): Promise<{ lines: string[]; frame: CapturedFrame }> {
  const { renderOnce, captureCharFrame, captureSpans, renderer } = await testRender(() => (
    <box flexDirection="column">
      {opts.sessions.map((s, i) => (
        <CompactSession
          session={s}
          isSelected={s.name === opts.focused}
          isCurrent={s.name === opts.focused}
          visibleAgentCount={opts.visible?.[i] ?? s.agents.length}
          spinIdx={() => 0}
          glowT={() => 0}
          palette={() => palette}
          onSelect={() => {}}
          panelFocus={() => "sessions"}
          focusedAgentIdx={() => 0}
          focusedPaneId={() => null}
          onAgentDismiss={() => {}}
          onAgentFocus={() => {}}
        />
      ))}
      <text>END</text>
    </box>
  ), { width: opts.width, height: 40 });
  await renderOnce();
  const lines = captureCharFrame().split("\n").map((line) => line.trimEnd());
  const frame = captureSpans();
  renderer.destroy();
  const end = lines.findIndex((line) => line.startsWith("END"));
  expect(end).toBeGreaterThan(0);
  return { lines: lines.slice(0, end), frame };
}

/** Foreground colour of the first cell in `line` holding `ch`. */
function fgAt(frame: CapturedFrame, line: number, ch: string): RGBA | undefined {
  let col = 0;
  for (const span of frame.lines[line]!.spans) {
    if (span.text.includes(ch)) return span.fg;
    col += span.width;
  }
  return undefined;
}

const glyph = AGENT_GLYPHS["claude-code"]!;
const TWO = [
  session("tail-claude-mux", [agent("fix-needs-jump"), agent("sidebar-mode")]),
  session("dotfiles", [agent("tmux-nav")]),
];

describe("compact session list", () => {
  test("width 33: one header row per session, one row per agent, icons and count at the right edge", async () => {
    const { lines } = await renderList({ width: 33, sessions: TWO });
    expect(lines.length).toBe(5);
    expect(lines[0]).toMatch(/^tail-claude-mux ─+ 2$/);
    expect(lines[3]).toMatch(/^dotfiles ─+ 1$/);
    expect(lines[1]).toContain(`${glyph}  fix-needs-jump`);
    expect(lines[1]!.endsWith(SEV_READY)).toBe(true);
    // Count and icon share the right-edge column (paddingRight 1).
    for (const i of [0, 1, 2, 3, 4]) expect([...lines[i]!].length).toBe(32);
  });

  test("narrow width 20: rows still fit and keep the count and icon", async () => {
    const { lines } = await renderList({ width: 20, sessions: TWO });
    expect(lines.length).toBe(5);
    expect(lines[0]).toMatch(/^tail-claude-mux ─+ 2$/);
    expect(lines[3]).toMatch(/^dotfiles ─+ 1$/);
    for (const line of lines) expect([...line].length).toBeLessThanOrEqual(19);
    expect(lines[1]!.endsWith(SEV_READY)).toBe(true);
  });

  test("a long thread name hard-clips with no ellipsis", async () => {
    const long = "a-really-long-thread-name-that-cannot-possibly-fit";
    const { lines } = await renderList({ width: 33, sessions: [session("s", [agent(long)])] });
    expect(lines.length).toBe(2);
    expect(lines[1]).not.toContain("…");
    expect(lines[1]).toContain("a-really-long");
    expect(lines[1]!.endsWith(SEV_READY)).toBe(true);
  });

  test("a session with no live agents shows no count", async () => {
    const { lines } = await renderList({ width: 33, sessions: [session("idle", [])] });
    expect(lines).toEqual([expect.stringMatching(/^idle ─+$/)]);
  });

  test("the count caps at 9+", async () => {
    const many = Array.from({ length: 12 }, (_, i) => agent(`t${i}`));
    const { lines } = await renderList({ width: 33, sessions: [session("busy", many)], visible: [2] });
    expect(lines[0]).toMatch(/^busy ─+ 9\+$/);
    expect(lines[3]).toContain("+10 more");
  });

  test("the focused session's header rule takes the selected-card border colour", async () => {
    const { frame } = await renderList({ width: 33, sessions: TWO, focused: "dotfiles" });
    expect(fgAt(frame, 3, "─")).toEqual(RGBA.fromHex(palette.blue));
    expect(fgAt(frame, 0, "─")).toEqual(RGBA.fromHex(palette.surface2));
  });

  test("each session's drawn height matches the row maths", async () => {
    const sessions = [session("a", [agent("x"), agent("y")]), session("b", [agent("z")])];
    const { lines } = await renderList({ width: 33, sessions });
    const counts = visibleAgentCounts("compact", sessions, 40);
    expect(lines.length).toBe(sessions.reduce((n, s, i) => n + sessionRows("compact", s, counts[i]!), 0));
  });

  test("mock scenario 'live' in compact mode", async () => {
    const scenario = getScenario("live")!;
    const { lines } = await renderList({ width: 33, sessions: scenario.sessions, focused: scenario.focusedSession ?? undefined });
    expect(lines[0]).toMatch(/^ai-engineering-template ─+ 1$/);
    expect(lines.length).toBe(scenario.sessions.reduce((n, s) => n + 1 + (s.agents?.length ?? 0), 0));
  });
});
