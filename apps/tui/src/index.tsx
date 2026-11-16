import { render } from "@opentui/solid";
import { createSignal, createEffect, onCleanup, onMount, batch, For, Show, createMemo, type Accessor } from "solid-js";
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
  LIAISON_WORKER_GLYPH,
  NEEDS_GLYPH,
  REASON_GLYPHS,
} from "./vocab";
import { tier, type TierStyle } from "./tiers";
import {
  findLiaisons,
  fleetCounts,
  formatAge,
  isLiaisonWorker,
  isLiveAgent,
  liaisonBlockRows,
  liaisonWorkerCount,
  needsSummary,
  needsYou,
  statusLabel,
  type AttentionReason,
  type SessionAgentRef,
  type StatusLabel,
} from "./attention";
import { BlockRule, LiaisonBlock, statusColor } from "./liaison-block";
import { conversationName, padAge, padReason, scrollWindow, showReasonWord } from "./needs-block";
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

/** Server port: `TCM_PORT` when set, as the repo's shell scripts honour it. */
const PORT = Number(process.env.TCM_PORT) || SERVER_PORT;

const SPINNERS = SEV_WORKING_SPINNER;
const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;
const THEME_NAMES = Object.keys(BUILTIN_THEMES);
const HEADER_ROWS = 2; // App header: one title row plus paddingTop={1}; keep in sync with its box.
const CARD_CHROME_ROWS = 3; // Session frame: rounded border top/bottom plus the name row.
const BRANCH_ROWS = 1; // SessionCard adds exactly one row when session.branch is present.
const HIDDEN_AGENTS_ROWS = 1; // SessionCard adds exactly one "+N more" row when agents are trimmed.
const OVERFLOW_INDICATOR_ROWS = 1; // "▲ N more" / "▼ N more" line when the session stack scrolls.
const NEEDS_HEADER_ROWS = 2; // "NEEDS YOU (n)" heading above the needs rows plus the closing rule below them.
const NEEDS_MIN_ROWS = 3; // The needs block always gets at least this many rows of the pane.

/** Blocking reasons (an open question to the engineer) read as more urgent. */
function isBlockingReason(reason: AttentionReason): boolean {
  return reason === "permission" || reason === "question";
}

function reasonColor(reason: AttentionReason, p: ThemePalette): string {
  if (reason === "permission") return p.peach;
  if (reason === "question") return p.yellow;
  if (reason === "input") return p.lavender;
  if (reason === "error") return p.red;
  return p.green;
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
 *  Uses the last 4 chars of the ID because multiple agents (Codex, OpenCode)
 *  produce IDs with deterministic *prefixes* (UUIDv7 timestamp, `ses_`
 *  sigil) while their random bits live at the tail. For Claude Code's
 *  UUIDv4 the distribution is uniform, so the tail is just as good as the
 *  head. */
function shortThreadId(id: string): string {
  return id.length <= 4 ? id : id.slice(-4);
}

type SessionAgent = SessionData["agents"][number];

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


function App() {
  const renderer = useRenderer();
  // Reactive terminal size — `renderer.terminalWidth` is a plain property and
  // goes stale after SIGWINCH; every layout computation must read this signal
  // instead so pane resizes re-flow the sidebar.
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

  // Wall clock for the liaison and needs ages; ticks only while either block shows.
  const [nowMs, setNowMs] = createSignal(Date.now());

  const liaisons = createMemo(() => findLiaisons(sessions));
  const needs = createMemo(() => needsYou(sessions));

  createEffect(() => {
    if (liaisons().length === 0 && needs().length === 0) return;
    setNowMs(Date.now());
    const interval = setInterval(() => setNowMs(Date.now()), 1000);
    onCleanup(() => clearInterval(interval));
  });

  const liaisonRows = createMemo(() =>
    liaisons().reduce((rows, { session, agent }, i) =>
      rows + liaisonBlockRows(i, !!session.metadata?.status?.text, liaisonWorkerCount(sessions, agent) > 0, needs().length > 0), 0));

  // The needs block takes up to half of what the header and liaison blocks
  // leave; past that it scrolls with its own ▲/▼ indicators.
  const [needsOffset, setNeedsOffset] = createSignal(0);
  const needsMaxRows = createMemo(() => {
    const free = termDims().height - HEADER_ROWS - liaisonRows() - NEEDS_HEADER_ROWS;
    return Math.max(NEEDS_MIN_ROWS, Math.floor(free / 2));
  });
  const needsWindow = createMemo(() => scrollWindow(needs().length, needsMaxRows(), needsOffset()));
  const needsBlockRows = createMemo(() => {
    if (needs().length === 0) return 0;
    const win = needsWindow();
    return NEEDS_HEADER_ROWS + (win.end - win.start)
      + (win.above > 0 ? OVERFLOW_INDICATOR_ROWS : 0) + (win.below > 0 ? OVERFLOW_INDICATOR_ROWS : 0);
  });
  type NeedsOverflow = { overflow: -1 | 1; count: number };
  const needsItems = createMemo<(SessionAgentRef | NeedsOverflow)[]>(() => {
    const win = needsWindow();
    const items: (SessionAgentRef | NeedsOverflow)[] = [];
    if (win.above > 0) items.push({ overflow: -1, count: win.above });
    items.push(...needs().slice(win.start, win.end));
    if (win.below > 0) items.push({ overflow: 1, count: win.below });
    return items;
  });
  function scrollNeeds(delta: -1 | 1) {
    const win = needsWindow();
    const page = Math.max(1, win.end - win.start);
    setNeedsOffset(Math.max(0, win.start + delta * page));
  }

  const listBudgetRows = createMemo(() =>
    Math.max(0, termDims().height - HEADER_ROWS - liaisonRows() - needsBlockRows()));

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

  function focusAgentPane(sessionName: string, agent: SessionAgent) {
    send({
      type: "focus-agent-pane",
      session: sessionName,
      agent: agent.agent,
      threadId: agent.threadId,
      threadName: agent.threadName,
      paneId: agent.paneId,
      clientTty: clientTty() || undefined,
    });
  }

  /** Jump to the conversation that has needed the engineer longest. */
  function jumpToOldestNeed() {
    const oldest = needs()[0];
    if (oldest) focusAgentPane(oldest.session.name, oldest.agent);
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
      const socket = new WebSocket(`ws://${SERVER_HOST}:${PORT}`);
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
      case "a":
        jumpToOldestNeed();
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

      {/* Liaison blocks: one per open liaison, pinned above everything else. */}
      <box flexDirection="column" flexShrink={0}>
        <For each={liaisons()}>
          {(ref, i) => (
            <LiaisonBlock
              liaison={ref}
              showFleet={i() === 0}
              palette={P}
              nowMs={nowMs}
              fleet={() => fleetCounts(sessions)}
              workers={() => liaisonWorkerCount(sessions, ref.agent)}
              width={() => termDims().width}
              needs={() => needsSummary(needs(), nowMs())}
              needsCount={() => needs().length}
            />
          )}
        </For>
      </box>

      {/* "Needs you": every conversation with an attention reason, oldest first. */}
      <box flexDirection="column" flexShrink={0}>
        <Show when={needs().length > 0}>
          <NeedsBlock
            items={needsItems}
            count={() => needs().length}
            palette={P}
            nowMs={nowMs}
            width={() => termDims().width}
            onScroll={scrollNeeds}
            onActivate={(ref) => focusAgentPane(ref.session.name, ref.agent)}
          />
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
                  liaisons={liaisons}
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
              ["a", "jump to oldest need"],
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
  /** An open liaison started this conversation; the row shows the worker mark before its name. */
  isLiaisonWorker: boolean;
  onDismiss: () => void;
  onFocusPane: () => void;
}

function AgentListItem(props: AgentListItemProps) {
  const P = () => props.palette();
  const [isDismissHover, setIsDismissHover] = createSignal(false);
  const [isFlash, setIsFlash] = createSignal(false);

  // Resolve the five-label scheme from tracker status + liveness
  const label = (): StatusLabel => statusLabel(props.agent);

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

  const color = () => statusColor(label(), P());

  const triggerFlash = () => {
    setIsFlash(true);
    setTimeout(() => setIsFlash(false), 150);
  };

  const bgColor = () => {
    if (isFlash()) return P().surface1;
    if (props.isKeyboardFocused) return P().surface0;
    // Focused-pane rows get the same row-bg treatment: the sky fg tint
    // alone is too subtle on light/transparent themes.
    if (props.isPaneFocused) return P().surface0;
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
  // Focused-pane marker: the row's whole identity (glyph, subagent, thread
  // name) tints sky. A colour survives the right-edge clip that used to eat
  // the trailing "•" on long thread names.
  const focusTinted = (base: TierStyle) => props.isPaneFocused
    ? { fg: P().sky }
    : base;

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
            <span style={focusTinted(nameStyle())}>{AGENT_GLYPHS[props.agent.agent] ?? props.agent.agent}</span>
            <Show when={props.agent.subagent}>
              <span style={focusTinted(tier(props.isSessionSelected ? "dim" : "muted", P()))}>{"  "}{props.agent.subagent}</span>
            </Show>
            {/* Session name from the agent-ouija registry when known;
                the 4-char uuid suffix is the fallback identity. */}
            <Show when={props.isLiaisonWorker}>
              <span style={{ fg: P().blue }}>{"  "}{LIAISON_WORKER_GLYPH}</span>
            </Show>
            <Show when={props.agent.threadName || props.agent.threadId}>
              <span style={focusTinted(supportingStyle())}>{props.isLiaisonWorker ? " " : "  "}{props.agent.threadName || shortThreadId(props.agent.threadId!)}</span>
            </Show>
          </text>
          <text flexShrink={0} style={{ fg: color() }}>
            <span style={{ fg: color() }}>{" "}{icon()}</span>
          </text>
        </box>
      </box>
    </box>
  );
}

// --- Needs You Block ---

interface NeedsBlockProps {
  items: Accessor<(SessionAgentRef | { overflow: -1 | 1; count: number })[]>;
  count: Accessor<number>;
  palette: Accessor<ThemePalette>;
  nowMs: Accessor<number>;
  width: Accessor<number>;
  onScroll: (delta: -1 | 1) => void;
  onActivate: (ref: SessionAgentRef) => void;
}

function NeedsBlock(props: NeedsBlockProps) {
  const P = () => props.palette();
  return (
    <box flexDirection="column" flexShrink={0}>
    <box flexDirection="column" flexShrink={0} paddingLeft={1} paddingRight={1}>
      <text wrapMode="none">
        <span style={{ fg: P().peach }}>{NEEDS_GLYPH}{" "}</span>
        <span style={{ fg: P().text, attributes: BOLD }}>{"NEEDS YOU ("}{props.count()}{")"}</span>
      </text>
      <For each={props.items()}>
        {(item) => {
          if ("overflow" in item) {
            return (
              <box paddingLeft={2} flexShrink={0} onMouseDown={() => props.onScroll(item.overflow)}>
                <text wrapMode="none" style={tier("muted", P())}>
                  {item.overflow < 0 ? "▲ " : "▼ "}{item.count}{" more"}
                </text>
              </box>
            );
          }
          const reason = () => item.agent.attention!;
          const fg = () => reasonColor(reason(), P());
          const age = () => padAge(formatAge(item.agent.attentionSince, props.nowMs()));
          return (
            <box flexDirection="row" flexShrink={0} onMouseDown={() => props.onActivate(item)}>
              <text flexGrow={1} wrapMode="none">
                <span style={{ fg: fg() }}>{" "}{REASON_GLYPHS[reason()]}{" "}</span>
                <span style={isBlockingReason(reason()) ? { fg: P().text, attributes: BOLD } : { fg: P().subtext1 }}>
                  {conversationName(item.agent)}
                </span>
              </text>
              <text flexShrink={0} wrapMode="none">
                <Show when={showReasonWord(props.width())}>
                  <span style={{ fg: fg() }}>{" "}{padReason(reason())}</span>
                </Show>
                <span style={tier("muted", P())}>{" "}{age()}</span>
              </text>
            </box>
          );
        }}
      </For>
    </box>
    <BlockRule palette={P} />
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
  /** Open liaisons; a row that one of them started shows the worker mark. */
  liaisons: Accessor<SessionAgentRef[]>;
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
              isLiaisonWorker={isLiaisonWorker(agent, props.liaisons())}
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
  // ensureServer only knows the default port; a sidebar pointed elsewhere
  // connects to whatever server already listens there.
  if (!mock) {
    if (PORT === SERVER_PORT) await ensureServer();
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
