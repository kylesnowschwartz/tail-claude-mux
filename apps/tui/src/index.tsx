import { render } from "@opentui/solid";
import { createSignal, createEffect, onCleanup, onMount, batch, For, Index, Show, createMemo, type Accessor } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { useKeyboard, useRenderer, useTerminalDimensions } from "@opentui/solid";
import { TextAttributes, type InputRenderable, type KeyEvent } from "@opentui/core";

import { ensureServer } from "@tcm/runtime";
import {
  type ServerMessage,
  type SessionData,
  type ClientCommand,
  type Theme,
  type ThemePalette,
  type MetadataTone,
  SERVER_PORT,
  SERVER_HOST,
  BUILTIN_THEMES,
  resolveTheme,
  sanitizeForDisplay,
  glowPhase,
  lerpHex,
} from "@tcm/runtime";
import { TmuxClient } from "@tcm/mux-tmux";
import {
  SEV_WORKING_SPINNER,
  SEV_WAITING,
  SEV_READY,
  SEV_STOPPED,
  SEV_ERROR,
  BRAND_CLAWD,
  BRANCH_GLYPH,
  DIR_MISMATCH_GLYPH,
  WRAP_UP,
  WRAP_DOWN,
  AGENT_GLYPHS,
} from "./vocab";
import { tier } from "./tiers";
import {
  type ActivityLog,
  type BucketIcon,
  SPARKLINE_BUCKET_MS,
  BLANK_SLOT,
  seismographGeometry,
  windowMs,
  bucketSparklineLogs,
  sparklineRows,
  expandSparklineRows,
  bucketIconLogs,
  iconSlot,
  formatRelTime,
} from "./activity";
import { getScenario, listScenarios } from "./mocks/scenarios";
import { createRefocusGate } from "./refocus-gate";

// Detect which mux we're running inside
type MuxContext =
  | { type: "tmux"; sdk: TmuxClient; paneId: string }
  | { type: "none" };

function detectMuxContext(): MuxContext {
  if (process.env.TMUX_PANE && process.env.TMUX) {
    return { type: "tmux", sdk: new TmuxClient(), paneId: process.env.TMUX_PANE };
  }
  return { type: "none" };
}

const muxCtx = detectMuxContext();

const SPINNERS = SEV_WORKING_SPINNER;
const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;
const THEME_NAMES = Object.keys(BUILTIN_THEMES);

function toneColor(tone: MetadataTone | undefined, palette: ReturnType<() => Theme["palette"]>): string {
  switch (tone) {
    case "success": return palette.green;
    case "error": return palette.red;
    case "warn": return palette.yellow;
    case "info": return palette.blue;
    default: return palette.overlay0;
  }
}

function formatDir(dir: string | undefined): { project: string; parent: string } {
  if (!dir) return { project: "", parent: "" };
  const home = process.env.HOME ?? "";
  const display = home && dir.startsWith(home) ? "~" + dir.slice(home.length) : dir;
  const segments = display.split("/").filter(Boolean);
  if (segments.length <= 1) return { project: display, parent: "" };
  const project = segments[segments.length - 1];
  const parent = segments[segments.length - 2];
  return { project, parent };
}

function sanitizeThreadName(raw: string): string {
  // Belt-and-braces: watchers already sanitize at their boundary, but the
  // server→TUI WebSocket is another trust boundary so we re-run the cheap
  // pass here. Idempotent.
  const cleaned = sanitizeForDisplay(raw);
  const firstLine = cleaned.split("\n")[0];
  return firstLine.replace(/^(?:---+|#+|\*{1,2}|>\s*)+\s*/, "").trim();
}

/** Short display form for a threadId.
 *  Uses the last 4 chars of the ID because multiple agents (pi, OpenCode)
 *  produce IDs with deterministic *prefixes* (UUIDv7 timestamp, `ses_`
 *  sigil) while their random bits live at the tail. For Claude Code's
 *  UUIDv4 the distribution is uniform, so the tail is just as good as the
 *  head. */
function shortThreadId(id: string): string {
  return id.length <= 4 ? id : id.slice(-4);
}

/** Build an FZF_DEFAULT_OPTS --color string from an tcm palette.
 *  fzf doesn't understand the literal string "transparent" — it wants -1 to
 *  mean "use terminal default", which is how we render transparency. */
function paletteToFzfColors(p: ThemePalette): string {
  const c = (v: string) => (v === "transparent" ? "-1" : v);
  return [
    `--color=fg:${c(p.text)},bg:${c(p.base)},hl:${c(p.blue)}`,
    `--color=fg+:${c(p.surface2)},bg+:${c(p.surface0)},hl+:${c(p.blue)}`,
    `--color=info:${c(p.blue)},prompt:${c(p.blue)},pointer:${c(p.blue)}`,
    `--color=marker:${c(p.green)},spinner:${c(p.blue)},header:${c(p.overlay0)}`,
    `--color=border:${c(p.surface2)},gutter:${c(p.base)}`,
    `--color=query:${c(p.text)},disabled:${c(p.overlay0)}`,
  ].join(" ");
}

/** Refocus the main (non-sidebar) pane after TUI capability detection finishes.
 *  This must happen from the TUI process — doing it from start.sh races with
 *  capability query responses and leaks escape sequences to the main pane. */
function refocusMainPane() {
  if (muxCtx.type === "tmux") {
    try {
      // Use the TUI's own pane ID to find its current window (handles stash restore
      // where the pane may have moved to a different window than the original).
      const windowId = process.env.REFOCUS_WINDOW
        || Bun.spawnSync(
            ["tmux", "display-message", "-t", muxCtx.paneId, "-p", "#{window_id}"],
            { stdout: "pipe", stderr: "pipe" },
          ).stdout.toString().trim();
      if (!windowId) return;
      const r = Bun.spawnSync(
        ["tmux", "list-panes", "-t", windowId, "-F", "#{pane_id} #{@tcm-sidebar} #{@tcm-companion} #{pane_title}"],
        { stdout: "pipe", stderr: "pipe" },
      );
      const lines = r.stdout.toString().trim().split("\n");
      // A "main" pane is anything that's NOT tcm-managed (sidebar or
      // companion) — neither marker nor legacy title. Marker == "1"
      // primary, pane_title fallback.
      const main = lines.find((l) => {
        const parts = l.split(" ");
        const title = parts.slice(3).join(" ");
        return parts[1] !== "1" && parts[2] !== "1"
          && title !== "tcm-sidebar" && title !== "tcm-companion";
      });
      if (main) {
        const paneId = main.split(" ")[0];
        Bun.spawnSync(["tmux", "select-pane", "-t", paneId], { stdout: "pipe", stderr: "pipe" });
      }
    } catch {}
  }
}

function getClientTty(): string {
  if (muxCtx.type === "tmux") {
    const { sdk, paneId } = muxCtx;
    const sessName = sdk.display("#{session_name}", { target: paneId });
    if (sessName) {
      const clients = sdk.listClients();
      const client = clients.find((c) => c.sessionName === sessName);
      if (client) return client.tty;
    }
    return sdk.getClientTty();
  }
  return "";
}

function parseMockFlag(): string | null {
  // Supports `--mock` (defaults to "quiet"), `--mock=<name>`, and `--mock <name>`
  // (space-separated). The space-separated form was previously dropped on the
  // floor and would silently fall through to the default scenario.
  const args = process.argv.slice(2);
  for (let i = 0; i < args.length; i++) {
    const a = args[i]!;
    if (a === "--mock") {
      const next = args[i + 1];
      if (next && !next.startsWith("-")) return next;
      return "quiet";
    }
    if (a.startsWith("--mock=")) return a.slice("--mock=".length);
  }
  return null;
}

function getLocalSessionName(): string | null {
  if (muxCtx.type === "tmux") {
    const sessionName = muxCtx.sdk.display("#{session_name}", { target: muxCtx.paneId });
    return sessionName || null;
  }

  return null;
}

/**
 * Rolodex wrap rule — the split horizontal divider with a centred chevron
 * that marks where the rolodex visually wraps around the focused card.
 *
 * Renders as: `─────  ·  ─────` where the centre glyph is
 * \u{F0143} (chevron-up) above the focused card, \u{F0140} (chevron-down)
 * below it.
 *
 * The two flex-grown rule segments overflow horizontally; their containing
 * box clips them to the panel width.
 */
function WrapRule(props: { direction: "up" | "down"; palette: ThemePalette }) {
  const fg = props.palette.surface1;
  const chevron = props.direction === "up" ? WRAP_UP : WRAP_DOWN;
  return (
    <box height={1} flexDirection="row" paddingLeft={1} paddingRight={1}>
      <box flexGrow={1} flexShrink={1} overflow="hidden">
        <text style={{ fg }}>{"─".repeat(200)}</text>
      </box>
      <text style={{ fg }} flexShrink={0}>{" "}{chevron}{" "}</text>
      <box flexGrow={1} flexShrink={1} overflow="hidden">
        <text style={{ fg }}>{"─".repeat(200)}</text>
      </box>
    </box>
  );
}

// ────────────────────────────────────────────────────────────────────────────
// Activity zone — pure histogram/icon logic lives in ./activity.ts. The
// original text-stream layout spec (docs/simmer/activity-zone/result.md) is
// superseded by the seismograph below; its sparkline contract carries over.
// ────────────────────────────────────────────────────────────────────────────

/**
 * Activity zone — a two-row, full-width "seismograph" beneath the rolodex.
 *
 *            ▂▂▂                ← histogram overflow row (bursts stack up)
 *   ▁▁▁▄▄▄▁▁▁███▅▅▅▁▁▁▁▁▁▁▁▁   ← histogram: activity density, one bucket = 8 s,
 *       R       E   W           ← each bucket BUCKET_COLS wide; verb glyphs
 *                                  centred in the slot of their bucket
 *
 * The old text stream (eyebrows, chips, descriptions) is retired. Both rows
 * share one time axis (activity.ts bucketIndex) — histogram slot N and glyph
 * slot N cover the same 8 s bucket — so the glyphs scroll left with the
 * histogram as the window slides. Newest bucket is the rightmost slot. The
 * freshest occupied glyph renders bright, older ones dim; system-tag bells
 * keep their tone colour and errors render red (severity bypasses the
 * unfocus tier slide).
 *
 * Empty states: no logs → flat baseline + blank glyph row; window slid past
 * the newest log → flat baseline + a dim right-aligned `·Nm` age marker (the
 * one text survivor — it disambiguates "quiet now" from "dead for an hour").
 */
function ActivityZone(props: {
  focusedSession: SessionData | null;
  palette: ThemePalette;
  paneFocused: boolean;
}) {
  // Reactive terminal size, read here (not via a prop) so a stale-width
  // caller mistake is unrepresentable — see the note on App's termDims.
  const dims = useTerminalDimensions();

  // 1 Hz tick for the `·Ns` stale marker; the heavy bucketing pipeline keys
  // off bucketEpoch below and only recomputes when the 8 s window slides.
  const [nowMs, setNowMs] = createSignal(Date.now());
  const tick = setInterval(() => setNowMs(Date.now()), 1000);
  onCleanup(() => clearInterval(tick));

  // Quantised clock: changes once per bucket, so sparkline/icons memos skip
  // the 7-of-8 ticks whose output would be byte-identical, and every cell
  // slides left in lockstep at bucket boundaries (logs newer than the
  // quantised boundary clamp into the freshest bucket via bucketIndex).
  const bucketNow = createMemo(() => Math.floor(nowMs() / SPARKLINE_BUCKET_MS) * SPARKLINE_BUCKET_MS);

  const logs = createMemo<readonly ActivityLog[]>(() => props.focusedSession?.metadata?.logs ?? []);
  const newestTs = createMemo(() => {
    let max = -Infinity;
    for (const log of logs()) max = Math.max(max, log.ts);
    return max; // -Infinity when there are no logs
  });

  // Full-width geometry: the box pads 1 cell each side; the remaining columns
  // split into BUCKET_COLS-wide slots (activity.ts owns the arithmetic).
  const PAD = 1;
  const contentWidth = createMemo(() => Math.max(1, dims().width - PAD * 2));
  const geometry = createMemo(() => seismographGeometry(contentWidth()));

  const sparkline = createMemo(() =>
    expandSparklineRows(
      sparklineRows(bucketSparklineLogs(logs(), bucketNow(), geometry().buckets)),
      contentWidth(),
    ),
  );
  const icons = createMemo(() => bucketIconLogs(logs(), bucketNow(), geometry().buckets));

  const freshestIdx = createMemo(() => {
    const arr = icons();
    for (let i = arr.length - 1; i >= 0; i--) if (arr[i]) return i;
    return -1;
  });

  // Window-empty detection — newest log is older than the visible window.
  const staleSuffix = createMemo(() => {
    const age = nowMs() - newestTs();
    if (!Number.isFinite(age) || age < windowMs(geometry().buckets)) return null;
    return "·" + formatRelTime(age);
  });

  const sparklineStyle = () => tier("secondary", props.palette, props.paneFocused);
  const suffixStyle    = () => tier("dim",       props.palette, props.paneFocused);
  // Glyph freshness mirrors the retired description tiers: bright on the
  // newest occupied bucket, dim on older ones. Severity/tone bypass the slide.
  const freshFg = () => props.paneFocused ? props.palette.text     : props.palette.subtext0;
  const oldFg   = () => props.paneFocused ? props.palette.overlay1 : props.palette.surface2;
  const iconStyle = (cell: BucketIcon, idx: number) => {
    if (cell.kind === "error")  return { fg: props.palette.red };
    if (cell.kind === "system") return { fg: toneColor(cell.tone, props.palette) };
    return { fg: idx === freshestIdx() ? freshFg() : oldFg() };
  };

  return (
    <box flexDirection="column" flexShrink={0} paddingLeft={PAD} paddingRight={PAD}>
      <Index each={sparkline()}>
        {(row) => (
          <text truncate>
            <span style={sparklineStyle()}>{row()}</span>
          </text>
        )}
      </Index>
      <Show when={!staleSuffix()} fallback={
        <box height={1} flexDirection="row" justifyContent="flex-end">
          <text style={suffixStyle()}>{staleSuffix()}</text>
        </box>
      }>
        <text truncate>
          <span>{" ".repeat(geometry().leftoverCols)}</span>
          <Index each={icons()}>
            {(cell, idx) => (
              <Show when={cell()} fallback={<span>{BLANK_SLOT}</span>}>
                <span style={iconStyle(cell()!, idx)}>{iconSlot(cell()!.glyph)}</span>
              </Show>
            )}
          </Index>
        </text>
      </Show>
    </box>
  );
}


function App() {
  const renderer = useRenderer();
  // Reactive terminal size — `renderer.terminalWidth` is a plain property and
  // goes stale after SIGWINCH; every layout computation must read this signal
  // instead so pane resizes re-flow the sidebar (QA: seismograph wrap bug).
  const termDims = useTerminalDimensions();

  // --- Theme state (driven by server) ---
  const [theme, setTheme] = createSignal<Theme>(resolveTheme(undefined));
  const P = () => theme().palette;
  const S = () => theme().status;

  const [sessions, setSessions] = createStore<SessionData[]>([]);
  const [focusedSession, setFocusedSession] = createSignal<string | null>(null);
  const [currentSession, setCurrentSession] = createSignal<string | null>(null);
  const [mySession, setMySession] = createSignal<string | null>(null);
  const [connected, setConnected] = createSignal(false);
  const [spinIdx, setSpinIdx] = createSignal(0);

  // --- Pane focus: does this terminal pane have focus? ---
  const [paneFocused, setPaneFocused] = createSignal(false);

  // --- Pane focus: which paneId is currently focused anywhere in the user's
  //     tmux session? Used to mark the agent row whose pane the user is
  //     currently looking at. Separate from paneFocused, which is scoped to
  //     "is THIS TUI's host pane the focused one?".
  const [focusedPaneId, setFocusedPaneId] = createSignal<string | null>(null);

  // --- Panel focus: sessions list vs agent detail ---
  type PanelFocus = "sessions" | "agents";
  const [panelFocus, setPanelFocus] = createSignal<PanelFocus>("sessions");
  const [focusedAgentIdx, setFocusedAgentIdx] = createSignal(0);

  // --- Modal state ---
  const [modal, setModal] = createSignal<"none" | "theme-picker" | "confirm-kill" | "help">("none");
  const [killTarget, setKillTarget] = createSignal<string | null>(null);
  let themeBeforePreview: Theme | null = null;

  // --- Flash message (brief feedback after actions like refresh) ---
  const [flashMessage, setFlashMessage] = createSignal<string | null>(null);
  let flashTimer: ReturnType<typeof setTimeout> | null = null;
  function flash(msg: string, ms = 1200) {
    if (flashTimer) clearTimeout(flashTimer);
    setFlashMessage(msg);
    flashTimer = setTimeout(() => setFlashMessage(null), ms);
  }

  const [clientTty, setClientTty] = createSignal(getClientTty());
  let ws: WebSocket | null = null;
  let startupFocusSynced = false;
  const startupSessionName = getLocalSessionName();

  const focusedData = createMemo(() =>
    sessions.find((s) => s.name === focusedSession()) ?? null,
  );

  const focusedIdx = createMemo(() => {
    const name = focusedSession();
    if (!name) return -1;
    return sessions.findIndex(s => s.name === name);
  });

  // Rolodex: a *linear tape* of sessions in their natural order. The focused
  // card is pinned at the vertical centre of the zone; the viewport slides
  // over the tape as the focus index changes. Sessions appear in stable,
  // predictable positions relative to each other — the visible layout
  // never rotates.
  //
  // `j`/`k` navigation wraps modularly (see moveLocalFocus) so a single
  // press at either end snaps to the opposite end. That wrap is a
  // *navigation* behaviour; the tape itself does not wrap visually — at the
  // boundaries the `before` / `after` halves shrink, leaving empty space
  // above or below the focused card. The chevron separators above and below
  // the focused card stay always-visible regardless.
  // (The earlier wheel/rotation model disoriented users in live QA, hence
  // this slide-up/down approach.)
  const rolodex = createMemo(() => {
    const idx = focusedIdx();
    if (idx < 0) return { before: [] as SessionData[], after: [] as SessionData[] };
    return {
      before: sessions.slice(0, idx),
      after: sessions.slice(idx + 1),
    };
  });

  const sessionsBefore = createMemo(() => rolodex().before);
  const sessionsAfter = createMemo(() => rolodex().after);

  // Compute the tallest card height across all sessions so the
  // focused-card frame never resizes as you cycle.
  // Accounts for text wrapping in narrow sidebars.
  const maxCardHeight = createMemo(() => {
    // Available width for wrapped text (sidebar minus border, padding, indent)
    const textWidth = Math.max(8, termDims().width - 10);
    const wrapLines = (text: string) => Math.max(1, Math.ceil(text.length / textWidth));

    let max = 0;
    for (const session of sessions) {
      let h = 1; // row 1: name
      if (session.branch) h++; // row 2: branch

      // expanded content
      const { project, parent } = formatDir(session.dir);
      if (project && project !== session.name) {
        h++;
        if (parent) h++;
      }

      const agents = session.agents ?? [];
      for (const agent of agents) {
        // Mirror the AgentListItem render so subagent + threadId wrapping is
        // accounted for. Without this, rows that wrap to 2 lines push the
        // card's effective click area outside its frame and later rows
        // become un-clickable even though they appear visible.
        let text = "00 X"; // 2-cell win-num + space + 1-cell icon = 4 cells
        if (agent.subagent) text += "  " + agent.subagent;
        if (agent.threadId) text += "  " + shortThreadId(agent.threadId);
        text += "  X"; // status glyph slot on the right
        h += wrapLines(text);
      }
      // no gap between agents — card border provides visual grouping

      // Status / progress / logs render in the ActivityZone now, not in the
      // focused card. No height contribution from metadata.

      max = Math.max(max, h);
    }
    return max;
  });

  function send(cmd: ClientCommand) {
    if (connected() && ws) ws.send(JSON.stringify(cmd));
  }

  // Suppress pane-focus-out events briefly after session switch to prevent
  // the focus highlight from blinking during tmux's focus handoff.
  let focusSuppressUntil = 0;

  function switchToSession(name: string) {
    // Optimistic local update — makes rapid Tab repeat instant by removing
    // the server/hook round-trip from the next-Tab decision.
    // The server's focus/state broadcast will reconcile if needed.
    setCurrentSession(name);
    setFocusedSession(name);
    setPanelFocus("sessions");
    setFocusedAgentIdx(0);
    // Hold paneFocused true during session switch — tmux's focus handoff
    // briefly unfocuses the sidebar, causing a visible blink.
    setPaneFocused(true);
    focusSuppressUntil = Date.now() + 500;
    send({ type: "switch-session", name });
  }

  function reIdentify() {
    const sessionName = getLocalSessionName();
    if (!sessionName) return;

    if (muxCtx.type === "tmux") {
      send({ type: "identify-pane", paneId: muxCtx.paneId, sessionName });
    }
  }

  function moveLocalFocus(delta: -1 | 1) {
    const list = sessions;
    if (list.length === 0) return;

    const current = focusedSession();
    const currentIdx = Math.max(0, list.findIndex((s) => s.name === current));
    const nextIdx = (currentIdx + delta + list.length) % list.length;
    const next = list[nextIdx]?.name ?? null;

    if (!next || next === current) return;

    setFocusedSession(next);
    send({ type: "focus-session", name: next });
  }

  function moveAgentFocus(delta: -1 | 1) {
    const data = focusedData();
    const agents = data?.agents ?? [];
    if (agents.length === 0) return;
    const idx = focusedAgentIdx();
    const next = Math.max(0, Math.min(agents.length - 1, idx + delta));
    setFocusedAgentIdx(next);
  }

  function activateFocusedAgent() {
    const data = focusedData();
    const agents = data?.agents ?? [];
    const agent = agents[focusedAgentIdx()];
    if (!agent || !data) return;
    send({
      type: "focus-agent-pane",
      session: data.name,
      agent: agent.agent,
      threadId: agent.threadId,
      threadName: agent.threadName,
      paneId: agent.paneId,
    });
  }

  function dismissFocusedAgent() {
    const data = focusedData();
    const agents = data?.agents ?? [];
    const agent = agents[focusedAgentIdx()];
    if (!agent || !data) return;
    send({
      type: "dismiss-agent",
      session: data.name,
      agent: agent.agent,
      threadId: agent.threadId,
      paneId: agent.paneId,
      pid: agent.pid,
    });
    // Adjust index if we dismissed the last item
    if (focusedAgentIdx() >= agents.length - 1 && agents.length > 1) {
      setFocusedAgentIdx(agents.length - 2);
    }
    // If no agents left, go back to sessions
    if (agents.length <= 1) setPanelFocus("sessions");
  }

  function killFocusedAgentPane() {
    const data = focusedData();
    const agents = data?.agents ?? [];
    const agent = agents[focusedAgentIdx()];
    if (!agent || !data) return;
    send({
      type: "kill-agent-pane",
      session: data.name,
      agent: agent.agent,
      threadId: agent.threadId,
      threadName: agent.threadName,
      paneId: agent.paneId,
    });
  }

  function applyTheme(themeName: string) {
    send({ type: "set-theme", theme: themeName });
  }

  function previewTheme(themeName: string) {
    setTheme(resolveTheme(themeName));
  }

  function createNewSession() {
    if (muxCtx.type !== "tmux") {
      send({ type: "new-session" });
      return;
    }
    const scriptPath = new URL("../scripts/sessionizer.sh", import.meta.url).pathname;
    muxCtx.sdk.displayPopup({
      command: `bash "${scriptPath}"`,
      title: " new session ",
      width: "60%",
      height: "60%",
      closeOnExit: true,
      env: { TCM_FZF_COLORS: paletteToFzfColors(P()) },
    });
  }

  onMount(() => {
    // --- Mock mode: seed the store from a canonical scenario and skip the WS path ---
    const mockName = parseMockFlag();
    if (mockName) {
      const scenario = getScenario(mockName);
      if (scenario) {
        batch(() => {
          setSessions(reconcile(scenario.sessions, { key: "name" }));
          setFocusedSession(scenario.focusedSession);
          setCurrentSession(scenario.currentSession);
          setMySession(scenario.currentSession);
          setPaneFocused(scenario.paneFocused);
          setConnected(true);
        });
        // Tick the spinner so working agents animate even without a server.
        const spinTimer = setInterval(() => setSpinIdx((i) => (i + 1) % SPINNERS.length), 120);
        onCleanup(() => clearInterval(spinTimer));
      }
      return;
    }

    // Refocus the main pane once terminal capability detection settles.
    // opentui fires "capabilities" PER response sequence, not once when all
    // probes complete; refocusing on the first event leaks later responses
    // (kitty graphics OK, DA1, etc.) into the main pane as garbage typed at
    // the shell prompt. The gate waits for a quiescent window after the
    // last event — see ./refocus-gate.ts for the design.
    const refocusGate = createRefocusGate(refocusMainPane, { quietMs: 250, fallbackMs: 2000 });
    const onCapability = () => refocusGate.onCapability();
    renderer.on("capabilities", onCapability);

    onCleanup(() => {
      refocusGate.cleanup();
      renderer.removeListener("capabilities", onCapability);
    });

    let intentionalQuit = false;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let resizeHandler: (() => void) | null = null;

    function connectWebSocket() {
      const socket = new WebSocket(`ws://${SERVER_HOST}:${SERVER_PORT}`);
      ws = socket;

      socket.onopen = () => {
        setConnected(true);
        const tty = clientTty();
        if (tty) send({ type: "identify", clientTty: tty });
        reIdentify();

        // Report sidebar width on SIGWINCH (terminal resize / pane drag)
        // Only the TUI in the current session reports — other TUIs' resizes
        // are always enforcement echoes, never user drags.
        if (resizeHandler) renderer.removeListener("resize", resizeHandler);
        let lastReportedWidth = renderer.terminalWidth;
        resizeHandler = () => {
          const width = renderer.terminalWidth;
          if (width !== lastReportedWidth) {
            lastReportedWidth = width;
            const my = mySession();
            const current = currentSession();
            if (my && current && my !== current) return;
            send({ type: "report-width", width });
          }
        };
        renderer.on("resize", resizeHandler);
      };

      socket.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data as string) as ServerMessage;

          // Intentional quit — server told us to exit
          if ((msg as any).type === "quit") {
            intentionalQuit = true;
            if (ws) ws.close();
            renderer.destroy();
            return;
          }

          let startupFocusToPublish: string | null = null;
          batch(() => {
            if (msg.type === "state") {
              // Only claim focus if this TUI is in the user's current session.
              // Without this guard, every TUI sends focus-session on reconnect
              // and the last one to connect wins — producing a random highlight.
              const isCurrentSession = msg.currentSession === startupSessionName;
              const startupFocus = !startupFocusSynced
                && startupSessionName
                && isCurrentSession
                && msg.sessions.some((session) => session.name === startupSessionName)
                ? startupSessionName
                : msg.focusedSession;

              if (startupFocus === startupSessionName) {
                startupFocusSynced = true;
                if (msg.focusedSession !== startupSessionName) {
                  startupFocusToPublish = startupSessionName;
                }
              }

              setSessions(reconcile(msg.sessions, { key: "name" }));
              setFocusedSession(startupFocus);
              setCurrentSession(msg.currentSession);
              setTheme(resolveTheme(msg.theme));
            } else if (msg.type === "focus") {
              setFocusedSession(msg.focusedSession);
              setCurrentSession(msg.currentSession);
            } else if (msg.type === "your-session") {
              setMySession(msg.name);
              if (msg.clientTty) setClientTty(msg.clientTty);

              // Only claim focus if we're in the current session (same guard as state handler)
              if (!startupFocusSynced && currentSession() === msg.name && sessions.some((session) => session.name === msg.name)) {
                startupFocusSynced = true;
                setFocusedSession(msg.name);
                if (focusedSession() !== msg.name) {
                  startupFocusToPublish = msg.name;
                }
              }
            } else if (msg.type === "pane-focus") {
              // Record the focused paneId globally for cross-row comparison.
              setFocusedPaneId(msg.paneId ?? null);
              if (muxCtx.type !== "none") {
                const isFocused = msg.paneId === muxCtx.paneId;
                // During session switch, suppress transient unfocus to prevent blink
                if (isFocused || Date.now() >= focusSuppressUntil) {
                  setPaneFocused(isFocused);
                }
              }
            } else if (msg.type === "re-identify") {
              reIdentify();
            }
          });

          if (startupFocusToPublish) {
            send({ type: "focus-session", name: startupFocusToPublish });
          }
        } catch {}
      };

      socket.onclose = () => {
        setConnected(false);
        ws = null;
        if (intentionalQuit) return;

        // Retry connection — server may be restarting
        let attempts = 0;
        const MAX_ATTEMPTS = 30;
        const RETRY_MS = 500;

        function retry() {
          if (intentionalQuit || attempts >= MAX_ATTEMPTS) {
            renderer.destroy();
            return;
          }
          attempts++;
          reconnectTimer = setTimeout(() => connectWebSocket(), RETRY_MS);
        }
        retry();
      };
    }

    connectWebSocket();

    onCleanup(() => {
      intentionalQuit = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (resizeHandler) renderer.removeListener("resize", resizeHandler);
      if (ws) ws.close();
    });
  });

  const hasRunning = createMemo(() =>
    sessions.some((s) => s.agentState?.status === "running"),
  );

  createEffect(() => {
    if (!hasRunning()) return;
    const interval = setInterval(() => {
      setSpinIdx((i) => (i + 1) % SPINNERS.length);
    }, 120);
    onCleanup(() => clearInterval(interval));
  });

  // Glow tick: only runs while at least one agent (or session card) is in
  // the `waiting` state — costs nothing in the steady-state. ~10Hz so the
  // sine breathe at 2s period stays smooth without ghosting.
  const hasWaiting = createMemo(() =>
    sessions.some((s) =>
      s.agentState?.status === "waiting"
      || s.agents.some((a) => a.status === "waiting"),
    ),
  );
  const [glowMs, setGlowMs] = createSignal(0);
  createEffect(() => {
    if (!hasWaiting()) return;
    const start = Date.now();
    const interval = setInterval(() => setGlowMs(Date.now() - start), 100);
    onCleanup(() => clearInterval(interval));
  });
  const glowT = createMemo(() => glowPhase(glowMs()));


  // Reset agent-mode when focused session loses all agents
  createEffect(() => {
    const data = focusedData();
    const agents = data?.agents ?? [];
    if (panelFocus() === "agents" && agents.length === 0) {
      setPanelFocus("sessions");
    }
    setFocusedAgentIdx((idx) => Math.min(idx, Math.max(0, agents.length - 1)));
  });

  useKeyboard((key) => {
    const currentModal = modal();

    // --- Theme picker modal: input handles all keys via onKeyDown ---
    if (currentModal === "theme-picker") {
      return;
    }

    // --- Help modal: any key dismisses ---
    if (currentModal === "help") {
      setModal("none");
      return;
    }

    // --- Confirm kill modal ---
    if (currentModal === "confirm-kill") {
      if (key.name === "y") {
        const target = killTarget();
        if (target) send({ type: "kill-session", name: target });
        setKillTarget(null);
        setModal("none");
      } else {
        setKillTarget(null);
        setModal("none");
      }
      return;
    }

    // --- Normal mode keybindings ---
    // Alt+Up / Alt+Down → reorder session
    if ((key.meta || key.option) && (key.name === "up" || key.name === "down")) {
      const focused = focusedSession();
      if (focused) {
        const delta: -1 | 1 = key.name === "up" ? -1 : 1;
        send({ type: "reorder-session", name: focused, delta });
      }
      return;
    }

    switch (key.name) {
      case "q":
        send({ type: "quit" });
        break;
      case "escape":
        if (panelFocus() === "agents") {
          setPanelFocus("sessions");
        }
        break;
      case "up":
      case "k":
        if (panelFocus() === "agents") {
          moveAgentFocus(-1);
        } else {
          moveLocalFocus(-1);
        }
        break;
      case "down":
      case "j":
        if (panelFocus() === "agents") {
          moveAgentFocus(1);
        } else {
          moveLocalFocus(1);
        }
        break;
      case "left":
      case "h":
        if (panelFocus() === "agents") {
          setPanelFocus("sessions");
        }
        break;
      case "right":
      case "l":
        if (panelFocus() === "sessions") {
          const data = focusedData();
          const agents = data?.agents ?? [];
          if (agents.length > 0) {
            setPanelFocus("agents");
            setFocusedAgentIdx((idx) => Math.min(idx, agents.length - 1));
          }
        }
        break;
      case "return": {
        if (panelFocus() === "agents") {
          activateFocusedAgent();
        } else {
          const focused = focusedSession();
          if (focused) switchToSession(focused);
        }
        break;
      }
      case "tab": {
        const list = sessions;
        if (list.length === 0) break;
        const cur = currentSession();
        const idx = list.findIndex((s) => s.name === cur);
        const next = list[(idx + (key.shift ? list.length - 1 : 1)) % list.length];
        if (next) switchToSession(next.name);
        break;
      }
      case "r":
        send({ type: "refresh" });
        flash("refreshed");
        break;
      case "=":
        send({ type: "equalize-width" });
        flash("reset width");
        break;
      case "t":
        themeBeforePreview = theme();
        setModal("theme-picker");
        break;
      case "u":
        send({ type: "show-all-sessions" });
        break;
      case "d": {
        if (panelFocus() === "agents") {
          dismissFocusedAgent();
        } else {
          const focused = focusedSession();
          if (focused) send({ type: "hide-session", name: focused });
        }
        break;
      }
      case "x": {
        if (panelFocus() === "agents") {
          killFocusedAgentPane();
        } else {
          const focused = focusedSession();
          if (focused) {
            setKillTarget(focused);
            setModal("confirm-kill");
          }
        }
        break;
      }
      case "n":
      case "c":
        createNewSession();
        break;
      case "?":
        setModal("help");
        break;
      default: {
        if (key.number) {
          const idx = parseInt(key.name, 10) - 1;
          const target = sessions[idx];
          if (target) switchToSession(target.name);
        }
        break;
      }
    }
  });

  // Header counters (runningCount / errorCount / unseenCount) were retired
  // in the panel redesign — the rolodex is the summary.

  return (
    <box flexDirection="column" flexGrow={1} backgroundColor={P().crust}>
      {/* Header */}
      <box flexDirection="column" paddingLeft={1} paddingTop={1} paddingBottom={0} flexShrink={0}>
        <text>
          <span style={{ fg: paneFocused() ? P().blue : P().overlay1 }}>{BRAND_CLAWD}{" "}</span>
          <span style={{ fg: paneFocused() ? P().text : P().overlay1, attributes: BOLD }}>tcm</span>
          <span style={{ fg: paneFocused() ? P().subtext0 : P().overlay0 }}>{"  "}{String(sessions.length)}{" sessions"}</span>
          <Show when={flashMessage()}><span style={{ fg: P().overlay0, attributes: DIM }}>{" "}{flashMessage()}</span></Show>
        </text>
      </box>

      {/* Session rolodex — focused card pinned at center, neighbors above/below */}
      <box flexDirection="column" flexGrow={1} flexShrink={1} paddingTop={1}>
        {/* Sessions above focused — bottom-aligned so nearest is adjacent */}
        <box flexDirection="column" flexGrow={1} flexBasis={0} justifyContent="flex-end" gap={1} paddingBottom={1}>
          <For each={sessionsBefore()}>
            {(session, i) => (
              <>
                <SessionCard
                  session={session}
                  isFocused={false}
                  isCurrent={session.name === currentSession()}
                  paneFocused={paneFocused}
                  spinIdx={spinIdx}
                  glowT={glowT}
                  theme={theme}
                  statusColors={S}
                  onSelect={() => {
                    setFocusedSession(session.name);
                    send({ type: "focus-session", name: session.name });
                    switchToSession(session.name);
                  }}
                  panelFocus={panelFocus}
                  focusedAgentIdx={focusedAgentIdx}
                  focusedPaneId={focusedPaneId}
                  onAgentDismiss={(agent) => {
                    send({
                      type: "dismiss-agent",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                      paneId: agent.paneId,
                      pid: agent.pid,
                    });
                  }}
                  onAgentFocus={(agent) => {
                    send({
                      type: "focus-agent-pane",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                      threadName: agent.threadName,
                      paneId: agent.paneId,
                    });
                  }}
                />
              </>
            )}
          </For>
        </box>

        {/* Always-visible chevron wrap-rule above the focused card. */}
        <WrapRule direction="up" palette={P()} />

        {/* Focused session — bordered frame pinned at center.
            +2 on height: maxCardHeight() returns inner content rows (name +
            branch + agents + ...); the rounded border eats 1 row top + 1 row
            bottom, and overflow="hidden" clips anything that doesn't fit.
            Without the +2, agent rows get silently truncated whenever the
            card has both a branch and any agents (regression visible since
            commit e1bf37d shrank agent rows from 2 lines to 1). */}
        <box border borderStyle="rounded" borderColor={paneFocused() ? P().blue : P().surface2} flexShrink={0} height={maxCardHeight() + 2} overflow="hidden">
          <Show when={focusedData()}>
            {(data: Accessor<SessionData>) => (
              <SessionCard
                session={data()}
                isFocused={true}
                isCurrent={data().name === currentSession()}
                paneFocused={paneFocused}
                spinIdx={spinIdx}
                glowT={glowT}
                theme={theme}
                statusColors={S}
                onSelect={() => switchToSession(data().name)}
                panelFocus={panelFocus}
                focusedAgentIdx={focusedAgentIdx}
                focusedPaneId={focusedPaneId}
                onAgentDismiss={(agent) => {
                  send({
                    type: "dismiss-agent",
                    session: data().name,
                    agent: agent.agent,
                    threadId: agent.threadId,
                    paneId: agent.paneId,
                    pid: agent.pid,
                  });
                }}
                onAgentFocus={(agent) => {
                  send({
                    type: "focus-agent-pane",
                    session: data().name,
                    agent: agent.agent,
                    threadId: agent.threadId,
                    threadName: agent.threadName,
                    paneId: agent.paneId,
                  });
                }}
              />
            )}
          </Show>
        </box>

        {/* Always-visible chevron wrap-rule below the focused card. */}
        <WrapRule direction="down" palette={P()} />

        {/* Sessions below focused */}
        <box flexDirection="column" flexGrow={1} flexBasis={0} gap={1} paddingTop={1}>
          <For each={sessionsAfter()}>
            {(session, i) => (
              <>
                <SessionCard
                  session={session}
                  isFocused={false}
                  isCurrent={session.name === currentSession()}
                  paneFocused={paneFocused}
                  spinIdx={spinIdx}
                  glowT={glowT}
                  theme={theme}
                  statusColors={S}
                  onSelect={() => {
                    setFocusedSession(session.name);
                    send({ type: "focus-session", name: session.name });
                    switchToSession(session.name);
                  }}
                  panelFocus={panelFocus}
                  focusedAgentIdx={focusedAgentIdx}
                  focusedPaneId={focusedPaneId}
                  onAgentDismiss={(agent) => {
                    send({
                      type: "dismiss-agent",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                      paneId: agent.paneId,
                      pid: agent.pid,
                    });
                  }}
                  onAgentFocus={(agent) => {
                    send({
                      type: "focus-agent-pane",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                      threadName: agent.threadName,
                      paneId: agent.paneId,
                    });
                  }}
                />
              </>
            )}
          </For>
        </box>
      </box>

      {/* Activity zone — two-row full-width seismograph below the rolodex. */}
      <ActivityZone
        focusedSession={focusedData()}
        palette={P()}
        paneFocused={paneFocused()}
      />

      {/* Footer */}
      {(() => {
        const keyFg = () => paneFocused() ? P().subtext0 : P().surface2;
        const labelFg = () => paneFocused() ? P().overlay1 : P().surface2;
        return (
          <box flexDirection="column" paddingLeft={1} paddingBottom={1} paddingTop={0} flexShrink={0}>
            <box height={1}><text style={{ fg: paneFocused() ? P().overlay0 : P().surface2 }}>{"─".repeat(200)}</text></box>
            <Show when={panelFocus() === "sessions"} fallback={
              <text>
                <span style={{ fg: keyFg() }}>{"←"}</span>
                <span style={{ fg: labelFg() }}>{" back  "}</span>
                <span style={{ fg: keyFg() }}>{"⏎"}</span>
                <span style={{ fg: labelFg() }}>{" focus  "}</span>
                <span style={{ fg: keyFg() }}>{"d"}</span>
                <span style={{ fg: labelFg() }}>{" dismiss  "}</span>
                <span style={{ fg: keyFg() }}>{"x"}</span>
                <span style={{ fg: labelFg() }}>{" kill  "}</span>
                <span style={{ fg: keyFg() }}>{"?"}</span>
                <span style={{ fg: labelFg() }}>{" help"}</span>
              </text>
            }>
              <text>
                <span style={{ fg: keyFg() }}>{"⇥"}</span>
                <span style={{ fg: labelFg() }}>{" cycle  "}</span>
                <span style={{ fg: keyFg() }}>{"⏎"}</span>
                <span style={{ fg: labelFg() }}>{" go  "}</span>
                <span style={{ fg: keyFg() }}>{"d"}</span>
                <span style={{ fg: labelFg() }}>{" hide  "}</span>
                <span style={{ fg: keyFg() }}>{"?"}</span>
                <span style={{ fg: labelFg() }}>{" help"}</span>
              </text>
            </Show>
          </box>
        );
      })()}

      {/* Theme picker overlay */}
      <Show when={modal() === "theme-picker"}>
        <ThemePicker
          palette={P}
          onSelect={(name) => {
            themeBeforePreview = null;
            applyTheme(name);
            setModal("none");
          }}
          onPreview={(name) => {
            previewTheme(name);
          }}
          onClose={() => {
            if (themeBeforePreview) {
              setTheme(themeBeforePreview);
              themeBeforePreview = null;
            }
            setModal("none");
          }}
        />
      </Show>

      {/* Kill confirmation overlay */}
      <Show when={modal() === "confirm-kill"}>
        <box
          position="absolute"
          top={0} left={0} right={0} bottom={0}
          justifyContent="center"
          alignItems="center"
          backgroundColor="transparent"
        >
          <box
            border
            borderStyle="rounded"
            borderColor={P().red}
            backgroundColor={P().mantle}
            padding={1}
            paddingX={2}
            flexDirection="column"
            alignItems="center"
          >
            <text>
              <span style={{ fg: P().red, attributes: BOLD }}>Kill session?</span>
            </text>
            <text>
              <span style={{ fg: P().text }}>{killTarget() ?? ""}</span>
            </text>
            <text>
              <span style={{ fg: P().overlay0 }}>y</span>
              <span style={{ fg: P().overlay1 }}>/</span>
              <span style={{ fg: P().overlay0 }}>n</span>
            </text>
          </box>
        </box>
      </Show>

      {/* Help overlay */}
      <Show when={modal() === "help"}>
        <box
          position="absolute"
          top={0} left={0} right={0} bottom={0}
          justifyContent="center"
          alignItems="center"
          backgroundColor="transparent"
        >
          <box
            border
            borderStyle="rounded"
            borderColor={P().blue}
            backgroundColor={P().mantle}
            paddingX={2}
            paddingY={1}
            flexDirection="column"
          >
            <text><span style={{ fg: P().text, attributes: BOLD }}>Keybindings</span></text>
            <box height={1}><text style={{ fg: P().surface2 }}>{"─".repeat(200)}</text></box>
            {([
              ["j/k", "navigate"],
              ["⏎", "switch to session"],
              ["⇥", "cycle next"],
              ["⇧⇥", "cycle prev"],
              ["→/l", "agent detail"],
              ["←/h", "back to sessions"],
              ["d", "hide / dismiss"],
              ["u", "unhide all"],
              ["x", "kill session / pane"],
              ["n/c", "new session"],
              ["r", "refresh"],
              ["t", "theme picker"],
              ["=", "reset width"],
              ["⌥↑↓", "reorder"],
              ["1-9", "jump to session"],
              ["q", "quit"],
            ] as const).map(([k, v]) => (
              <text>
                <span style={{ fg: P().text }}>{k.padEnd(7)}</span>
                <span style={{ fg: P().subtext0 }}>{v}</span>
              </text>
            ))}
            <box height={1} />
            <text><span style={{ fg: P().overlay0 }}>press any key to close</span></text>
          </box>
        </box>
      </Show>
    </box>
  );
}

// --- Theme Picker ---

interface ThemePickerProps {
  palette: Accessor<Theme["palette"]>;
  onSelect: (name: string) => void;
  onPreview: (name: string) => void;
  onClose: () => void;
}

function ThemePicker(props: ThemePickerProps) {
  let inputRef: InputRenderable;

  const [query, setQuery] = createSignal("");
  const [selected, setSelected] = createSignal(0);

  const filtered = createMemo(() => {
    const q = query().toLowerCase();
    if (!q) return THEME_NAMES;
    return THEME_NAMES.filter((name) => name.toLowerCase().includes(q));
  });

  function move(direction: -1 | 1) {
    const list = filtered();
    if (!list.length) return;
    let next = selected() + direction;
    if (next < 0) next = list.length - 1;
    if (next >= list.length) next = 0;
    setSelected(next);
    const name = list[next];
    if (name) props.onPreview(name);
  }

  function confirm() {
    const name = filtered()[selected()];
    if (name) props.onSelect(name);
  }

  function handleKeyDown(e: KeyEvent) {
    if (e.name === "up") {
      e.preventDefault();
      move(-1);
    } else if (e.name === "down") {
      e.preventDefault();
      move(1);
    } else if (e.name === "return") {
      e.preventDefault();
      confirm();
    } else if (e.name === "escape") {
      e.preventDefault();
      props.onClose();
    }
  }

  function handleInput(value: string) {
    setQuery(value);
    setSelected(0);
  }

  const MAX_VISIBLE = 12;

  const scrollOffset = createMemo(() => {
    const sel = selected();
    if (sel < MAX_VISIBLE) return 0;
    return sel - MAX_VISIBLE + 1;
  });

  const visibleItems = createMemo(() => {
    const list = filtered();
    return list.slice(scrollOffset(), scrollOffset() + MAX_VISIBLE);
  });

  return (
    <box
      position="absolute"
      top={0} left={0} right={0} bottom={0}
      justifyContent="center"
      alignItems="center"
      backgroundColor="transparent"
    >
      <box
        border
        borderStyle="rounded"
        borderColor={props.palette().blue}
        backgroundColor={props.palette().mantle}
        padding={1}
        flexDirection="column"
        width={30}
      >
        <text>
          <span style={{ fg: props.palette().blue, attributes: BOLD }}>Select Theme</span>
        </text>
        <box height={1}><text style={{ fg: props.palette().surface2 }}>{"─".repeat(200)}</text></box>
        <box border borderColor={props.palette().surface1} marginBottom={1}>
          <input
            ref={(r: InputRenderable) => { inputRef = r; inputRef.focus(); }}
            value={query()}
            onInput={handleInput}
            onKeyDown={handleKeyDown}
            placeholder="Search themes…"
            backgroundColor={props.palette().surface0}
            focusedBackgroundColor={props.palette().surface0}
            textColor={props.palette().text}
            cursorColor={props.palette().blue}
            placeholderColor={props.palette().overlay0}
          />
        </box>
        <Show when={filtered().length > 0} fallback={
          <box paddingLeft={1}><text style={{ fg: props.palette().overlay0 }}>No matches</text></box>
        }>
          <For each={visibleItems()}>
            {(name) => {
              const idx = createMemo(() => filtered().indexOf(name));
              const isSel = createMemo(() => idx() === selected());
              return (
                <box
                  paddingLeft={1}
                  paddingRight={1}
                  backgroundColor={isSel() ? props.palette().surface0 : undefined}
                >
                  <text style={{ fg: isSel() ? props.palette().text : props.palette().subtext0 }}>
                    {isSel() ? "▸ " : "  "}{name}
                  </text>
                </box>
              );
            }}
          </For>
          <Show when={filtered().length > MAX_VISIBLE}>
            <text style={{ fg: props.palette().overlay0, attributes: DIM }}>
              {"  "}↕ {filtered().length - MAX_VISIBLE} more
            </text>
          </Show>
        </Show>
        <box height={1}><text style={{ fg: props.palette().surface2 }}>{"─".repeat(200)}</text></box>
        <text style={{ fg: props.palette().overlay0 }}>
          <span style={{ attributes: DIM }}>↑↓</span>{" browse  "}
          <span style={{ attributes: DIM }}>⏎</span>{" select  "}
          <span style={{ attributes: DIM }}>esc</span>{" close"}
        </text>
      </box>
    </box>
  );
}

interface AgentListItemProps {
  agent: SessionData["agents"][number];
  palette: Accessor<Theme["palette"]>;
  statusColors: Accessor<Theme["status"]>;
  spinIdx: Accessor<number>;
  // 0..1 sine-driven glow phase. Only consulted when this row is waiting;
  // upstream ticks the signal only while any row is waiting, so the closure
  // is dormant otherwise.
  glowT: Accessor<number>;
  isKeyboardFocused: boolean;
  isPaneFocused: boolean;
  onDismiss: () => void;
  onFocusPane: () => void;
}

function AgentListItem(props: AgentListItemProps) {
  const P = () => props.palette();
  const [isDismissHover, setIsDismissHover] = createSignal(false);
  const [isFlash, setIsFlash] = createSignal(false);

  // Resolve the five-label scheme from tracker status + liveness
  const label = (): "working" | "waiting" | "ready" | "stopped" | "error" => {
    const s = props.agent.status;
    if (s === "running") return "working";
    if (s === "waiting") return "waiting";
    if (s === "error") return "error";
    // done/interrupted/idle — liveness determines ready vs stopped
    // "alive" means pane scanner confirmed the process exists → ready at prompt
    // "exited" means pane scanner saw it disappear → stopped
    // undefined means no pane data (e.g. watcher-seeded, server just started)
    //   → for terminal statuses (done/interrupted), assume stopped
    //   → for idle (synthetic cold-start), assume ready
    if (props.agent.liveness === "alive") return "ready";
    if (s === "done" || s === "interrupted") return "stopped";
    return "ready";
  };

  const isUnseen = () => props.agent.unseen === true;

  const icon = () => {
    const l = label();
    if (l === "working") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
    if (l === "waiting") return SEV_WAITING;
    if (l === "ready") return SEV_READY;
    if (l === "stopped") return SEV_STOPPED;
    if (l === "error") return SEV_ERROR;
    return SEV_READY;
  };

  const color = () => {
    const l = label();
    if (l === "working") return P().blue;
    if (l === "waiting") return P().yellow;
    if (l === "ready") return P().green;
    if (l === "stopped") return P().surface2;
    if (l === "error") return P().red;
    return P().surface2;
  };

  const triggerFlash = () => {
    setIsFlash(true);
    setTimeout(() => setIsFlash(false), 150);
  };

  const bgColor = () => {
    if (isFlash()) return P().surface1;
    if (props.isKeyboardFocused) return P().surface0;
    return "transparent";
  };

  // Agent-name fg. Normally the focus/unseen-driven palette tone; while the
  // agent is waiting on input, the row breathes toward `yellow` so it pulls
  // the eye without adding chrome. (Status glyph still shows yellow; this is
  // the only animated part.)
  const nameFg = () => {
    const base = isUnseen()
      ? P().teal
      : (props.isKeyboardFocused ? P().text : P().subtext1);
    if (label() !== "waiting") return base;
    return lerpHex(base, P().yellow, props.glowT());
  };

  return (
    <box flexDirection="column" flexShrink={0} onMouseDown={() => {
      triggerFlash();
      props.onFocusPane();
    }}>
      <box
        flexDirection="column"
        backgroundColor={bgColor()}
        paddingRight={1}
      >
        {/* Row 1: dismiss + agent name + threadId ... unseen badge + status icon */}
        <box flexDirection="row">
          <text
            flexShrink={0}
            onMouseDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
              props.onDismiss();
            }}
            onMouseOver={() => setIsDismissHover(true)}
            onMouseOut={() => setIsDismissHover(false)}
          >
            <span style={{ fg: isDismissHover() ? P().red : P().overlay0 }}>{(props.agent.windowIndex != null ? String(props.agent.windowIndex).padStart(2, " ") : " ·") + " "}</span>
          </text>
          <text flexGrow={1} truncate>
            <span style={{
              // Focused-pane highlight wins over the normal name-fg palette,
              // matching the trailing "•" dot. Same anchor color, two surfaces.
              fg: props.isPaneFocused ? P().sky : nameFg(),
              attributes: props.isKeyboardFocused ? BOLD : undefined,
            }}>{AGENT_GLYPHS[props.agent.agent] ?? props.agent.agent}</span>
            <Show when={props.agent.subagent}>
              <span style={{ fg: P().overlay1 }}>{"  "}{props.agent.subagent}</span>
            </Show>
            <Show when={props.agent.threadId}>
              <span style={{ fg: P().overlay0, attributes: DIM }}>{"  "}{shortThreadId(props.agent.threadId!)}</span>
            </Show>
            <Show when={props.isPaneFocused}>
              <span style={{ fg: P().sky }}>{" •"}</span>
            </Show>
          </text>
          <text flexShrink={0}>
            <span style={{ fg: color() }}>{" "}{icon()}</span>
          </text>
        </box>

        {/* Row 2 (tool description / thread name) retired — now surfaced
            in the standalone ActivityZone, persistently and across focus
            changes. */}
      </box>
    </box>
  );
}

// --- Session Card ---

interface SessionCardProps {
  session: SessionData;
  isFocused: boolean;
  isCurrent: boolean;
  paneFocused: Accessor<boolean>;
  spinIdx: Accessor<number>;
  glowT: Accessor<number>;
  theme: Accessor<Theme>;
  statusColors: Accessor<Theme["status"]>;
  onSelect: () => void;
  panelFocus: Accessor<"sessions" | "agents">;
  focusedAgentIdx: Accessor<number>;
  focusedPaneId: Accessor<string | null>;
  onAgentDismiss: (agent: SessionData["agents"][number]) => void;
  onAgentFocus: (agent: SessionData["agents"][number]) => void;
}

function SessionCard(props: SessionCardProps) {
  const P = () => props.theme().palette;

  // Resolve five-label scheme for session card
  const label = (): "working" | "waiting" | "ready" | "stopped" | "error" => {
    const state = props.session.agentState;
    if (!state) return "ready";
    if (state.status === "running") return "working";
    if (state.status === "waiting") return "waiting";
    if (state.status === "error") return "error";
    if (state.liveness === "alive") return "ready";
    if (state.status === "done" || state.status === "interrupted") return "stopped";
    return "ready";
  };

  const unseen = () => props.session.unseen;

  // B5 (locked decision): the session-row severity gutter is BLANK when
  // the session is in a nominal state (ready/stopped). Only attention-
  // needing states (working/waiting/error) show a glyph. Applies on the
  // session row whether the card is collapsed or focused; agent-level
  // severity still appears on each agent row inside the focused card.
  const statusIcon = () => {
    const l = label();
    if (l === "working") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
    if (l === "waiting") return SEV_WAITING;
    if (l === "error") return SEV_ERROR;
    return "";
  };

  const statusColor = () => {
    const l = label();
    if (l === "working") return P().blue;
    if (l === "waiting") return P().yellow;
    if (l === "ready") return P().green;
    if (l === "stopped") return P().surface2;
    if (l === "error") return P().red;
    return P().surface2;
  };

  const nameColor = () => {
    const focused = props.paneFocused();
    // Unseen sessions get the teal colour shift (color-only marker, replaces
    // the retired ● glyph).
    const base = unseen()
      ? P().teal
      : props.isCurrent
        ? (focused ? P().text : P().subtext0)
        : (focused ? P().subtext1 : P().overlay1);
    // Mirror AgentListItem.nameFg: when this session is itself waiting (e.g.
    // collapsed card with no visible agent rows), breathe the name toward
    // yellow so the gated tick — which counts session-level waiting — has
    // something to paint.
    if (label() !== "waiting") return base;
    return lerpHex(base, P().yellow, props.glowT());
  };

  const truncName = () => {
    const n = props.session.name;
    return n.length > 18 ? n.slice(0, 17) + "…" : n;
  };

  const truncBranch = () => {
    const b = props.session.branch;
    if (!b) return "";
    return b.length > 15 ? b.slice(0, 14) + "…" : b;
  };

  const metaSummary = () => {
    const meta = props.session.metadata;
    if (!meta) return "";
    const parts: string[] = [];
    if (meta.status) parts.push(meta.status.text);
    if (meta.progress) {
      if (meta.progress.current != null && meta.progress.total != null) {
        parts.push(`${meta.progress.current}/${meta.progress.total}`);
      } else if (meta.progress.percent != null) {
        parts.push(`${Math.round(meta.progress.percent * 100)}%`);
      }
      if (meta.progress.label) parts.push(meta.progress.label);
    }
    return parts.join(" · ");
  };

  const agentCount = () =>
    props.session.agents?.filter((a) =>
      a.liveness === "alive" ||
      (a.liveness !== "exited" && !["done", "error", "interrupted"].includes(a.status)),
    ).length ?? 0;

  // Locked count format (B1 / Q3): bare numeric, capped at "9+". The legacy
  // "●N" badge and the "2π" same-type compaction are both retired.
  const agentBadge = () => {
    const n = agentCount();
    if (n === 0) return "";
    if (n >= 10) return "9+";
    return String(n);
  };

  const agentBadgeColor = () => {
    if (props.isFocused) return P().subtext0;
    return P().overlay0;
  };

  const metaTone = () => props.session.metadata?.status?.tone;

  const bgColor = () => "transparent";

  // --- Expanded content helpers ---
  const dirParts = () => formatDir(props.session.dir);
  const dirMismatch = () => dirParts().project !== props.session.name;
  const agents = () => props.session.agents ?? [];
  const meta = () => props.session.metadata;
  // Note: logs are summarised by the standalone ActivityZone seismograph
  // beneath the rolodex; collapsed cards keep a one-line status/progress
  // summary (metaSummary). The focused card body stays lean: name + branch +
  // dir + agents only.

  // ▎ current-session left bar retired in render: bold name + row position
  // already signal current state.

  return (
    <box id={`session-${props.session.name}`} flexDirection="column" flexShrink={0}>
      <box
        flexDirection="row"
        backgroundColor={bgColor()}
        paddingLeft={1}
        onMouseDown={props.onSelect}
      >
        {/* Content */}
        <box flexDirection="column" flexGrow={1} paddingRight={1}>
          {/* Row 1: name + agent badge (left) + status icons (right) */}
          <box flexDirection="row">
            <text truncate>
              <span style={{ fg: nameColor(), attributes: props.isCurrent ? BOLD : undefined }}>
                {truncName()}
              </span>
              <Show when={agentBadge()}>
                <span style={{ fg: agentBadgeColor() }}>{" "}{agentBadge()}</span>
              </Show>
            </text>
            <box flexGrow={1} />
            {/* Unseen marker is now color-only on the name (see nameColor()). */}
            {/* Row-level statusIcon shown only on collapsed cards — the focused
                card's agent rows below already render per-agent severity, so a
                second spinner/icon at the session row is redundant. */}
            <Show when={statusIcon() && !props.isFocused}>
              <text flexShrink={0}>
                <span style={{ fg: statusColor() }}>{" "}{statusIcon()}</span>
              </text>
            </Show>
          </box>

          {/* Row 2: branch + dir-mismatch flag (focused only) */}
          <Show when={props.session.branch}>
            <box flexDirection="row">
              <text truncate>
                <span style={{ fg: props.isFocused ? P().pink : (props.paneFocused() ? P().overlay0 : P().surface2) }}>
                  {BRANCH_GLYPH}{" "}{truncBranch()}
                </span>
              </text>
              <box flexGrow={1} />
              <Show when={props.isFocused && dirMismatch()}>
                <text flexShrink={0}>
                  <span style={{ fg: P().overlay0, attributes: DIM }}>{" "}{DIR_MISMATCH_GLYPH}</span>
                </text>
              </Show>
            </box>
          </Show>

          {/* Row 3: metadata summary (status + progress) — only when collapsed */}
          <Show when={!props.isFocused && metaSummary()}>
            <text truncate>
              <span style={{ fg: toneColor(metaTone(), P()), attributes: DIM }}>{metaSummary()}</span>
            </text>
          </Show>
        </box>
      </box>

      {/* Expanded detail — shown inline when focused */}
      <Show when={props.isFocused}>
        <box flexDirection="column" paddingLeft={1}>
          {/* Directory mismatch is now flagged with DIR_MISMATCH_GLYPH on the
              branch row above; the inline two-line cwd block has been retired. */}
          {/* Agent instances */}
          <Show when={agents().length > 0}>
            <box flexDirection="column">
              <For each={agents()}>
                {(agent, i) => (
                  <AgentListItem
                    agent={agent}
                    palette={() => P()}
                    statusColors={props.statusColors}
                    spinIdx={props.spinIdx}
                    glowT={props.glowT}
                    isKeyboardFocused={props.panelFocus() === "agents" && i() === props.focusedAgentIdx()}
                    isPaneFocused={agent.paneId != null && agent.paneId === props.focusedPaneId()}
                    onDismiss={() => props.onAgentDismiss(agent)}
                    onFocusPane={() => props.onAgentFocus(agent)}
                  />
                )}
              </For>
            </box>
          </Show>

          {/* Metadata moved to the ActivityZone component (see App). The
              focused card no longer renders status / progress / logs inline. */}
        </box>
      </Show>
    </box>
  );
}

async function main() {
  const mock = parseMockFlag();
  if (!mock) {
    await ensureServer();
  } else {
    const scenario = getScenario(mock);
    if (!scenario) {
      console.error(`Unknown mock scenario: ${mock}`);
      console.error(`Available: ${listScenarios().join(", ")}`);
      process.exit(1);
    }
    console.error(`[mock mode: ${scenario.name}] ${scenario.description}`);
  }
  render(() => <App />, {
    exitOnCtrlC: true,
    targetFps: 30,
    useMouse: true,
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
