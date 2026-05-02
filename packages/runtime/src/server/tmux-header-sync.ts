// Tmux header synchroniser.
//
// Single-writer translation from tcm state -> tmux user options
// that the status line in `integrations/tmux-plugin/scripts/header.tmux`
// reads to render per-window agent glyphs and theme-aware colours.
//
// Spec: docs/specs/tmux-header.md

import { existsSync } from "node:fs";
import { homedir, platform } from "node:os";
import { join } from "node:path";
import type { SessionData } from "./../shared";
import type { AgentEvent, AgentStatus, AgentLiveness } from "../contracts/agent";
import type { Theme } from "../themes";

// --- Glyph table ---

// claude-code's glyph is detect-and-fall-back: if Clawd.ttf is installed at
// the OS-standard user-fonts path, emit U+100CC0 (Plane 16 PUA-B, the Clawd
// mascot); otherwise fall back to U+2605 (★), available in any monospace
// stack. Run `just install-clawd` to install the vendored font from
// `fonts/Clawd.ttf`. The remaining glyphs are drawn from widely-supported
// Unicode blocks (U+25xx, U+26xx, basic Greek).

/** OS-standard user-fonts path for Clawd.ttf, by platform. */
function clawdFontPath(): string {
  if (platform() === "darwin") return join(homedir(), "Library", "Fonts", "Clawd.ttf");
  return join(homedir(), ".local", "share", "fonts", "Clawd.ttf");
}

/** Probe whether the Clawd mascot font is installed on this host. Cheap (one
 *  stat call) — called once at module load. Server restart picks up post-hoc
 *  installs; live re-detection is overkill for a glyph table. */
export function isClawdInstalled(): boolean {
  return existsSync(clawdFontPath());
}

export function buildAgentGlyphs(opts: { clawdInstalled: boolean }): Record<string, string> {
  return {
    "claude-code": opts.clawdInstalled ? "\u{100CC0}" : "\u2605",
    "pi": "\u03C0",
    "codex": "\u25B2",
    "amp": "\u2666",
    "generic": "\u{F167A}",
  };
}

export const AGENT_GLYPHS: Record<string, string> = buildAgentGlyphs({
  clawdInstalled: isClawdInstalled(),
});

export const AGENT_PRIORITY: readonly string[] = ["claude-code", "pi", "codex", "amp"];

// --- Statusline-only glyphs (server-global tmux user options) ---
//
// These are not per-window state — they're constants the statusline format
// references in fixed slots. Emitted once per palette-write cycle as
// `set-option -g @tcm-<name>-glyph` so `header.tmux` can resolve them with
// `#{@tcm-<name>-glyph}`. Re-exported from the runtime barrel so
// `apps/tui/src/vocab.ts` can re-export and remain the single reader-facing
// vocabulary surface, even though the panel itself never renders them.
export const STATUSLINE_LAST_WINDOW = "\u{F17B3}"; // nf-md-arrow_u_left_top — last-visited-window marker
export const STATUSLINE_SHELL = "\u{EA85}";        // nf-cod-terminal — no-agent-in-window marker

export function pickAgentForWindow(agents: string[]): string {
  for (const candidate of AGENT_PRIORITY) {
    if (agents.includes(candidate)) return candidate;
  }
  return agents[0] ?? "generic";
}

// --- Palette tokens propagated to per-server tmux options ---

const PALETTE_TOKENS = [
  "base", "text", "blue", "surface0", "surface2",
  "overlay0", "yellow", "red", "green",
] as const satisfies readonly (keyof Theme["palette"])[];

// --- Sync state ---

export interface WindowState {
  glyph: string;
  fg: string;
  agent: string;
}

export type SyncedState = Map<string, WindowState>;

export interface PaletteState {
  themeName: string | undefined;
  values: Map<string, string>;
}

export interface PlanInput {
  sessions: SessionData[];
  theme: Theme;
  themeName: string | undefined;
  enabled: boolean;
  paneToWindow: Map<string, string>;
  prevWindows: SyncedState;
  prevPalette: PaletteState | null;
}

export interface PlanOutput {
  commands: string[][];
  newWindows: SyncedState;
  newPalette: PaletteState;
}

// --- Pure planner ---

/** Compute the desired window state and palette from server state, diff against
 *  the previous values, and emit the minimal set of tmux invocations needed to
 *  reach the new state. */
export function planTmuxHeaderSync(input: PlanInput): PlanOutput {
  const newPalette = buildPaletteState(input.theme, input.themeName);

  if (!input.enabled) {
    // Gate off: produce no commands; carry forward state so a later enable
    // does not falsely claim "no diff".
    return {
      commands: [],
      newWindows: new Map(),
      newPalette: { themeName: undefined, values: new Map() },
    };
  }

  const newWindows = computeWindowStates(input);
  const commands: string[][] = [];

  // Live windows: every tmux window has at least one pane, so the value-set
  // of paneToWindow is the set of windows currently alive in the server. Used
  // below to skip cleanup writes for windows that have already been closed —
  // those would error with "no such window: @N", aborting the chained tmux
  // call and preventing lastWindows/lastPalette from advancing. One stuck
  // dead-window id used to wedge the sync forever (until process restart).
  const liveWindows = new Set(input.paneToWindow.values());

  // Palette + statusline glyphs: write whenever any palette value differs
  // from the previously-applied state, or the palette has never been written.
  // Diffing on themeName alone misses same-name palette rewrites (e.g. a user
  // edits a theme JSON in place, or the-themer regenerates a theme with the
  // same name but tweaked colours). The statusline glyphs
  // (`@tcm-last-window-glyph`, `@tcm-shell-glyph`) don't vary with theme but
  // lumping them in here costs nothing — these writes are idempotent.
  if (!input.prevPalette || !paletteValuesEqual(input.prevPalette, newPalette)) {
    for (const [token, value] of newPalette.values) {
      commands.push(["set-option", "-g", `@tcm-thm-${token}`, value]);
    }
    commands.push(["set-option", "-g", "@tcm-last-window-glyph", STATUSLINE_LAST_WINDOW]);
    commands.push(["set-option", "-g", "@tcm-shell-glyph", STATUSLINE_SHELL]);
  }

  // Per-window diffs.
  for (const [windowId, next] of newWindows) {
    const prev = input.prevWindows.get(windowId);
    if (prev && prev.glyph === next.glyph && prev.fg === next.fg && prev.agent === next.agent) continue;
    commands.push(["set-option", "-w", "-t", windowId, "@tcm-agent", next.glyph]);
    commands.push(["set-option", "-w", "-t", windowId, "@tcm-agent-fg", next.fg]);
    commands.push(["set-option", "-w", "-t", windowId, "@tcm-agent-type", next.agent]);
  }

  // Cleanup: windows that had a glyph but no longer do. Skip windows that
  // are no longer alive in tmux — `set-option -wu -t @N` errors with "no
  // such window" and aborts the whole chained command, which used to
  // permanently wedge the sync (the stale id stayed in prevWindows forever
  // because lastWindows is only advanced on a successful chain).
  for (const windowId of input.prevWindows.keys()) {
    if (newWindows.has(windowId)) continue;
    if (!liveWindows.has(windowId)) continue;
    commands.push(["set-option", "-wu", "-t", windowId, "@tcm-agent"]);
    commands.push(["set-option", "-wu", "-t", windowId, "@tcm-agent-fg"]);
    commands.push(["set-option", "-wu", "-t", windowId, "@tcm-agent-type"]);
  }

  return { commands, newWindows, newPalette };
}

function paletteValuesEqual(a: PaletteState, b: PaletteState): boolean {
  if (a.values.size !== b.values.size) return false;
  for (const [token, value] of a.values) {
    if (b.values.get(token) !== value) return false;
  }
  return true;
}

function buildPaletteState(theme: Theme, themeName: string | undefined): PaletteState {
  const values = new Map<string, string>();
  for (const token of PALETTE_TOKENS) values.set(token, toTmuxColour(theme.palette[token]));
  return { themeName, values };
}

/** Translate an tcm palette value to a tmux-renderable colour.
 *  The "transparent" theme stores the literal string "transparent" for base
 *  surfaces; tmux understands "default" but not "transparent". */
function toTmuxColour(value: string): string {
  return value === "transparent" ? "default" : value;
}

function computeWindowStates(input: PlanInput): SyncedState {
  // Group alive agents by tmux windowId.
  const windowAgents = new Map<string, AgentEvent[]>();
  for (const session of input.sessions) {
    for (const agent of session.agents) {
      if (agent.liveness !== "alive") continue;
      const paneId = agent.paneId;
      if (!paneId) continue;
      const windowId = input.paneToWindow.get(paneId);
      if (!windowId) continue;
      const list = windowAgents.get(windowId) ?? [];
      list.push(agent);
      windowAgents.set(windowId, list);
    }
  }

  const result: SyncedState = new Map();
  for (const [windowId, agents] of windowAgents) {
    const dominantName = pickAgentForWindow(agents.map((a) => a.agent));
    const dominant = agents.find((a) => a.agent === dominantName) ?? agents[0]!;
    const glyph = AGENT_GLYPHS[dominantName] ?? AGENT_GLYPHS["generic"]!;
    const fg = toTmuxColour(severityColour(dominant, input.theme));
    result.set(windowId, { glyph, fg, agent: dominantName });
  }
  return result;
}

// --- Severity colour resolution ---
//
// Mirrors the panel's status→colour map (apps/tui/src/index.tsx). When/if
// these diverge, extract a shared resolver into `runtime/themes.ts`. Today
// they're small enough that two copies + tests is cheaper than the
// cross-package coupling.

/** Five-state severity label, derived from agent status + liveness.
 *  Mirrors the panel's resolver in apps/tui/src/index.tsx. */
export type SeverityLabel = "working" | "waiting" | "ready" | "stopped" | "error";

export function severityLabel(status: AgentStatus | null, liveness: AgentLiveness | undefined): SeverityLabel {
  if (status === "running") return "working";
  if (status === "waiting") return "waiting";
  if (status === "error") return "error";
  // done / interrupted / idle / null — liveness disambiguates.
  if (liveness === "alive") return "ready";
  if (status === "done" || status === "interrupted") return "stopped";
  return "ready";
}

/** Resolve the tmux fg colour for a given agent's severity. */
export function severityColour(agent: AgentEvent, theme: Theme): string {
  const label = severityLabel(agent.status, agent.liveness);
  switch (label) {
    case "working": return theme.palette.blue;
    case "waiting": return theme.palette.yellow;
    case "ready":   return theme.palette.green;
    case "stopped": return theme.palette.surface2;
    case "error":   return theme.palette.red;
  }
}

// --- Shell adapter ---

export interface SyncDeps {
  /** Run a tmux command and return stdout. Caller-provided so tests can stub. */
  shellTmux: (args: string[]) => string;
  /** Structured logger; same shape as the server's `log()`. */
  log: (msg: string, data?: Record<string, unknown>) => void;
}

let lastWindows: SyncedState = new Map();
let lastPalette: PaletteState | null = null;

export function __resetTmuxHeaderSyncStateForTests(): void {
  lastWindows = new Map();
  lastPalette = null;
}

export interface SyncArgs {
  sessions: SessionData[];
  theme: Theme;
  themeName: string | undefined;
  enabled: boolean;
}

/** Live sync. Called from `broadcastStateImmediate`; idempotent and non-throwing. */
export function syncTmuxHeaderOptions(args: SyncArgs, deps: SyncDeps): void {
  try {
    if (!args.enabled) {
      lastWindows = new Map();
      lastPalette = null;
      return;
    }

    let paneToWindow: Map<string, string>;
    try {
      paneToWindow = readPaneToWindow(deps);
    } catch (err) {
      // Read-side failure (list-panes flake): preserve cache. The next
      // successful scan will recompute against the correct prior state.
      // Clearing here would (a) leak stale per-window options when the
      // recovery scan re-emitted writes against an empty cache, and
      // (b) clear lastWindows but not lastPalette, leaving the bar on
      // fallback colours after a tmux server restart wiped @tcm-thm-*.
      deps.log("sync read failed", { error: String(err) });
      return;
    }
    if (paneToWindow.size === 0) {
      // Empty list-panes is ambiguous (transient flake vs genuinely empty
      // server). Same reasoning as above — preserve cache.
      return;
    }

    const plan = planTmuxHeaderSync({
      sessions: args.sessions,
      theme: args.theme,
      themeName: args.themeName,
      enabled: args.enabled,
      paneToWindow,
      prevWindows: lastWindows,
      prevPalette: lastPalette,
    });

    if (plan.commands.length > 0) {
      try {
        runTmuxCommands(plan.commands, deps);
      } catch (err) {
        // Write-side failure: the chained tmux command aborted at an
        // unknown point, so we don't know which writes landed. Reset cache
        // so the next broadcast re-emits the full state (palette + every
        // alive window's agent options, no cleanup) and self-heals.
        // The cleanup-against-live-windows filter in the planner should
        // make this branch unreachable in practice, but it's the safe
        // fallback if a future regression — or a window closing in the
        // race window between list-panes and set-option — pokes a hole.
        deps.log("sync write failed", { error: String(err) });
        lastWindows = new Map();
        lastPalette = null;
        return;
      }
    }

    lastWindows = plan.newWindows;
    lastPalette = plan.newPalette;
  } catch (err) {
    deps.log("sync failed", { error: String(err) });
  }
}

function readPaneToWindow(deps: SyncDeps): Map<string, string> {
  const map = new Map<string, string>();
  const out = deps.shellTmux(["list-panes", "-a", "-F", "#{pane_id}|#{window_id}"]);
  if (!out) return map;
  for (const line of out.split("\n")) {
    if (!line) continue;
    const idx = line.indexOf("|");
    if (idx <= 0) continue;
    map.set(line.slice(0, idx), line.slice(idx + 1));
  }
  return map;
}

function runTmuxCommands(commands: string[][], deps: SyncDeps): void {
  // Chain into a single tmux invocation via `;` separators to amortise the
  // process-spawn cost. tmux requires `\;` so it doesn't get eaten by the
  // shell; spawning directly without a shell, we pass `;` as its own arg.
  const chained: string[] = [];
  for (let i = 0; i < commands.length; i++) {
    if (i > 0) chained.push(";");
    chained.push(...commands[i]!);
  }
  deps.shellTmux(chained);
}
