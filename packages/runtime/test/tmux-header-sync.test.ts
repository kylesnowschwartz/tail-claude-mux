import { describe, test, expect } from "bun:test";
import {
  AGENT_GLYPHS,
  AGENT_PRIORITY,
  buildAgentGlyphs,
  isClawdInstalled,
  pickAgentForWindow,
  planTmuxHeaderSync,
  syncTmuxHeaderOptions,
  severityLabel,
  severityColour,
  STATUSLINE_LAST_WINDOW,
  STATUSLINE_SHELL,
  __resetTmuxHeaderSyncStateForTests,
  type PlanInput,
  type SyncedState,
  type SyncDeps,
} from "../src/server/tmux-header-sync";
import { resolveTheme } from "../src/themes";
import type { SessionData } from "../src/shared";
import type { AgentEvent } from "../src/contracts/agent";

// `STATUSLINE_*` constants are still exported from tmux-header-sync (they're
// the runtime's vocabulary surface for statusline-only glyphs) even though
// emission moved to tmux-palette-file.ts. Anchor a smoke check so anyone
// renaming/relocating them notices the test surface needs updating.
void STATUSLINE_LAST_WINDOW;
void STATUSLINE_SHELL;

const THEME = resolveTheme("catppuccin-mocha");
const BLUE = THEME.palette.blue;
const YELLOW = THEME.palette.yellow;
const GREEN = THEME.palette.green;
const RED = THEME.palette.red;
const SURFACE2 = THEME.palette.surface2;

function makeAgent(o: Partial<AgentEvent> & { agent: string; session: string; paneId?: string }): AgentEvent {
  return {
    agent: o.agent,
    session: o.session,
    status: o.status ?? "running",
    ts: o.ts ?? 1,
    paneId: o.paneId,
    liveness: o.liveness ?? "alive",
  };
}

function makeSession(name: string, agents: AgentEvent[]): SessionData {
  return {
    name,
    createdAt: 0,
    dir: "",
    branch: "",
    dirty: false,
    isWorktree: false,
    unseen: false,
    panes: agents.length,
    windows: 1,
    uptime: "",
    agentState: agents[0] ?? null,
    agents,
    eventTimestamps: [],
    metadata: null,
  };
}

function emptyInput(overrides: Partial<PlanInput> = {}): PlanInput {
  return {
    sessions: [],
    theme: THEME,
    enabled: true,
    paneToWindow: new Map(),
    prevWindows: new Map(),
    ...overrides,
  };
}

// --- Glyph table + precedence ---

describe("AGENT_GLYPHS", () => {
  test("has entries for all priority agents and a generic fallback", () => {
    for (const agent of AGENT_PRIORITY) {
      expect(AGENT_GLYPHS[agent]).toBeDefined();
    }
    expect(AGENT_GLYPHS["generic"]).toBeDefined();
  });

  test("all glyphs are non-empty strings", () => {
    for (const value of Object.values(AGENT_GLYPHS)) {
      expect(typeof value).toBe("string");
      expect(value.length).toBeGreaterThan(0);
    }
  });
});

describe("buildAgentGlyphs (Clawd detection)", () => {
  test("emits Clawd codepoint U+100CC0 when font is installed", () => {
    const glyphs = buildAgentGlyphs({ clawdInstalled: true });
    expect(glyphs["claude-code"]).toBe("\u{100CC0}");
  });

  test("falls back to U+2605 (★) when font is not installed", () => {
    const glyphs = buildAgentGlyphs({ clawdInstalled: false });
    expect(glyphs["claude-code"]).toBe("★");
  });

  test("non-clawd glyphs are unaffected by detection state", () => {
    const installed = buildAgentGlyphs({ clawdInstalled: true });
    const missing = buildAgentGlyphs({ clawdInstalled: false });
    for (const key of ["pi", "codex", "amp", "generic"]) {
      expect(installed[key]).toBe(missing[key]!);
    }
  });

  test("isClawdInstalled returns a boolean", () => {
    expect(typeof isClawdInstalled()).toBe("boolean");
  });
});

describe("pickAgentForWindow (E1, E2)", () => {
  test("E1: precedence picks claude-code over pi", () => {
    expect(pickAgentForWindow(["pi", "claude-code"])).toBe("claude-code");
  });

  test("E2: empty input returns generic", () => {
    expect(pickAgentForWindow([])).toBe("generic");
  });

  test("respects the full priority order", () => {
    expect(pickAgentForWindow(["amp", "codex", "pi"])).toBe("pi");
    expect(pickAgentForWindow(["amp", "codex"])).toBe("codex");
    expect(pickAgentForWindow(["amp"])).toBe("amp");
  });

  test("unknown agent name passes through as agents[0] when no priority match", () => {
    expect(pickAgentForWindow(["my-custom-agent"])).toBe("my-custom-agent");
  });
});

// --- Pure planner ---

describe("planTmuxHeaderSync", () => {
  test("S1: single claude-code agent emits glyph + fg + type for that window", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));

    const setAgent = out.commands.find((c) => c.includes("@tcm-agent") && c.includes("@1") && !c.includes("-fg") && !c.includes("-type"));
    expect(setAgent).toEqual(["set-option", "-w", "-t", "@1", "@tcm-agent", AGENT_GLYPHS["claude-code"]!]);
    const setFg = out.commands.find((c) => c.includes("@tcm-agent-fg") && c.includes("@1"));
    expect(setFg?.[5]).toBe(BLUE);
    const setType = out.commands.find((c) => c.includes("@tcm-agent-type") && c.includes("@1"));
    expect(setType?.[5]).toBe("claude-code");

    expect(out.newWindows.get("@1")).toEqual({ glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" });
  });

  test("S2: pi agent emits the pi glyph", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "pi", session: "s1", paneId: "%20" })])];
    const paneToWindow = new Map([["%20", "@5"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@5")?.glyph).toBe(AGENT_GLYPHS["pi"]!);
    expect(out.newWindows.get("@5")?.agent).toBe("pi");
  });

  test("S3: window with pi + claude-code resolves to claude-code (precedence)", () => {
    const sessions = [
      makeSession("s1", [
        makeAgent({ agent: "pi", session: "s1", paneId: "%30" }),
        makeAgent({ agent: "claude-code", session: "s1", paneId: "%31" }),
      ]),
    ];
    const paneToWindow = new Map([
      ["%30", "@7"],
      ["%31", "@7"],
    ]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.size).toBe(1);
    expect(out.newWindows.get("@7")?.agent).toBe("claude-code");
  });

  test("S4: identical state on second call emits zero commands", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const first = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    const second = planTmuxHeaderSync(emptyInput({
      sessions, paneToWindow,
      prevWindows: first.newWindows,
    }));
    expect(second.commands).toEqual([]);
  });

  // Theme + statusline-glyph emission moved to tmux-palette-file.ts
  // (catppuccin pattern). The planner no longer touches palette / glyph
  // tokens — see tmux-palette-file.test.ts for the corresponding behaviour.

  test("S6: enabled === false produces zero commands and resets state", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow, enabled: false }));
    expect(out.commands).toEqual([]);
    expect(out.newWindows.size).toBe(0);
  });

  test("E2: unknown agent type falls back to generic glyph", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "my-novel-agent", session: "s1", paneId: "%10" })])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@1")?.glyph).toBe(AGENT_GLYPHS["generic"]!);
  });

  test("E3: empty sessions + window still alive emits cleanup", () => {
    // @1 is still a live tmux window (it has a pane in paneToWindow), but no
    // longer hosts an agent. Cleanup fires.
    const prevWindows: SyncedState = new Map([["@1", { glyph: "X", fg: "#fff", agent: "claude-code" }]]);
    const paneToWindow = new Map([["%shell", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions: [], paneToWindow, prevWindows }));
    const cleanups = out.commands.filter((c) => c[0] === "set-option" && c[1] === "-wu" && c[3] === "@1");
    expect(cleanups.length).toBe(3); // @tcm-agent, @tcm-agent-fg, @tcm-agent-type
    expect(out.newWindows.size).toBe(0);
  });

  test("E1a: window closed in tmux — no cleanup emitted (would error 'no such window')", () => {
    // Regression: cleanup commands for windows tmux no longer knows about
    // used to abort the whole chained command, wedging lastWindows /
    // lastPalette indefinitely. The planner must filter cleanup against
    // live windows (i.e. paneToWindow.values()).
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const prevWindows: SyncedState = new Map([
      ["@deadWin", { glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" }],
      ["@liveWin", { glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" }],
    ]);
    // @liveWin still has a pane (the agent has just moved off it); @deadWin
    // has no panes at all — the window itself is gone.
    const paneToWindow = new Map([
      ["%10", "@newWin"],
      ["%shell", "@liveWin"],
    ]);
    const out = planTmuxHeaderSync(emptyInput({
      sessions,
      paneToWindow,
      prevWindows,
    }));
    expect(out.commands.filter((c) => c[1] === "-wu" && c[3] === "@deadWin")).toEqual([]);
    expect(out.commands.filter((c) => c[1] === "-wu" && c[3] === "@liveWin").length).toBe(3);
  });

  test("E1b: agent pane destroyed but its window is gone — no cleanup", () => {
    // The original E1 case (pane vanished, window also vanished). Cleanup
    // would error with "no such window".
    const prevWindows: SyncedState = new Map([
      ["@deadWin", { glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" }],
    ]);
    const out = planTmuxHeaderSync(emptyInput({
      sessions: [],
      paneToWindow: new Map(),
      prevWindows,
    }));
    expect(out.commands.filter((c) => c[1] === "-wu" && c[3] === "@deadWin")).toEqual([]);
    expect(out.newWindows.size).toBe(0);
  });

  test("E5: pane moves to a different window — old window cleared if alive, new window set", () => {
    // Old window survives because it still has a sibling pane; new window is
    // where the agent now lives. Both must be in paneToWindow.values() for
    // the planner's live-window filter to keep them.
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const prevWindows: SyncedState = new Map([
      ["@oldWin", { glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" }],
    ]);
    const paneToWindow = new Map([
      ["%10", "@newWin"],
      ["%sibling", "@oldWin"],
    ]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow, prevWindows }));
    const cleared = out.commands.filter((c) => c[1] === "-wu" && c[3] === "@oldWin");
    expect(cleared.length).toBe(3);
    const setOnNew = out.commands.filter((c) => c[1] === "-w" && c[3] === "@newWin");
    expect(setOnNew.length).toBe(3);
  });

  // S5e (same themeName + different palette values re-emits @tcm-thm-*) moved
  // to tmux-palette-file.test.ts: the file body is the new diff key, so any
  // palette-value change writes a new file regardless of themeName.

  test("agents with liveness !== alive are ignored", () => {
    const sessions = [makeSession("s1", [
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%10", liveness: "exited" }),
    ])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.size).toBe(0);
  });

  // "transparent theme palette translates to default" assertion moved to
  // tmux-palette-file.test.ts (the file body is where palette emission lives).

  test("agent fg uses tmux 'default' when theme.blue is transparent", () => {
    // Synthetic theme with transparent blue (defensive — no builtin uses it,
    // but the translation should still apply).
    const synthetic: typeof THEME = {
      ...THEME,
      palette: { ...THEME.palette, blue: "transparent" },
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const out = planTmuxHeaderSync(emptyInput({
      sessions,
      paneToWindow: new Map([["%10", "@1"]]),
      theme: synthetic,
    }));
    expect(out.newWindows.get("@1")?.fg).toBe("default");
  });

  test("agent without paneId is ignored", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1" })])];
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow: new Map() }));
    expect(out.newWindows.size).toBe(0);
  });
});

// --- Severity-aware glyph colour (Stage 5) ---
//
// Mirrors the panel's status→colour map. Whenever these tests change, the
// panel resolver in `apps/tui/src/index.tsx` (search `working/waiting/ready`)
// must move in lockstep.

describe("severityLabel (resolver)", () => {
  test("running → working", () => {
    expect(severityLabel("running", "alive")).toBe("working");
  });
  test("waiting → waiting", () => {
    expect(severityLabel("waiting", "alive")).toBe("waiting");
  });
  test("error → error (regardless of liveness)", () => {
    expect(severityLabel("error", "alive")).toBe("error");
    expect(severityLabel("error", "exited")).toBe("error");
  });
  test("done + alive → ready (process is at prompt)", () => {
    expect(severityLabel("done", "alive")).toBe("ready");
  });
  test("done + exited → stopped", () => {
    expect(severityLabel("done", "exited")).toBe("stopped");
  });
  test("interrupted + exited → stopped", () => {
    expect(severityLabel("interrupted", "exited")).toBe("stopped");
  });
  test("idle + alive → ready", () => {
    expect(severityLabel("idle", "alive")).toBe("ready");
  });
  test("null status with no liveness defaults to ready (cold-start synthetic)", () => {
    expect(severityLabel(null, undefined)).toBe("ready");
  });
});

describe("severityColour (palette mapping)", () => {
  test("working uses theme.blue", () => {
    const a = makeAgent({ agent: "claude-code", session: "s1", status: "running", liveness: "alive" });
    expect(severityColour(a, THEME)).toBe(BLUE);
  });
  test("waiting uses theme.yellow", () => {
    const a = makeAgent({ agent: "claude-code", session: "s1", status: "waiting", liveness: "alive" });
    expect(severityColour(a, THEME)).toBe(YELLOW);
  });
  test("ready uses theme.green", () => {
    const a = makeAgent({ agent: "claude-code", session: "s1", status: "done", liveness: "alive" });
    expect(severityColour(a, THEME)).toBe(GREEN);
  });
  test("stopped uses theme.surface2", () => {
    const a = makeAgent({ agent: "claude-code", session: "s1", status: "done", liveness: "exited" });
    expect(severityColour(a, THEME)).toBe(SURFACE2);
  });
  test("error uses theme.red", () => {
    const a = makeAgent({ agent: "claude-code", session: "s1", status: "error", liveness: "alive" });
    expect(severityColour(a, THEME)).toBe(RED);
  });
});

describe("planTmuxHeaderSync severity-aware fg", () => {
  test("waiting agent emits @tcm-agent-fg = yellow", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10", status: "waiting" })])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@1")?.fg).toBe(YELLOW);
  });

  test("errored agent emits @tcm-agent-fg = red", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "pi", session: "s1", paneId: "%20", status: "error" })])];
    const paneToWindow = new Map([["%20", "@2"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@2")?.fg).toBe(RED);
  });

  test("alive 'done' agent emits @tcm-agent-fg = green (ready, at prompt)", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "codex", session: "s1", paneId: "%30", status: "done", liveness: "alive" })])];
    const paneToWindow = new Map([["%30", "@3"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@3")?.fg).toBe(GREEN);
  });

  test("severity uses the dominant agent (claude-code precedence over pi)", () => {
    // Window has waiting pi + working claude-code. Dominant agent is
    // claude-code by precedence; fg should reflect claude-code's working.
    const sessions = [makeSession("s1", [
      makeAgent({ agent: "pi", session: "s1", paneId: "%40", status: "waiting" }),
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%41", status: "running" }),
    ])];
    const paneToWindow = new Map([["%40", "@4"], ["%41", "@4"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@4")?.agent).toBe("claude-code");
    expect(out.newWindows.get("@4")?.fg).toBe(BLUE);
  });

  test("severity tie-break picks highest-severity entry sharing the dominant name", () => {
    // Two claude-code instances in the same window: idle 'done' (alive=ready)
    // pushed first, running pushed second. The old `.find()` picked the first
    // by push order and rendered green; we want blue (working) to surface.
    const sessions = [makeSession("s1", [
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%50", status: "done", liveness: "alive" }),
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%51", status: "running" }),
    ])];
    const paneToWindow = new Map([["%50", "@5"], ["%51", "@5"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@5")?.agent).toBe("claude-code");
    expect(out.newWindows.get("@5")?.fg).toBe(BLUE);
  });

  test("severity tie-break: error outranks working among same-agent entries", () => {
    const sessions = [makeSession("s1", [
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%60", status: "running" }),
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%61", status: "error" }),
    ])];
    const paneToWindow = new Map([["%60", "@6"], ["%61", "@6"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.get("@6")?.fg).toBe(RED);
  });

  test("status change without agent-type change re-emits a new fg", () => {
    const paneToWindow = new Map([["%10", "@1"]]);
    const first = planTmuxHeaderSync(emptyInput({
      sessions: [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10", status: "running" })])],
      paneToWindow,
    }));
    expect(first.newWindows.get("@1")?.fg).toBe(BLUE);

    const second = planTmuxHeaderSync(emptyInput({
      sessions: [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10", status: "waiting" })])],
      paneToWindow,
      prevWindows: first.newWindows,
    }));
    // fg flipped; the diff should write three @tcm-agent* options for @1.
    expect(second.newWindows.get("@1")?.fg).toBe(YELLOW);
    const writes = second.commands.filter((c) => c[1] === "-w" && c[3] === "@1");
    expect(writes.length).toBe(3);
  });
});

// --- Live sync (with stubbed shell) ---

describe("syncTmuxHeaderOptions (X1)", () => {
  test("X1: empty list-panes output produces no writes and does not throw", () => {
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        return "";
      },
      log: () => {},
    };
    syncTmuxHeaderOptions({ sessions: [], theme: THEME, enabled: true }, deps);
    // Only the list-panes probe ran; no write call.
    expect(calls.length).toBe(1);
    expect(calls[0]?.[0]).toBe("list-panes");
  });

  test("disabled gate short-circuits before list-panes", () => {
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        return "";
      },
      log: () => {},
    };
    syncTmuxHeaderOptions({ sessions: [], theme: THEME, enabled: false }, deps);
    expect(calls.length).toBe(0);
  });

  test("happy path: one agent → list-panes + chained set-option call", () => {
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        if (args[0] === "list-panes") return "%10|@1\n%11|@2";
        return "";
      },
      log: () => {},
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(2);
    expect(calls[1]?.[0]).toBe("set-option");
    // Chain contains the agent + fg + type writes for @1.
    expect(calls[1]).toContain("@tcm-agent");
    expect(calls[1]).toContain("@tcm-agent-fg");
    expect(calls[1]).toContain("@tcm-agent-type");
  });

  test("idempotence: second identical call only runs list-panes", () => {
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        if (args[0] === "list-panes") return "%10|@1";
        return "";
      },
      log: () => {},
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    const firstCallCount = calls.length;
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    // Second call: only list-panes ran; no new set-option chain.
    expect(calls.length).toBe(firstCallCount + 1);
    expect(calls[calls.length - 1]?.[0]).toBe("list-panes");
  });

  test("X2: shellTmux throws on write — error caught, cache reset to self-heal", () => {
    // Write-side failures reset lastWindows so the next broadcast re-emits
    // the full per-window agent state. Safety net for the cleanup-poison
    // bug: even if a regression sneaks a "no such window" into the chain,
    // the sync recovers next tick instead of wedging until process restart.
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    let throwOnWrite = true;
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        if (args[0] === "list-panes") return "%10|@1";
        if (throwOnWrite) throw new Error("write failed");
        return "";
      },
      log: () => {},
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(2);
    expect(calls[1]?.[0]).toBe("set-option");

    // Now writes work. Cache was reset, so the sync re-emits the full agent
    // state for @1 — proving we self-healed rather than skipping work.
    // (Palette tokens are no longer in the chain; they live in
    // tmux-palette-file.ts and don't share this cache.)
    throwOnWrite = false;
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(4);
    expect(calls[3]?.[0]).toBe("set-option");
    const chain = calls[3]!;
    expect(chain).toContain("@tcm-agent");
    expect(chain.some((arg) => typeof arg === "string" && arg.startsWith("@tcm-thm-"))).toBe(false);
  });

  test("X3: read-side throw preserves cache (transient list-panes flake)", () => {
    // Distinct from X2: failures of `list-panes` itself don't tell us
    // anything about tmux state, so we must not nuke cache.
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    let throwOnRead = false;
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        if (args[0] === "list-panes") {
          if (throwOnRead) throw new Error("list-panes flake");
          return "%10|@1";
        }
        return "";
      },
      log: () => {},
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];

    // Prime cache.
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(2);

    // Read flakes. No write call, no state mutation.
    throwOnRead = true;
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(3);
    expect(calls[2]?.[0]).toBe("list-panes");

    // Read recovers. Cache was preserved, so this is a true no-op (only
    // list-panes runs, no set-option chain).
    throwOnRead = false;
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(4);
    expect(calls[3]?.[0]).toBe("list-panes");
  });

  test("non-throwing: shellTmux that throws is caught and logged", () => {
    __resetTmuxHeaderSyncStateForTests();
    let logged = false;
    const deps: SyncDeps = {
      shellTmux: () => {
        throw new Error("boom");
      },
      log: () => {
        logged = true;
      },
    };
    expect(() => {
      syncTmuxHeaderOptions({ sessions: [], theme: THEME, enabled: true }, deps);
    }).not.toThrow();
    expect(logged).toBe(true);
  });

  test("M1b: empty list-panes does NOT clear cached state — recovery is no-op when world matches", () => {
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    let listPanesReturn = "%10|@1";
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        if (args[0] === "list-panes") return listPanesReturn;
        return "";
      },
      log: () => {},
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];

    // Prime cache: list-panes + set-option chain.
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(2);

    // Transient empty list-panes (could be tmux flake or genuinely empty).
    listPanesReturn = "";
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    // Only list-panes ran — function returned early.
    expect(calls.length).toBe(3);
    expect(calls[2]?.[0]).toBe("list-panes");

    // World recovers — same panes as before. Cache must still match, so this
    // is a true no-op (only list-panes runs, no set-option chain).
    listPanesReturn = "%10|@1";
    syncTmuxHeaderOptions({ sessions, theme: THEME, enabled: true }, deps);
    expect(calls.length).toBe(4);
    expect(calls[3]?.[0]).toBe("list-panes");
  });
});
