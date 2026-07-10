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
  AGENT_GLYPHS,
} from "./vocab";
import { tier } from "./tiers";
import {
  type ActivityLog,
  type BucketIcon,
  SPARKLINE_BUCKET_MS,
  SPARK_ROWS,
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
const HEADER_ROWS = 2; // App header: one title row plus paddingTop={1}; keep in sync with its box.
const FOOTER_ROWS = 3; // App footer: separator, key row, and paddingBottom={1}; keep in sync with its box.
const ACTIVITY_ZONE_ROWS = SPARK_ROWS + 1; // ActivityZone: histogram rows plus the icon/suffix row.
const CARD_CHROME_ROWS = 3; // Session frame: rounded border top/bottom plus the name row.
const BRANCH_ROWS = 1; // SessionCard adds exactly one row when session.branch is present.
const HIDDEN_AGENTS_ROWS = 1; // SessionCard adds exactly one "+N more" row when agents are trimmed.
const OVERFLOW_INDICATOR_ROWS = 1; // "▲ N more" / "▼ N more" line when the session stack scrolls.

function toneColor(tone: MetadataTone | undefined, palette: ReturnType<() => Theme["palette"]>): string {
  switch (tone) {
    case "success": return palette.green;
    case "error": return palette.red;
    case "warn": return palette.yellow;
    case "info": return palette.blue;
    default: return palette.overlay0;
  }
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

type SessionAgent = SessionData["agents"][number];

function isLiveAgent(agent: SessionAgent): boolean {
  return agent.liveness === "alive"
    || (agent.liveness !== "exited" && !["done", "error", "interrupted"].includes(agent.status));
}

function liveFirstAgents(agents: readonly SessionAgent[]): SessionAgent[] {
  const live: SessionAgent[] = [];
  const inactive: SessionAgent[] = [];
  for (const agent of agents) (isLiveAgent(agent) ? live : inactive).push(agent);
  return live.concat(inactive);
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

// ────────────────────────────────────────────────────────────────────────────
// Activity zone — pure histogram/icon logic lives in ./activity.ts. The
// original text-stream layout spec (docs/simmer/activity-zone/result.md) is
// superseded by the seismograph below; its sparkline contract carries over.
// ────────────────────────────────────────────────────────────────────────────

/**
 * Activity zone: a two-row, full-width "seismograph" beneath the session list.
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
 * text tiers).
 *
 * Empty states: no logs → flat baseline + blank glyph row; window slid past
 * the newest log → flat baseline + a dim right-aligned `·Nm` age marker (the
 * one text survivor — it disambiguates "quiet now" from "dead for an hour").
 */
function ActivityZone(props: {
  focusedSession: SessionData | null;
  palette: ThemePalette;
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

  const sparklineStyle = () => tier("secondary", props.palette);
  const suffixStyle    = () => tier("dim",       props.palette);
  // Glyph freshness mirrors the retired description tiers: bright on the
  // newest occupied bucket, dim on older ones. Severity/tone bypass the tiers.
  const freshFg = () => props.palette.text;
  const oldFg   = () => props.palette.overlay1;
  const iconStyle = (cell: BucketIcon, idx: number) => {
    if (cell.kind === "error")  return { fg: props.palette.red };
    if (cell.kind === "system") return { fg: toneColor(cell.tone, props.palette) };
    return { fg: idx === freshestIdx() ? freshFg() : oldFg() };
  };

  return (
    <box flexDirection="column" flexShrink={0} paddingLeft={PAD} paddingRight={PAD}>
      <Index each={sparkline()}>
        {(row) => (
          <text wrapMode="none" style={sparklineStyle()}>
            <span style={sparklineStyle()}>{row()}</span>
          </text>
        )}
      </Index>
      <Show when={!staleSuffix()} fallback={
        <box height={1} flexDirection="row" justifyContent="flex-end">
          <text style={suffixStyle()}>{staleSuffix()}</text>
        </box>
      }>
        <text wrapMode="none" style={sparklineStyle()}>
          <span style={sparklineStyle()}>{" ".repeat(geometry().leftoverCols)}</span>
          <Index each={icons()}>
            {(cell, idx) => (
              <Show when={cell()} fallback={<span style={sparklineStyle()}>{BLANK_SLOT}</span>}>
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

  const [sessions, setSessions] = createStore<SessionData[]>([]);
  const [focusedSession, setFocusedSession] = createSignal<string | null>(null);
  const [currentSession, setCurrentSession] = createSignal<string | null>(null);
  const [mySession, setMySession] = createSignal<string | null>(null);
  const [connected, setConnected] = createSignal(false);
  const [spinIdx, setSpinIdx] = createSignal(0);

  // --- Pane focus: does this terminal pane have focus? Drives the header
  //     FOCUS chip — the "your keystrokes land here" warning. ---
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

  const listBudgetRows = createMemo(() =>
    Math.max(0, termDims().height - HEADER_ROWS - FOOTER_ROWS - ACTIVITY_ZONE_ROWS));

  // Cards show every agent row; the scrolling session window absorbs the
  // height. "+N more" survives only as a guard when a single card on its own
  // cannot fit the viewport alongside the scroll indicators.
  const visibleAgentCounts = createMemo(() => {
    const availableRows = listBudgetRows();
    return sessions.map((session) => {
      const total = session.agents?.length ?? 0;
      const chrome = CARD_CHROME_ROWS + (session.branch ? BRANCH_ROWS : 0);
      const indicatorAllowance = sessions.length > 1 ? 2 * OVERFLOW_INDICATOR_ROWS : 0;
      const maxAgents = Math.max(1, availableRows - chrome - HIDDEN_AGENTS_ROWS - indicatorAllowance);
      return Math.min(total, maxAgents);
    });
  });

  // Session-list viewport: when the stack is taller than the pane, render a
  // window of cards around the focused one plus ▲/▼ overflow indicators, so
  // every session stays reachable (the old render-everything flex-end layout
  // silently spilled cards past the top edge).
  const sessionWindow = createMemo(() => {
    const counts = visibleAgentCounts();
    const budget = listBudgetRows();
    const len = sessions.length;
    const heights = sessions.map((session, i) => {
      const total = session.agents?.length ?? 0;
      const visible = counts[i] ?? 0;
      return CARD_CHROME_ROWS + (session.branch ? BRANCH_ROWS : 0)
        + visible + (visible < total ? HIDDEN_AGENTS_ROWS : 0);
    });
    if (heights.reduce((total, h) => total + h, 0) <= budget) {
      return { start: 0, end: len, above: 0, below: 0 };
    }

    const indicatorRows = (start: number, end: number) =>
      (start > 0 ? OVERFLOW_INDICATOR_ROWS : 0) + (end < len ? OVERFLOW_INDICATOR_ROWS : 0);
    const anchor = Math.min(Math.max(0, focusedIdx()), len - 1);
    let start = anchor;
    let end = anchor + 1;
    let used = heights[anchor] ?? 0;
    let grew = true;
    while (grew) {
      grew = false;
      if (end < len && used + heights[end]! + indicatorRows(start, end + 1) <= budget) {
        used += heights[end]!;
        end++;
        grew = true;
      }
      if (start > 0 && used + heights[start - 1]! + indicatorRows(start - 1, end) <= budget) {
        start--;
        used += heights[start]!;
        grew = true;
      }
    }
    return { start, end, above: start, below: len - end };
  });

  // Overflow indicators are list items rather than siblings of the card
  // <For>: the renderer mounts a dynamic fragment's children after all of
  // its parent's static children, so a second fragment in the same box
  // scrambles the visual order.
  type OverflowIndicator = { overflow: -1 | 1; count: number };
  const listItems = createMemo<(SessionData | OverflowIndicator)[]>(() => {
    const win = sessionWindow();
    const items: (SessionData | OverflowIndicator)[] = [];
    if (win.above > 0) items.push({ overflow: -1, count: win.above });
    items.push(...sessions.slice(win.start, win.end));
    if (win.below > 0) items.push({ overflow: 1, count: win.below });
    return items;
  });

  const focusedVisibleAgents = createMemo(() => {
    const data = focusedData();
    const index = focusedIdx();
    if (!data || index < 0) return [];
    return liveFirstAgents(data.agents ?? []).slice(0, visibleAgentCounts()[index] ?? 0);
  });

  function send(cmd: ClientCommand) {
    if (connected() && ws) ws.send(JSON.stringify(cmd));
  }

  // Suppress transient pane-focus-in events briefly after session switch:
  // clicking a card focuses the sidebar pane for an instant before tmux
  // hands focus to the target session's pane, which would flash the FOCUS
  // chip on every switch.
  let focusSuppressUntil = 0;

  function switchToSession(name: string) {
    // Optimistic local update — makes rapid Tab repeat instant by removing
    // the server/hook round-trip from the next-Tab decision.
    // The server's focus/state broadcast will reconcile if needed.
    setCurrentSession(name);
    setFocusedSession(name);
    setPanelFocus("sessions");
    setFocusedAgentIdx(0);
    // Focus is leaving for the target pane; no trailing pane-focus event
    // arrives once the handoff settles, so record the destination now.
    setPaneFocused(false);
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
    setFocusedAgentIdx((index) => Math.min(index, Math.max(0, (visibleAgentCounts()[nextIdx] ?? 0) - 1)));
    send({ type: "focus-session", name: next });
  }

  function moveAgentFocus(delta: -1 | 1) {
    const agents = focusedVisibleAgents();
    if (agents.length === 0) return;
    const idx = focusedAgentIdx();
    const next = Math.max(0, Math.min(agents.length - 1, idx + delta));
    setFocusedAgentIdx(next);
  }

  function activateFocusedAgent() {
    const data = focusedData();
    const agents = focusedVisibleAgents();
    const agent = agents[focusedAgentIdx()];
    if (!agent || !data) return;
    send({
      type: "focus-agent-pane",
      session: data.name,
      agent: agent.agent,
      threadId: agent.threadId,
      threadName: agent.threadName,
      paneId: agent.paneId,
      clientTty: clientTty() || undefined,
    });
  }

  function dismissFocusedAgent() {
    const data = focusedData();
    const agents = focusedVisibleAgents();
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
    const agents = focusedVisibleAgents();
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
                // During session switch, ignore the transient focus-in from
                // the card click so the FOCUS chip doesn't flash.
                if (!isFocused || Date.now() >= focusSuppressUntil) {
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
    const agents = focusedVisibleAgents();
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
  // in the panel redesign; the session list is the summary.

  return (
    <box flexDirection="column" flexGrow={1} backgroundColor={P().crust}>
      {/* Header. The FOCUS chip is the pane-focus signal (the panel no
          longer dims when unfocused): loud when keystrokes would land
          here, absent the rest of the time. */}
      <box flexDirection="row" paddingLeft={1} paddingRight={1} paddingTop={1} paddingBottom={0} flexShrink={0}>
        <text flexGrow={1} wrapMode="none">
          <span style={{ fg: P().blue }}>{BRAND_CLAWD}{" "}</span>
          <span style={{ fg: P().text, attributes: BOLD }}>tcm</span>
          <span style={{ fg: P().subtext0 }}>{"  "}{String(sessions.length)}{" sessions"}</span>
          <Show when={flashMessage()}><span style={{ fg: P().overlay0, attributes: DIM }}>{" "}{flashMessage()}</span></Show>
        </text>
        <Show when={paneFocused()}>
          <text flexShrink={0} style={{ fg: P().crust, bg: P().blue, attributes: BOLD }}> FOCUS </text>
        </Show>
      </box>

      <box flexDirection="column" flexGrow={1} flexShrink={1} justifyContent="flex-end" gap={0}>
        <For each={listItems()}>
          {(item) => {
            if ("overflow" in item) {
              return (
                <box paddingLeft={2} flexShrink={0} onMouseDown={() => moveLocalFocus(item.overflow)}>
                  <text wrapMode="none" style={tier("muted", P())}>
                    {item.overflow < 0 ? "▲ " : "▼ "}{item.count}{" more"}
                  </text>
                </box>
              );
            }
            const session = item;
            const sessionIdx = createMemo(() => sessions.indexOf(session));
            const isSelected = () => session.name === focusedSession();
            return (
              // Keep overflow unset: OpenTUI drops the last row's click zone
              // when a bordered box also uses overflow="hidden".
              <box
                border
                borderStyle="rounded"
                borderColor={isSelected() ? P().blue : P().surface2}
                flexShrink={0}
              >
                <SessionCard
                  session={session}
                  isSelected={isSelected()}
                  isCurrent={session.name === currentSession()}
                  visibleAgentCount={visibleAgentCounts()[sessionIdx()] ?? 0}
                  spinIdx={spinIdx}
                  glowT={glowT}
                  theme={theme}
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
                      clientTty: clientTty() || undefined,
                    });
                  }}
                />
              </box>
            );
          }}
        </For>
      </box>

      {/* Activity zone: two-row full-width seismograph below the session list. */}
      <ActivityZone
        focusedSession={focusedData()}
        palette={P()}
      />

      {/* Footer */}
      {(() => {
        const keyFg = () => P().subtext0;
        const labelFg = () => P().overlay1;
        return (
          <box flexDirection="column" paddingLeft={1} paddingBottom={1} paddingTop={0} flexShrink={0}>
            <box height={1}><text style={{ fg: P().overlay0 }}>{"─".repeat(200)}</text></box>
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
  spinIdx: Accessor<number>;
  // 0..1 sine-driven glow phase. Only consulted when this row is waiting;
  // upstream ticks the signal only while any row is waiting, so the closure
  // is dormant otherwise.
  glowT: Accessor<number>;
  isSessionSelected: boolean;
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

  const nameStyle = () => {
    const base = props.isSessionSelected
      ? {
          fg: props.isKeyboardFocused ? P().text : P().subtext1,
          attributes: props.isKeyboardFocused ? BOLD : undefined,
        }
      : tier("dim", P());
    const attentionFg = isUnseen() ? P().teal : base.fg;
    return {
      ...base,
      fg: label() === "waiting" ? lerpHex(attentionFg, P().yellow, props.glowT()) : attentionFg,
    };
  };
  // Supporting detail steps below muted on unselected cards, preserving
  // the selected/unselected contrast the tier scale doesn't encode.
  const supportingStyle = () => props.isSessionSelected
    ? tier("muted", P())
    : { fg: P().surface2 };
  const identityStyle = () => props.isPaneFocused
    ? { fg: P().sky }
    : nameStyle();

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
            style={supportingStyle()}
            onMouseDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
              props.onDismiss();
            }}
            onMouseOver={() => setIsDismissHover(true)}
            onMouseOut={() => setIsDismissHover(false)}
          >
            <span style={isDismissHover() ? { fg: P().red } : supportingStyle()}>{(props.agent.windowIndex != null ? String(props.agent.windowIndex).padStart(2, " ") : " ·") + " "}</span>
          </text>
          {/* wrapMode="none" is load-bearing: without it opentui measures
              at the default word-wrap height and yoga grows the row,
              spilling past the card's border (which does not
              clip; see the overflow note on the card frame). No
              `truncate`: it renders a middle-ellipsis; plain end-clip is
              the intended look. */}
          <text flexGrow={1} wrapMode="none" style={nameStyle()}>
            <span style={identityStyle()}>{AGENT_GLYPHS[props.agent.agent] ?? props.agent.agent}</span>
            <Show when={props.agent.subagent}>
              <span style={tier(props.isSessionSelected ? "dim" : "muted", P())}>{"  "}{props.agent.subagent}</span>
            </Show>
            {/* Session name from the agent-ouija registry when known;
                the 4-char uuid suffix is the fallback identity. */}
            <Show when={props.agent.threadName || props.agent.threadId}>
              <span style={supportingStyle()}>{"  "}{props.agent.threadName || shortThreadId(props.agent.threadId!)}</span>
            </Show>
            <Show when={props.isPaneFocused}>
              <span style={identityStyle()}>{" •"}</span>
            </Show>
          </text>
          <text flexShrink={0} style={{ fg: color() }}>
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
  isSelected: boolean;
  isCurrent: boolean;
  visibleAgentCount: number;
  spinIdx: Accessor<number>;
  glowT: Accessor<number>;
  theme: Accessor<Theme>;
  onSelect: () => void;
  panelFocus: Accessor<"sessions" | "agents">;
  focusedAgentIdx: Accessor<number>;
  focusedPaneId: Accessor<string | null>;
  onAgentDismiss: (agent: SessionData["agents"][number]) => void;
  onAgentFocus: (agent: SessionData["agents"][number]) => void;
}

function SessionCard(props: SessionCardProps) {
  const P = () => props.theme().palette;
  const nameStyle = () => {
    const base = tier(props.isSelected ? "primary" : "dim", P());
    const attentionFg = props.session.unseen ? P().teal : base.fg;
    return {
      ...base,
      fg: props.session.agentState?.status === "waiting"
        ? lerpHex(attentionFg, P().yellow, props.glowT())
        : attentionFg,
      attributes: (base.attributes ?? 0) | (props.isCurrent ? BOLD : 0),
    };
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

  const agentCount = () => props.session.agents?.filter(isLiveAgent).length ?? 0;

  // Locked count format (B1 / Q3): bare numeric, capped at "9+". The legacy
  // "●N" badge and the "2π" same-type compaction are both retired.
  const agentBadge = () => {
    const n = agentCount();
    if (n === 0) return "";
    if (n >= 10) return "9+";
    return String(n);
  };

  const agents = () => liveFirstAgents(props.session.agents ?? []);
  const visibleAgents = () => agents().slice(0, props.visibleAgentCount);
  const hiddenAgentCount = () => Math.max(0, agents().length - visibleAgents().length);
  const branchStyle = () => props.isSelected
    ? { fg: P().pink }
    : tier("dim", P());

  return (
    <box id={`session-${props.session.name}`} flexDirection="column" flexShrink={0}>
      <box
        flexDirection="row"
        backgroundColor="transparent"
        paddingLeft={1}
        onMouseDown={props.onSelect}
      >
        <box flexDirection="column" flexGrow={1} paddingRight={1}>
          <box flexDirection="row">
            <text wrapMode="none" style={nameStyle()}>
              <span style={nameStyle()}>{truncName()}</span>
              <Show when={agentBadge()}>
                <span style={tier(props.isSelected ? "secondary" : "dim", P())}>{" "}{agentBadge()}</span>
              </Show>
            </text>
            <box flexGrow={1} />
          </box>

          <Show when={props.session.branch}>
            <box flexDirection="row">
              <text wrapMode="none" style={branchStyle()}>
                <span style={branchStyle()}>
                  {BRANCH_GLYPH}{" "}{truncBranch()}
                </span>
              </text>
            </box>
          </Show>
        </box>
      </box>

      <box flexDirection="column" paddingLeft={1}>
        <For each={visibleAgents()}>
          {(agent, i) => (
            <AgentListItem
              agent={agent}
              palette={() => P()}
              spinIdx={props.spinIdx}
              glowT={props.glowT}
              isSessionSelected={props.isSelected}
              isKeyboardFocused={props.isSelected && props.panelFocus() === "agents" && i() === props.focusedAgentIdx()}
              isPaneFocused={agent.paneId != null && agent.paneId === props.focusedPaneId()}
              onDismiss={() => props.onAgentDismiss(agent)}
              onFocusPane={() => props.onAgentFocus(agent)}
            />
          )}
        </For>
        <Show when={hiddenAgentCount() > 0}>
          <text wrapMode="none" style={tier(props.isSelected ? "dim" : "muted", P())}>
            {"   +"}{hiddenAgentCount()} more
          </text>
        </Show>
      </box>
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
