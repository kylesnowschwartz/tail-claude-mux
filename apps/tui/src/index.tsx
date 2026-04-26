import { render } from "@opentui/solid";
import { appendFileSync } from "fs";
import { createSignal, createEffect, onCleanup, onMount, batch, For, Show, createMemo, type Accessor } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { useKeyboard, useRenderer } from "@opentui/solid";
import { TextAttributes, type InputRenderable, type KeyEvent } from "@opentui/core";

import { ensureServer } from "@opensessions/runtime";
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
} from "@opensessions/runtime";
import { TmuxClient } from "@opensessions/mux-tmux";
import {
  SEV_WORKING_SPINNER,
  SEV_WAITING,
  SEV_READY,
  SEV_STOPPED,
  SEV_ERROR,
  BRAND_CLAWD,
  BRANCH_GLYPH,
  WRAP_UP,
  WRAP_DOWN,
  ACTIVITY_LEAD,
  ACTIVITY_HEAD,
} from "./vocab";
import { tier, activityDescription } from "./tiers";
import { getScenario, listScenarios } from "./mocks/scenarios";

// Detect which mux we're running inside
type MuxContext =
  | { type: "tmux"; sdk: TmuxClient; paneId: string }
  | { type: "zellij"; sessionName: string; paneId: string }
  | { type: "none" };

function detectMuxContext(): MuxContext {
  if (process.env.TMUX_PANE && process.env.TMUX) {
    return { type: "tmux", sdk: new TmuxClient(), paneId: process.env.TMUX_PANE };
  }
  if (process.env.ZELLIJ_SESSION_NAME) {
    return {
      type: "zellij",
      sessionName: process.env.ZELLIJ_SESSION_NAME,
      paneId: process.env.ZELLIJ_PANE_ID ?? "",
    };
  }
  return { type: "none" };
}

const muxCtx = detectMuxContext();

const SPINNERS = SEV_WORKING_SPINNER;
const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;
const THEME_NAMES = Object.keys(BUILTIN_THEMES);

const TONE_ICONS: Record<MetadataTone, string> = {
  neutral: "·",
  info: "ℹ",
  success: "✓",
  warn: "⚠",
  error: "✗",
};

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
  const firstLine = raw.split("\n")[0];
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

/** Build an FZF_DEFAULT_OPTS --color string from an opensessions palette.
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
        ["tmux", "list-panes", "-t", windowId, "-F", "#{pane_id} #{pane_title}"],
        { stdout: "pipe", stderr: "pipe" },
      );
      const lines = r.stdout.toString().trim().split("\n");
      const main = lines.find((l) => !l.includes("opensessions-sidebar"));
      if (main) {
        const paneId = main.split(" ")[0];
        Bun.spawnSync(["tmux", "select-pane", "-t", paneId], { stdout: "pipe", stderr: "pipe" });
      }
    } catch {}
  } else if (muxCtx.type === "zellij") {
    // Zellij: move focus to the right (away from the sidebar on the left)
    try {
      Bun.spawnSync(["zellij", "action", "move-focus", "right"], { stdout: "pipe", stderr: "pipe" });
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
  // Zellij doesn't expose client TTY
  return "";
}

function parseMockFlag(): string | null {
  for (const arg of process.argv.slice(2)) {
    if (arg === "--mock") return "quiet";
    if (arg.startsWith("--mock=")) return arg.slice("--mock=".length);
  }
  return null;
}

function getLocalSessionName(): string | null {
  if (muxCtx.type === "tmux") {
    const sessionName = muxCtx.sdk.display("#{session_name}", { target: muxCtx.paneId });
    return sessionName || null;
  }

  if (muxCtx.type === "zellij") {
    return muxCtx.sessionName || null;
  }

  return null;
}

/**
 * Rolodex wrap rule — the split horizontal divider with a centred chevron
 * that marks where the rolodex visually wraps around the focused card.
 *
 * Renders as: `─────  ·  ─────` where the centre glyph is
 * \u{F0143} (chevron-up) above the focused card, \u{F0140} (chevron-down)
 * below it. See docs/design/04-mockups/02-canonical.md §"Locked decisions" #5.
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

/**
 * Detect a trailing outcome marker in an activity entry's description.
 *
 * Per docs/design/03-vocabulary.md §7 "Entry shape", the suffix `(passed)`
 * renders in green and `(failed)` in red. Everything else stays in the entry's
 * tier colour. The split lets us colour the suffix without touching the rest
 * of the description string.
 */
function splitOutcome(message: string): { main: string; outcome: { text: string; tone: "success" | "error" } | null } {
  const m = message.match(/^(.*?)(\s*)(\((passed|failed)\))\s*$/);
  if (!m) return { main: message, outcome: null };
  return {
    main: m[1] + m[2],
    outcome: { text: m[3]!, tone: m[4] === "passed" ? "success" : "error" },
  };
}

/**
 * Activity zone — fixed-height structural band beneath the rolodex.
 *
 * Source of truth: docs/design/04-mockups/02-canonical.md §"Activity zone
 * behaviour" and docs/design/03-vocabulary.md §7.
 *
 * Layout (always visible, never animates):
 *   ──────────────  ← top zone separator
 *   <focused-name> →  ← heading (Tier 2 + arrow-right Tier 4)
 *    cc 1859  editing tmux-header-sync.ts   ← freshest: Tier 2 italic
 *    cc 1859  ran  bun test (passed)        ← history: Tier 3 italic
 *    pi 15c8  awaiting input                ← history: Tier 3 italic
 *
 * Empty state: heading + `(no recent activity)` (Tier 4 muted placeholder).
 *
 * Source ordering: production accumulates logs newest-LAST (push). The mock
 * scenarios use newest-first for authoring convenience. We sort by `ts` desc
 * to render newest-at-top either way.
 */
function ActivityZone(props: {
  focusedSession: SessionData | null;
  palette: ThemePalette;
  paneFocused: boolean;
  cap: number;
}) {
  const heading = () => props.focusedSession?.name ?? "";
  const entries = () => {
    const logs = props.focusedSession?.metadata?.logs ?? [];
    if (logs.length === 0) return [];
    return [...logs].sort((a, b) => b.ts - a.ts).slice(0, props.cap);
  };
  const sepFg = () => props.paneFocused ? props.palette.overlay0 : props.palette.surface2;
  const headingStyle = () => tier("secondary", props.palette, props.paneFocused);
  const headingArrowStyle = () => tier("muted", props.palette, props.paneFocused);
  const placeholderStyle = () => tier("muted", props.palette, props.paneFocused);
  const leaderStyle = () => tier("muted", props.palette, props.paneFocused);
  const sourceStyle = () => tier("secondary", props.palette, props.paneFocused);

  return (
    <box flexDirection="column" flexShrink={0} paddingLeft={1} paddingRight={1}>
      {/* Top zone separator */}
      <box height={1}><text style={{ fg: sepFg() }}>{"─".repeat(200)}</text></box>

      {/* Heading: focused-session name + arrow-right separator */}
      <text truncate>
        <span style={headingStyle()}>{heading()}</span>
        <Show when={heading()}>
          <span style={headingArrowStyle()}>{" "}{ACTIVITY_HEAD}</span>
        </Show>
      </text>

      {/* Body: empty placeholder OR entries */}
      <Show when={entries().length > 0} fallback={
        <text truncate>
          <span style={placeholderStyle()}>{"(no recent activity)"}</span>
        </text>
      }>
        <For each={entries()}>
          {(entry, i) => {
            const isFreshest = i() === 0;
            const descStyle = activityDescription(props.palette, props.paneFocused, isFreshest);
            const sysSourceTone = (() => {
              if (!entry.source) return null;
              if (entry.source.startsWith("[") && entry.source.endsWith("]")) {
                return entry.tone ?? "info";
              }
              return null;
            })();
            const sourceCol = (entry.source ?? "").padEnd(10, " ").slice(0, 10);
            const split = splitOutcome(entry.message);
            return (
              <text truncate>
                <span style={leaderStyle()}>{ACTIVITY_LEAD}</span>
                <span style={sysSourceTone ? { fg: toneColor(sysSourceTone, props.palette) } : sourceStyle()}>{" "}{sourceCol}</span>
                <span style={descStyle}>{" "}{split.main}</span>
                <Show when={split.outcome}>
                  <span style={{ fg: toneColor(split.outcome!.tone, props.palette), attributes: descStyle.attributes }}>{split.outcome!.text}</span>
                </Show>
              </text>
            );
          }}
        </For>
      </Show>
    </box>
  );
}

function App() {
  const renderer = useRenderer();

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
  //
  // See docs/design/04-mockups/02-canonical.md locked decision #6 and the
  // 2026-04-28 dated update note for the design rationale (the earlier
  // wheel/rotation model disoriented users in live QA).
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
    const textWidth = Math.max(8, renderer.terminalWidth - 10);
    const wrapLines = (text: string) => Math.max(1, Math.ceil(text.length / textWidth));

    let max = 0;
    for (const session of sessions) {
      let h = 1; // row 1: name
      if (session.branch || (session.ports?.length ?? 0) > 0) h++; // row 2: branch/port

      // expanded content
      const { project, parent } = formatDir(session.dir);
      if (project && project !== session.name) {
        h++;
        if (parent) h++;
      }

      const ports = session.ports ?? [];
      if (ports.length > 0) h += Math.ceil(ports.length / 3);

      const agents = session.agents ?? [];
      for (const agent of agents) {
        h++; // agent row
        if (agent.threadName) {
          h += wrapLines(sanitizeThreadName(agent.threadName));
        }
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
    } else if (muxCtx.type === "zellij") {
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
    appendFileSync("/tmp/opensessions-tui-agent-click.log",
      `[${new Date().toISOString()}] keyboard focus-agent-pane session=${data.name} agent=${agent.agent} threadId=${agent.threadId} threadName=${agent.threadName}\n`);
    send({
      type: "focus-agent-pane",
      session: data.name,
      agent: agent.agent,
      threadId: agent.threadId,
      threadName: agent.threadName,
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
      env: { OPENSESSIONS_FZF_COLORS: paletteToFzfColors(P()) },
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

    // Refocus the main pane once terminal capability detection finishes.
    // This avoids the race where start.sh refocuses too early and capability
    // responses leak as garbage text into the main pane.
    let startupRefocused = false;
    const doStartupRefocus = () => {
      if (startupRefocused) return;
      startupRefocused = true;
      refocusMainPane();
    };
    renderer.on("capabilities", doStartupRefocus);
    // Fallback: if no capability response arrives within 2s, refocus anyway
    const refocusTimeout = setTimeout(doStartupRefocus, 2000);

    onCleanup(() => {
      clearTimeout(refocusTimeout);
      renderer.removeListener("capabilities", doStartupRefocus);
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
        flash("equalized");
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
  // in the panel redesign — the rolodex is the summary. See
  // docs/design/04-mockups/02-canonical.md §"Locked decisions" #4.

  return (
    <box flexDirection="column" flexGrow={1} backgroundColor={P().crust}>
      {/* Header */}
      <box flexDirection="column" paddingLeft={1} paddingTop={1} paddingBottom={0} flexShrink={0}>
        <text>
          <span style={{ fg: paneFocused() ? P().blue : P().overlay1 }}>{BRAND_CLAWD}{" "}</span>
          <span style={{ fg: paneFocused() ? P().text : P().overlay1, attributes: BOLD }}>opensessions</span>
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
                  theme={theme}
                  statusColors={S}
                  onSelect={() => {
                    setFocusedSession(session.name);
                    send({ type: "focus-session", name: session.name });
                    switchToSession(session.name);
                  }}
                  panelFocus={panelFocus}
                  focusedAgentIdx={focusedAgentIdx}
                  onAgentDismiss={(agent) => {
                    send({
                      type: "dismiss-agent",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                    });
                  }}
                  onAgentFocus={(agent) => {
                    appendFileSync("/tmp/opensessions-tui-agent-click.log",
                      `[${new Date().toISOString()}] sending focus-agent-pane session=${session.name} agent=${agent.agent} threadId=${agent.threadId} threadName=${agent.threadName}\n`);
                    send({
                      type: "focus-agent-pane",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                      threadName: agent.threadName,
                    });
                  }}
                />
              </>
            )}
          </For>
        </box>

        {/* Always-visible chevron wrap-rule above the focused card. */}
        <WrapRule direction="up" palette={P()} />

        {/* Focused session — bordered frame pinned at center */}
        <box border borderStyle="rounded" borderColor={paneFocused() ? P().blue : P().surface2} flexShrink={0} height={maxCardHeight()} overflow="hidden">
          <Show when={focusedData()}>
            {(data) => (
              <SessionCard
                session={data()}
                isFocused={true}
                isCurrent={data().name === currentSession()}
                paneFocused={paneFocused}
                spinIdx={spinIdx}
                theme={theme}
                statusColors={S}
                onSelect={() => switchToSession(data().name)}
                panelFocus={panelFocus}
                focusedAgentIdx={focusedAgentIdx}
                onAgentDismiss={(agent) => {
                  send({
                    type: "dismiss-agent",
                    session: data().name,
                    agent: agent.agent,
                    threadId: agent.threadId,
                  });
                }}
                onAgentFocus={(agent) => {
                  appendFileSync("/tmp/opensessions-tui-agent-click.log",
                    `[${new Date().toISOString()}] sending focus-agent-pane session=${data().name} agent=${agent.agent} threadId=${agent.threadId} threadName=${agent.threadName}\n`);
                  send({
                    type: "focus-agent-pane",
                    session: data().name,
                    agent: agent.agent,
                    threadId: agent.threadId,
                    threadName: agent.threadName,
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
                  theme={theme}
                  statusColors={S}
                  onSelect={() => {
                    setFocusedSession(session.name);
                    send({ type: "focus-session", name: session.name });
                    switchToSession(session.name);
                  }}
                  panelFocus={panelFocus}
                  focusedAgentIdx={focusedAgentIdx}
                  onAgentDismiss={(agent) => {
                    send({
                      type: "dismiss-agent",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                    });
                  }}
                  onAgentFocus={(agent) => {
                    appendFileSync("/tmp/opensessions-tui-agent-click.log",
                      `[${new Date().toISOString()}] sending focus-agent-pane session=${session.name} agent=${agent.agent} threadId=${agent.threadId} threadName=${agent.threadName}\n`);
                    send({
                      type: "focus-agent-pane",
                      session: session.name,
                      agent: agent.agent,
                      threadId: agent.threadId,
                      threadName: agent.threadName,
                    });
                  }}
                />
              </>
            )}
          </For>
        </box>
      </box>

      {/* Activity zone — fixed-height structural band below the rolodex. */}
      <ActivityZone
        focusedSession={focusedData()}
        palette={P()}
        paneFocused={paneFocused()}
        cap={renderer.terminalHeight < 30 ? 3 : 5}
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
              ["=", "equalize widths"],
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
  isKeyboardFocused: boolean;
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

  return (
    <box flexDirection="column" flexShrink={0} onMouseDown={() => {
      appendFileSync("/tmp/opensessions-tui-agent-click.log",
        `[${new Date().toISOString()}] clicked agent=${props.agent.agent} thread=${props.agent.threadName ?? "?"}\n`);
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
            <span style={{ fg: isDismissHover() ? P().red : P().overlay0 }}>{"✕ "}</span>
          </text>
          <text flexGrow={1} truncate>
            <span style={{
              fg: isUnseen()
                ? P().teal
                : (props.isKeyboardFocused ? P().text : P().subtext1),
              attributes: props.isKeyboardFocused ? BOLD : undefined,
            }}>{props.agent.agent}</span>
            <Show when={props.agent.threadId}>
              <span style={{ fg: P().overlay0, attributes: DIM }}>{" #"}{shortThreadId(props.agent.threadId!)}</span>
            </Show>
          </text>
          <text flexShrink={0}>
            <span style={{ fg: color() }}>{" "}{icon()}</span>
          </text>
        </box>

        {/* Row 2: live activity or thread name */}
        {(() => {
          const l = label();
          const showActivity = (l === "working" || l === "waiting") && props.agent.toolDescription;
          const previewText = showActivity
            ? props.agent.toolDescription!.slice(0, 60)
            : props.agent.threadName ? sanitizeThreadName(props.agent.threadName) : undefined;
          const previewColor = showActivity ? color() : (isUnseen() ? color() : P().overlay0);
          return (
            <Show when={previewText}>
              <text truncate>
                <span style={{ fg: previewColor, attributes: { italic: true } }}>{"· "}{previewText}</span>
              </text>
            </Show>
          );
        })()}
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
  theme: Accessor<Theme>;
  statusColors: Accessor<Theme["status"]>;
  onSelect: () => void;
  panelFocus: Accessor<"sessions" | "agents">;
  focusedAgentIdx: Accessor<number>;
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
  // See docs/design/04-mockups/02-canonical.md §"Locked decisions" #B5.
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
    // the retired ● glyph). See docs/design/03-vocabulary.md §4 "Unseen state".
    if (unseen()) return P().teal;
    if (props.isCurrent) return focused ? P().text : P().subtext0;
    return focused ? P().subtext1 : P().overlay1;
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

  const portHint = () => {
    const ports = props.session.ports ?? [];
    if (ports.length === 0) return "";
    if (ports.length === 1) return `⌁${ports[0]}`;
    return `⌁${ports[0]}+${ports.length - 1}`;
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
  // See docs/design/03-vocabulary.md §"Locked count format".
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
  // Note: status / progress / logs are now rendered in the standalone
  // ActivityZone component beneath the rolodex (per the canonical mockup).
  // The focused card body stays lean: name + branch + dir + agents only.
  const portRows = () => {
    const ports = props.session.ports ?? [];
    const maxPerRow = 3;
    const rows: number[][] = [];
    for (let i = 0; i < ports.length; i += maxPerRow) {
      rows.push(ports.slice(i, i + maxPerRow));
    }
    return rows;
  };
  const progressText = () => {
    const p = meta()?.progress;
    if (!p) return "";
    if (p.current != null && p.total != null) return `${p.current}/${p.total}`;
    if (p.percent != null) return `${Math.round(p.percent * 100)}%`;
    return "";
  };

  const currentIndicator = () => props.isCurrent ? "▎" : " ";
  const currentBarColor = () => props.paneFocused() ? P().blue : P().overlay0;

  return (
    <box id={`session-${props.session.name}`} flexDirection="column" flexShrink={0}>
      <box
        flexDirection="row"
        backgroundColor={bgColor()}
        onMouseDown={props.onSelect}
      >
        <text flexShrink={0}><span style={{ fg: currentBarColor() }}>{currentIndicator()}</span></text>
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
            <Show when={statusIcon()}>
              <text flexShrink={0}>
                <span style={{ fg: statusColor() }}>{" "}{statusIcon()}</span>
              </text>
            </Show>
          </box>

          {/* Row 2: branch plus a compact local-port hint when available */}
          <Show when={props.session.branch || portHint()}>
            <box flexDirection="row">
              <Show when={props.session.branch}>
                <text truncate flexGrow={1}>
                  <span style={{ fg: props.isFocused ? P().pink : (props.paneFocused() ? P().overlay0 : P().surface2) }}>
                    {BRANCH_GLYPH}{" "}{truncBranch()}
                  </span>
                </text>
              </Show>
              <Show when={portHint()}>
                <text flexShrink={0}>
                  <span style={{ fg: props.isFocused ? P().sky : (props.paneFocused() ? P().overlay0 : P().surface2) }}>
                    {props.session.branch ? " " : ""}
                    {portHint()}
                  </span>
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
          {/* Directory — only when cwd doesn't match session name */}
          <Show when={dirMismatch()}>
            <text truncate>
              <span style={{ fg: P().subtext0 }}>{dirParts().project}</span>
            </text>
            <Show when={dirParts().parent}>
              <text truncate>
                <span style={{ fg: P().overlay0, attributes: DIM }}>{"  "}{dirParts().parent}</span>
              </text>
            </Show>
          </Show>

          {/* Ports — full clickable list */}
          <Show when={props.session.ports?.length}>
            <box flexDirection="column" flexShrink={0}>
              <For each={portRows()}>
                {(ports, rowIndex) => (
                  <box flexDirection="row" paddingRight={1}>
                    <text flexShrink={0}>
                      <span style={{ fg: rowIndex() === 0 ? P().overlay0 : P().surface2, attributes: DIM }}>
                        {rowIndex() === 0 ? "local " : "      "}
                      </span>
                    </text>
                    <For each={ports}>
                      {(port, portIndex) => (
                        <box flexDirection="row" flexShrink={0}>
                          <text onMouseDown={() => {
                            Bun.spawn(["open", `http://localhost:${port}`], { stdout: "ignore", stderr: "ignore" });
                          }}>
                            <span style={{ fg: P().sky, attributes: BOLD }}>{String(port)}</span>
                          </text>
                          <Show when={portIndex() < ports.length - 1}>
                            <text>
                              <span style={{ fg: P().surface2 }}>{" · "}</span>
                            </text>
                          </Show>
                        </box>
                      )}
                    </For>
                  </box>
                )}
              </For>
            </box>
          </Show>

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
                    isKeyboardFocused={props.panelFocus() === "agents" && i() === props.focusedAgentIdx()}
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
    targetFPS: 30,
    useMouse: true,
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
