import { describe, test, expect } from "bun:test";
import {
  AGENT_GLYPHS,
  AGENT_PRIORITY,
  buildAgentGlyphs,
  isClawdInstalled,
  pickAgentForWindow,
  planTmuxHeaderSync,
  syncTmuxHeaderOptions,
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
    ports: [],
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

    const setAgent = out.commands.find((c) => c.includes("@os-agent") && c.includes("@1") && !c.includes("-fg") && !c.includes("-type"));
    expect(setAgent).toEqual(["set-option", "-w", "-t", "@1", "@os-agent", AGENT_GLYPHS["claude-code"]!]);
    const setFg = out.commands.find((c) => c.includes("@os-agent-fg") && c.includes("@1"));
    expect(setFg?.[5]).toBe(BLUE);
    const setType = out.commands.find((c) => c.includes("@os-agent-type") && c.includes("@1"));
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

  test("S5: theme change re-emits the @os-thm-* palette options", () => {
    const sessions: SessionData[] = [];
    const first = planTmuxHeaderSync(emptyInput({ sessions, theme: resolveTheme("catppuccin-mocha"), themeName: "catppuccin-mocha" }));
    const second = planTmuxHeaderSync(emptyInput({
      sessions,
      theme: resolveTheme("dracula"),
      themeName: "dracula",
      prevWindows: first.newWindows,
      prevPalette: first.newPalette,
    }));
    const paletteWrites = second.commands.filter((c) => c[2]?.startsWith("@os-thm-"));
    expect(paletteWrites.length).toBeGreaterThan(0);
    expect(second.newPalette.values.get("base")).toBe(BUILTIN_THEMES["dracula"]!.palette.base);
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
    expect(cleanups.length).toBe(3); // @os-agent, @os-agent-fg, @os-agent-type
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
    expect(calls[1]).toContain("@os-agent");
    expect(calls[1]).toContain("@os-agent-fg");
    expect(calls[1]).toContain("@os-agent-type");
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
