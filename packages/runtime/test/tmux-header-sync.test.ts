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
  type PaletteState,
  type SyncDeps,
} from "../src/server/tmux-header-sync";
import { resolveTheme, BUILTIN_THEMES } from "../src/themes";
import type { SessionData } from "../src/shared";
import type { AgentEvent } from "../src/contracts/agent";

const THEME = resolveTheme("catppuccin-mocha");
const THEME_NAME = "catppuccin-mocha";
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
    themeName: THEME_NAME,
    enabled: true,
    paneToWindow: new Map(),
    prevWindows: new Map(),
    prevPalette: null,
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
      prevPalette: first.newPalette,
    }));
    expect(second.commands).toEqual([]);
  });

  test("S5: theme change re-emits the @tcm-thm-* palette options", () => {
    const sessions: SessionData[] = [];
    const first = planTmuxHeaderSync(emptyInput({ sessions, theme: resolveTheme("catppuccin-mocha"), themeName: "catppuccin-mocha" }));
    const second = planTmuxHeaderSync(emptyInput({
      sessions,
      theme: resolveTheme("dracula"),
      themeName: "dracula",
      prevWindows: first.newWindows,
      prevPalette: first.newPalette,
    }));
    const paletteWrites = second.commands.filter((c) => c[2]?.startsWith("@tcm-thm-"));
    expect(paletteWrites.length).toBeGreaterThan(0);
    expect(second.newPalette.values.get("base")).toBe(BUILTIN_THEMES["dracula"]!.palette.base);
  });

  test("S5b: first sync emits @tcm-shell-glyph and @tcm-last-window-glyph as server globals", () => {
    const sessions: SessionData[] = [];
    const out = planTmuxHeaderSync(emptyInput({ sessions }));
    const lastWin = out.commands.find((c) => c[2] === "@tcm-last-window-glyph");
    const shell = out.commands.find((c) => c[2] === "@tcm-shell-glyph");
    expect(lastWin).toEqual(["set-option", "-g", "@tcm-last-window-glyph", STATUSLINE_LAST_WINDOW]);
    expect(shell).toEqual(["set-option", "-g", "@tcm-shell-glyph", STATUSLINE_SHELL]);
  });

  test("S5c: identical second call does NOT re-emit statusline glyph globals", () => {
    const sessions: SessionData[] = [];
    const first = planTmuxHeaderSync(emptyInput({ sessions }));
    const second = planTmuxHeaderSync(emptyInput({
      sessions,
      prevWindows: first.newWindows,
      prevPalette: first.newPalette,
    }));
    const reEmits = second.commands.filter((c) => c[2] === "@tcm-last-window-glyph" || c[2] === "@tcm-shell-glyph");
    expect(reEmits).toEqual([]);
  });

  test("S5d: theme change re-emits statusline glyph globals (lumped with palette)", () => {
    const sessions: SessionData[] = [];
    const first = planTmuxHeaderSync(emptyInput({ sessions, theme: resolveTheme("catppuccin-mocha"), themeName: "catppuccin-mocha" }));
    const second = planTmuxHeaderSync(emptyInput({
      sessions,
      theme: resolveTheme("dracula"),
      themeName: "dracula",
      prevWindows: first.newWindows,
      prevPalette: first.newPalette,
    }));
    const lastWin = second.commands.find((c) => c[2] === "@tcm-last-window-glyph");
    const shell = second.commands.find((c) => c[2] === "@tcm-shell-glyph");
    expect(lastWin).toEqual(["set-option", "-g", "@tcm-last-window-glyph", STATUSLINE_LAST_WINDOW]);
    expect(shell).toEqual(["set-option", "-g", "@tcm-shell-glyph", STATUSLINE_SHELL]);
  });

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

  test("E3: empty sessions clears prev windows", () => {
    const prevWindows: SyncedState = new Map([["@1", { glyph: "X", fg: "#fff", agent: "claude-code" }]]);
    const prevPalette: PaletteState = { themeName: THEME_NAME, values: new Map([["base", "#000"]]) };
    const out = planTmuxHeaderSync(emptyInput({ sessions: [], paneToWindow: new Map(), prevWindows, prevPalette }));
    const cleanups = out.commands.filter((c) => c[0] === "set-option" && c[1] === "-wu" && c[3] === "@1");
    expect(cleanups.length).toBe(3); // @tcm-agent, @tcm-agent-fg, @tcm-agent-type
    expect(out.newWindows.size).toBe(0);
  });

  test("E1: agent pane destroyed before sync — windowId stays in prev, cleanup fires", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%dead" })])];
    const prevWindows: SyncedState = new Map([
      ["@deadWin", { glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" }],
    ]);
    // paneToWindow has no entry for %dead — pane is gone from tmux.
    const out = planTmuxHeaderSync(emptyInput({
      sessions,
      paneToWindow: new Map(),
      prevWindows,
      prevPalette: { themeName: THEME_NAME, values: new Map() },
    }));
    const cleanups = out.commands.filter((c) => c[1] === "-wu" && c[3] === "@deadWin");
    expect(cleanups.length).toBe(3);
    expect(out.newWindows.size).toBe(0);
  });

  test("E5: pane moves to a different window — old window cleared, new window set", () => {
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    const prevWindows: SyncedState = new Map([
      ["@oldWin", { glyph: AGENT_GLYPHS["claude-code"]!, fg: BLUE, agent: "claude-code" }],
    ]);
    const prevPalette: PaletteState = { themeName: THEME_NAME, values: new Map() };
    const paneToWindow = new Map([["%10", "@newWin"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow, prevWindows, prevPalette }));
    const cleared = out.commands.filter((c) => c[1] === "-wu" && c[3] === "@oldWin");
    expect(cleared.length).toBe(3);
    const setOnNew = out.commands.filter((c) => c[1] === "-w" && c[3] === "@newWin");
    expect(setOnNew.length).toBe(3);
  });

  test("agents with liveness !== alive are ignored", () => {
    const sessions = [makeSession("s1", [
      makeAgent({ agent: "claude-code", session: "s1", paneId: "%10", liveness: "exited" }),
    ])];
    const paneToWindow = new Map([["%10", "@1"]]);
    const out = planTmuxHeaderSync(emptyInput({ sessions, paneToWindow }));
    expect(out.newWindows.size).toBe(0);
  });

  test("transparent theme palette translates to tmux 'default' colour", () => {
    const transparent = resolveTheme("transparent");
    const out = planTmuxHeaderSync(emptyInput({
      sessions: [],
      theme: transparent,
      themeName: "transparent",
    }));
    expect(out.newPalette.values.get("base")).toBe("default");
    // Non-transparent values pass through unchanged.
    expect(out.newPalette.values.get("blue")).toBe(transparent.palette.blue);
  });

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
      prevPalette: first.newPalette,
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
    syncTmuxHeaderOptions({ sessions: [], theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
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
    syncTmuxHeaderOptions({ sessions: [], theme: THEME, themeName: THEME_NAME, enabled: false }, deps);
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
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
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
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    const firstCallCount = calls.length;
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    // Second call: only list-panes ran; no new set-option chain.
    expect(calls.length).toBe(firstCallCount + 1);
    expect(calls[calls.length - 1]?.[0]).toBe("list-panes");
  });

  test("X2: shellTmux throws after list-panes — error caught, cache not corrupted", () => {
    __resetTmuxHeaderSyncStateForTests();
    const calls: string[][] = [];
    let listPanesReturn = "%10|@1";
    const deps: SyncDeps = {
      shellTmux: (args) => {
        calls.push(args);
        if (args[0] === "list-panes") return listPanesReturn;
        // The chained set-option call throws.
        throw new Error("write failed");
      },
      log: () => {},
    };
    const sessions = [makeSession("s1", [makeAgent({ agent: "claude-code", session: "s1", paneId: "%10" })])];
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    // Both list-panes and the failing set-option were attempted.
    expect(calls.length).toBe(2);

    // Next call: cache was NOT updated, so the sync retries — list-panes plus
    // the same set-option chain.
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    expect(calls.length).toBe(4);
    expect(calls[3]?.[0]).toBe("set-option");
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
      syncTmuxHeaderOptions({ sessions: [], theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
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
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    expect(calls.length).toBe(2);

    // Transient empty list-panes (could be tmux flake or genuinely empty).
    listPanesReturn = "";
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    // Only list-panes ran — function returned early.
    expect(calls.length).toBe(3);
    expect(calls[2]?.[0]).toBe("list-panes");

    // World recovers — same panes as before. Cache must still match, so this
    // is a true no-op (only list-panes runs, no set-option chain).
    listPanesReturn = "%10|@1";
    syncTmuxHeaderOptions({ sessions, theme: THEME, themeName: THEME_NAME, enabled: true }, deps);
    expect(calls.length).toBe(4);
    expect(calls[3]?.[0]).toBe("list-panes");
  });
});
