import { render } from "@opentui/solid";
import { appendFileSync } from "fs";
import { createSignal, createEffect, onCleanup, onMount, batch, For, Show, createMemo, createSelector, type Accessor } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { useKeyboard, useRenderer } from "@opentui/solid";
import { TextAttributes, type MouseEvent } from "@opentui/core";

import { ensureServer } from "@opensessions/core";
import {
  type ServerMessage,
  type SessionData,
  type ClientCommand,
  type Theme,
  SERVER_PORT,
  SERVER_HOST,
  BUILTIN_THEMES,
  loadConfig,
  resolveTheme,
  saveConfig,
} from "@opensessions/core";
import { TmuxClient } from "@opensessions/mux-tmux";

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

const SPINNERS = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const UNSEEN_ICON = "●";
const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;
const SPARK_BLOCKS = [" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"];

const THEME_NAMES = Object.keys(BUILTIN_THEMES);
const DEFAULT_DETAIL_PANEL_HEIGHT = 10;
const MIN_DETAIL_PANEL_HEIGHT = 4;
const RESIZE_DEBUG_LOG = "/tmp/opensessions-tui-resize.log";

function logResizeDebug(message: string, data?: Record<string, unknown>): void {
  const ts = new Date().toISOString();
  const extra = data ? ` ${JSON.stringify(data)}` : "";
  try {
    appendFileSync(RESIZE_DEBUG_LOG, `[${ts}] [pid:${process.pid}] ${message}${extra}\n`);
  } catch {}
}

function clampDetailPanelHeight(height: number): number {
  return Math.max(MIN_DETAIL_PANEL_HEIGHT, Math.round(height));
}

function getStoredDetailPanelHeight(sessionName: string): number {
  const stored = loadConfig().detailPanelHeights?.[sessionName];
  return typeof stored === "number" ? clampDetailPanelHeight(stored) : DEFAULT_DETAIL_PANEL_HEIGHT;
}

function persistDetailPanelHeight(sessionName: string, height: number): void {
  const config = loadConfig();
  saveConfig({
    detailPanelHeights: {
      ...(config.detailPanelHeights ?? {}),
      [sessionName]: clampDetailPanelHeight(height),
    },
  });
}

/** Refocus the main (non-sidebar) pane after TUI capability detection finishes.
 *  This must happen from the TUI process — doing it from start.sh races with
 *  capability query responses and leaks escape sequences to the main pane. */
function refocusMainPane() {
  const windowId = process.env.REFOCUS_WINDOW;
  if (muxCtx.type === "tmux" && windowId) {
    try {
      const r = Bun.spawnSync(
        ["tmux", "list-panes", "-t", windowId, "-F", "#{pane_id} #{pane_title}"],
        { stdout: "pipe", stderr: "pipe" },
      );
      const lines = r.stdout.toString().trim().split("\n");
      const main = lines.find((l) => !l.includes("opensessions"));
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
  const [detailPanelHeight, setDetailPanelHeight] = createSignal(DEFAULT_DETAIL_PANEL_HEIGHT);
  const [isDetailResizeHover, setIsDetailResizeHover] = createSignal(false);
  const [isDetailResizing, setIsDetailResizing] = createSignal(false);
  const detailPanelSessionName = createMemo(() => focusedSession() ?? mySession());

  // --- Modal state ---
  const [modal, setModal] = createSignal<"none" | "theme-picker" | "confirm-kill">("none");
  const [killTarget, setKillTarget] = createSignal<string | null>(null);

  const [clientTty, setClientTty] = createSignal(getClientTty());
  let ws: WebSocket | null = null;
  let startupFocusSynced = false;
  let detailResizeStartY = 0;
  let detailResizeStartHeight = DEFAULT_DETAIL_PANEL_HEIGHT;
  const startupSessionName = getLocalSessionName();

  function send(cmd: ClientCommand) {
    if (connected() && ws) ws.send(JSON.stringify(cmd));
  }

  function switchToSession(name: string) {
    send({ type: "mark-seen", name });
    // Route through server — it has authoritative client TTY from hooks
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
    const nextIdx = Math.max(0, Math.min(list.length - 1, currentIdx + delta));
    const next = list[nextIdx]?.name ?? null;

    if (!next || next === current) return;

    setFocusedSession(next);
    send({ type: "focus-session", name: next });
  }

  function applyTheme(themeName: string) {
    send({ type: "set-theme", theme: themeName });
  }

  function resizeDetailPanel(delta: -1 | 1) {
    const nextHeight = clampDetailPanelHeight(detailPanelHeight() + delta);
    if (nextHeight === detailPanelHeight()) return;

    setDetailPanelHeight(nextHeight);

    const sessionName = detailPanelSessionName();
    if (sessionName) {
      persistDetailPanelHeight(sessionName, nextHeight);
    }
  }

  function beginDetailResize(event: MouseEvent) {
    logResizeDebug("beginDetailResize", {
      button: event.button,
      x: event.x,
      y: event.y,
      currentHeight: detailPanelHeight(),
      session: detailPanelSessionName(),
      target: event.target?.id ?? null,
    });
    if (event.button !== 0) return;
    (renderer as any).setCapturedRenderable?.(event.target ?? undefined);
    detailResizeStartY = event.y;
    detailResizeStartHeight = detailPanelHeight();
    setIsDetailResizing(true);
    event.stopPropagation();
  }

  function handleDetailResizeDrag(event: MouseEvent) {
    logResizeDebug("handleDetailResizeDrag", {
      x: event.x,
      y: event.y,
      isResizing: isDetailResizing(),
      startY: detailResizeStartY,
      startHeight: detailResizeStartHeight,
      currentHeight: detailPanelHeight(),
      session: detailPanelSessionName(),
    });
    if (!isDetailResizing()) return;
    const delta = detailResizeStartY - event.y;
    const nextHeight = clampDetailPanelHeight(detailResizeStartHeight + delta);
    setDetailPanelHeight(nextHeight);
    logResizeDebug("handleDetailResizeDrag:applied", {
      delta,
      nextHeight,
      session: detailPanelSessionName(),
    });
    event.stopPropagation();
  }

  function endDetailResize(event?: MouseEvent) {
    logResizeDebug("endDetailResize", {
      x: event?.x,
      y: event?.y,
      isResizing: isDetailResizing(),
      currentHeight: detailPanelHeight(),
      session: detailPanelSessionName(),
      target: event?.target?.id ?? null,
    });
    if (!isDetailResizing()) return;
    (renderer as any).setCapturedRenderable?.(undefined);
    setIsDetailResizing(false);
    setIsDetailResizeHover(false);

    const sessionName = detailPanelSessionName();
    if (sessionName) {
      persistDetailPanelHeight(sessionName, detailPanelHeight());
      logResizeDebug("endDetailResize:persisted", {
        session: sessionName,
        height: detailPanelHeight(),
      });
    }

    event?.stopPropagation();
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
    });
  }

  onMount(() => {
    logResizeDebug("mount", {
      startupSessionName,
      localSessionName: getLocalSessionName(),
      muxType: muxCtx.type,
      tmuxPane: process.env.TMUX_PANE ?? null,
    });
    // Refocus the main pane once terminal capability detection finishes.
    // This avoids the race where start.sh refocuses too early and capability
    // responses leak as garbage text into the main pane.
    let refocused = false;
    const doRefocus = () => {
      if (refocused) return;
      refocused = true;
      refocusMainPane();
    };
    renderer.on("capabilities", doRefocus);
    // Fallback: if no capability response arrives within 2s, refocus anyway
    const refocusTimeout = setTimeout(doRefocus, 2000);
    onCleanup(() => {
      clearTimeout(refocusTimeout);
      renderer.removeListener("capabilities", doRefocus);
    });

    const socket = new WebSocket(`ws://${SERVER_HOST}:${SERVER_PORT}`);
    ws = socket;

    socket.onopen = () => {
      setConnected(true);
      const tty = clientTty();
      if (tty) send({ type: "identify", clientTty: tty });
      reIdentify();
    };

    socket.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data as string) as ServerMessage;
        let startupFocusToPublish: string | null = null;
        batch(() => {
          if (msg.type === "state") {
            const startupFocus = !startupFocusSynced
              && startupSessionName
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

            if (!startupFocusSynced && sessions.some((session) => session.name === msg.name)) {
              startupFocusSynced = true;
              setFocusedSession(msg.name);
              if (focusedSession() !== msg.name) {
                startupFocusToPublish = msg.name;
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
      renderer.destroy();
    };

    onCleanup(() => socket.close());

    // Listen for quit messages from server
    socket.addEventListener("message", (event) => {
      try {
        const msg = JSON.parse(event.data as string);
        if (msg.type === "quit") {
          if (ws) ws.close();
          renderer.destroy();
        }
      } catch {}
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

  createEffect(() => {
    const sessionName = detailPanelSessionName();
    if (!sessionName) return;
    const storedHeight = getStoredDetailPanelHeight(sessionName);
    logResizeDebug("loadStoredDetailPanelHeight", {
      session: sessionName,
      storedHeight,
    });
    setDetailPanelHeight(storedHeight);
  });

  createEffect(() => {
    logResizeDebug("detailPanelHeight:changed", {
      height: detailPanelHeight(),
      session: detailPanelSessionName(),
      isResizing: isDetailResizing(),
    });
  });

  useKeyboard((key) => {
    const currentModal = modal();

    // --- Theme picker modal handles its own keys ---
    if (currentModal === "theme-picker") {
      if (key.name === "escape" || key.name === "q") {
        setModal("none");
      }
      // Select component handles j/k/up/down/enter internally
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
        // Send quit to server — it will kill all sidebars and shut down
        send({ type: "quit" });
        break;
      case "escape":
        // Escape just closes this TUI locally (doesn't quit server)
        if (ws) ws.close();
        renderer.destroy();
        break;
      case "up":
      case "k":
        moveLocalFocus(-1);
        break;
      case "down":
      case "j":
        moveLocalFocus(1);
        break;
      case "left":
        resizeDetailPanel(-1);
        break;
      case "right":
        resizeDetailPanel(1);
        break;
      case "return": {
        const focused = focusedSession();
        if (focused) switchToSession(focused);
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
        break;
      case "t":
        setModal("theme-picker");
        break;
      case "u":
        send({ type: "show-all-sessions" });
        break;
      case "d": {
        const focused = focusedSession();
        if (focused) send({ type: "hide-session", name: focused });
        break;
      }
      case "x": {
        const focused = focusedSession();
        if (focused) {
          setKillTarget(focused);
          setModal("confirm-kill");
        }
        break;
      }
      case "n":
      case "c":
        createNewSession();
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

  const runningCount = createMemo(() =>
    sessions.filter((s) => s.agentState?.status === "running").length,
  );

  const errorCount = createMemo(() =>
    sessions.filter((s) => s.agentState?.status === "error").length,
  );

  const unseenCount = createMemo(() =>
    sessions.filter((s) => s.unseen).length,
  );

  const isFocused = createSelector(focusedSession);

  const focusedData = createMemo(() =>
    sessions.find((s) => s.name === focusedSession()) ?? null,
  );

  return (
    <box flexDirection="column" flexGrow={1} backgroundColor={P().crust}>
      {/* Header */}
      <box flexDirection="column" paddingLeft={1} paddingTop={1} paddingBottom={0} flexShrink={0}>
        <text>
          <span style={{ fg: P().overlay1 }}>{"  "}</span>
          <span style={{ fg: P().subtext0, attributes: BOLD }}>Sessions</span>
          <span style={{ fg: P().overlay0 }}>{" "}{String(sessions.length)}</span>
          {runningCount() > 0 ? <span style={{ fg: P().yellow }}>{" "}{"⚡"}{runningCount()}</span> : ""}
          {errorCount() > 0 ? <span style={{ fg: P().red }}>{" "}{"✗"}{errorCount()}</span> : ""}
          {unseenCount() > 0 ? <span style={{ fg: P().teal }}>{" "}{"●"}{unseenCount()}</span> : ""}
        </text>
      </box>

      {/* Session list */}
      <scrollbox flexGrow={1} flexShrink={1} paddingTop={1}>
        <For each={sessions}>
          {(session, i) => (
            <SessionCard
              session={session}
              index={i() + 1}
              isFocused={isFocused(session.name)}
              isCurrent={session.name === mySession()}
              spinIdx={spinIdx}
              theme={theme}
              statusColors={S}
              onSelect={() => {
                setFocusedSession(session.name);
                send({ type: "focus-session", name: session.name });
                switchToSession(session.name);
              }}
            />
          )}
        </For>
      </scrollbox>

      {/* Detail panel — focused session info, draggable height */}
      <Show when={focusedData()}>
        {(data) => (
          <scrollbox height={detailPanelHeight()} maxHeight={detailPanelHeight()} flexShrink={0}>
            <DetailPanel
              session={data()}
              theme={theme}
              statusColors={S}
              spinIdx={spinIdx}
              onDismissAgent={(agent) => {
                send({
                  type: "dismiss-agent",
                  session: data().name,
                  agent: agent.agent,
                  threadId: agent.threadId,
                });
              }}
              isResizeHover={isDetailResizeHover()}
              isResizing={isDetailResizing()}
              onResizeStart={beginDetailResize}
              onResizeDrag={handleDetailResizeDrag}
              onResizeEnd={endDetailResize}
              onResizeHoverChange={setIsDetailResizeHover}
            />
          </scrollbox>
        )}
      </Show>

      {/* Footer */}
      <box flexDirection="column" paddingLeft={1} paddingBottom={1} paddingTop={0} flexShrink={0}>
        <text style={{ fg: P().surface2 }}>{"─".repeat(26)}</text>
        <text>
          <span style={{ fg: P().overlay0 }}>{"  ⇥"}</span>
          <span style={{ fg: P().overlay1 }}>{" cycle  "}</span>
          <span style={{ fg: P().overlay0 }}>{"⏎"}</span>
          <span style={{ fg: P().overlay1 }}>{" go  "}</span>
          <span style={{ fg: P().overlay0 }}>{"d"}</span>
          <span style={{ fg: P().overlay1 }}>{" remove  "}</span>
          <span style={{ fg: P().overlay0 }}>{"u"}</span>
          <span style={{ fg: P().overlay1 }}>{" restore  "}</span>
          <span style={{ fg: P().overlay0 }}>{"x"}</span>
          <span style={{ fg: P().overlay1 }}>{" kill  "}</span>
          <span style={{ fg: P().overlay0 }}>{"t"}</span>
          <span style={{ fg: P().overlay1 }}>{" theme"}</span>
        </text>
      </box>

      {/* Theme picker overlay */}
      <Show when={modal() === "theme-picker"}>
        <ThemePicker
          palette={P}
          onSelect={(name) => {
            applyTheme(name);
            setModal("none");
          }}
          onClose={() => setModal("none")}
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
    </box>
  );
}

// --- Theme Picker ---

interface ThemePickerProps {
  palette: Accessor<Theme["palette"]>;
  onSelect: (name: string) => void;
  onClose: () => void;
}

function ThemePicker(props: ThemePickerProps) {
  const options = THEME_NAMES.map((name) => ({
    name,
    value: name,
  }));

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
        <text style={{ fg: props.palette().surface2 }}>{"─".repeat(26)}</text>
        <select
          options={options}
          onSelect={(_index, option) => {
            props.onSelect(option.value as string);
          }}
          focused
          height={14}
          selectedBackgroundColor={props.palette().surface0}
          selectedTextColor={props.palette().text}
        />
        <text style={{ fg: props.palette().overlay0 }}>
          <span style={{ attributes: DIM }}>esc</span>{" close"}
        </text>
      </box>
    </box>
  );
}

// --- Sparkline ---

function buildSparkline(timestamps: number[], width: number, windowMs: number = 30 * 60 * 1000): string {
  if (timestamps.length === 0 || width <= 0) return "";
  const now = Date.now();
  const start = now - windowMs;
  const bucketSize = windowMs / width;
  const buckets = new Array(width).fill(0);

  for (const ts of timestamps) {
    if (ts < start) continue;
    const idx = Math.min(width - 1, Math.floor((ts - start) / bucketSize));
    buckets[idx]++;
  }

  const max = Math.max(...buckets, 1);
  return buckets.map((count: number) => {
    const level = Math.round((count / max) * (SPARK_BLOCKS.length - 1));
    return SPARK_BLOCKS[level];
  }).join("");
}

// --- Detail Panel ---

interface DetailPanelProps {
  session: SessionData;
  theme: Accessor<Theme>;
  statusColors: Accessor<Theme["status"]>;
  spinIdx: Accessor<number>;
  onDismissAgent: (agent: SessionData["agents"][number]) => void;
  isResizeHover: boolean;
  isResizing: boolean;
  onResizeStart: (event: MouseEvent) => void;
  onResizeDrag: (event: MouseEvent) => void;
  onResizeEnd: (event?: MouseEvent) => void;
  onResizeHoverChange: (hovered: boolean) => void;
}

function DetailPanel(props: DetailPanelProps) {
  const P = () => props.theme().palette;

  const agents = () => props.session.agents ?? [];
  const hasAgents = () => agents().length > 0;
  const portRows = () => {
    const ports = props.session.ports ?? [];
    const rows: number[][] = [];

    for (let i = 0; i < ports.length; i += 3) {
      rows.push(ports.slice(i, i + 3));
    }

    return rows;
  };

  const truncDir = () => {
    const d = props.session.dir;
    if (!d) return "";
    const home = process.env.HOME ?? "";
    const short = home && d.startsWith(home) ? "~" + d.slice(home.length) : d;
    return short.length > 24 ? "…" + short.slice(short.length - 23) : short;
  };

  return (
    <box flexDirection="column" flexShrink={0} paddingLeft={1}>
      <text
        selectable={false}
        onMouseDown={(event) => {
          logResizeDebug("separator:onMouseDown", { x: event.x, y: event.y, button: event.button, session: props.session.name });
          event.preventDefault();
          props.onResizeStart(event);
        }}
        onMouseDrag={(event) => {
          logResizeDebug("separator:onMouseDrag", { x: event.x, y: event.y, button: event.button, session: props.session.name });
          event.preventDefault();
          props.onResizeDrag(event);
        }}
        onMouseDragEnd={(event) => {
          logResizeDebug("separator:onMouseDragEnd", { x: event.x, y: event.y, button: event.button, session: props.session.name });
          event.preventDefault();
          props.onResizeEnd(event);
        }}
        onMouseUp={(event) => {
          logResizeDebug("separator:onMouseUp", { x: event.x, y: event.y, button: event.button, session: props.session.name });
          event.preventDefault();
          props.onResizeEnd(event);
        }}
        onMouseOver={() => props.onResizeHoverChange(true)}
        onMouseOut={() => {
          if (!props.isResizing) props.onResizeHoverChange(false);
        }}
        style={{
          fg: props.isResizing
            ? P().blue
            : props.isResizeHover
              ? P().overlay1
              : P().surface2,
        }}
      >
        {"─".repeat(26)}
      </text>

      {/* Directory */}
      <text truncate>
        <span style={{ fg: P().overlay0, attributes: DIM }}>{truncDir()}</span>
      </text>

      {/* Listening ports */}
      <Show when={props.session.ports?.length}>
        <box height={1} />
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
      </Show>

      {/* Agent instances */}
      <Show when={hasAgents()}>
        <For each={agents()}>
          {(agent) => (
            <AgentListItem
              agent={agent}
              palette={P}
              statusColors={props.statusColors}
              spinIdx={props.spinIdx}
              onDismiss={() => props.onDismissAgent(agent)}
            />
          )}
        </For>
      </Show>
    </box>
  );
}

interface AgentListItemProps {
  agent: SessionData["agents"][number];
  palette: Accessor<Theme["palette"]>;
  statusColors: Accessor<Theme["status"]>;
  spinIdx: Accessor<number>;
  onDismiss: () => void;
}

function AgentListItem(props: AgentListItemProps) {
  const P = () => props.palette();
  const SC = () => props.statusColors();
  const [isDismissHover, setIsDismissHover] = createSignal(false);

  const isTerminal = () => ["done", "error", "interrupted"].includes(props.agent.status);
  const isUnseen = () => isTerminal() && props.agent.unseen === true;

  const icon = () => {
    if (isUnseen()) return UNSEEN_ICON;
    if (isTerminal()) return props.agent.status === "done" ? "✓" : props.agent.status === "error" ? "✗" : "⚠";
    if (props.agent.status === "running") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
    if (props.agent.status === "waiting") return "◉";
    return "○";
  };

  const color = () => {
    if (isTerminal()) {
      if (props.agent.status === "error") return P().red;
      if (props.agent.status === "interrupted") return P().peach;
      return isUnseen() ? P().teal : P().green;
    }
    return SC()[props.agent.status];
  };

  const statusText = () => {
    if (props.agent.status === "running") return "running";
    if (props.agent.status === "done") return "done";
    if (props.agent.status === "error") return "error";
    if (props.agent.status === "interrupted") return "stopped";
    if (props.agent.status === "waiting") return "waiting";
    return "";
  };

  return (
    <box flexDirection="column" flexShrink={0}>
      <box height={1} />
      <box flexDirection="row" paddingRight={1}>
        <text flexGrow={1} truncate>
          <span style={{ fg: color() }}>{icon()}</span>
          <span style={{ fg: P().subtext1 }}>{" "}{props.agent.agent}</span>
        </text>
        <Show when={!isTerminal() || !isUnseen()}>
          <text flexShrink={0}>
            <span style={{ fg: color(), attributes: DIM }}>{statusText()}</span>
          </text>
        </Show>
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
          <span style={{ fg: isDismissHover() ? P().red : P().overlay0 }}>{" ✕"}</span>
        </text>
      </box>
      <Show when={props.agent.threadName}>
        <text paddingLeft={2} paddingRight={1}>
          <span style={{ fg: isUnseen() ? color() : P().overlay0 }}>{props.agent.threadName}</span>
        </text>
      </Show>
    </box>
  );
}

// --- Session Card ---

interface SessionCardProps {
  session: SessionData;
  index: number;
  isFocused: boolean;
  isCurrent: boolean;
  spinIdx: Accessor<number>;
  theme: Accessor<Theme>;
  statusColors: Accessor<Theme["status"]>;
  onSelect: () => void;
}

function SessionCard(props: SessionCardProps) {
  const P = () => props.theme().palette;
  const SC = () => props.statusColors();

  const status = () => props.session.agentState?.status ?? "idle";
  const unseen = () => props.session.unseen;

  const isUnseenTerminal = () =>
    unseen() && ["done", "error", "interrupted"].includes(status());

  const accentColor = () => {
    if (isUnseenTerminal()) return unseenAccentColor();
    const s = status();
    if (s === "error") return P().red;
    if (s === "interrupted") return P().peach;
    if (s === "running") return P().yellow;
    if (s === "done") return P().green;
    if (props.isCurrent) return P().green;
    if (props.isFocused) return P().lavender;
    return "transparent";
  };

  const unseenAccentColor = () => {
    const s = status();
    if (s === "error") return P().red;
    if (s === "interrupted") return P().peach;
    return P().teal;
  };

  const statusIcon = () => {
    const s = status();
    if (s === "running") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
    if (isUnseenTerminal()) return UNSEEN_ICON;
    return "";
  };

  const statusColor = () => {
    if (isUnseenTerminal()) return unseenAccentColor();
    return SC()[status()];
  };

  const nameColor = () => {
    if (props.isFocused) return P().text;
    if (props.isCurrent) return P().subtext1;
    return P().subtext0;
  };

  const indexColor = () => {
    if (props.isFocused) return P().subtext0;
    return P().surface2;
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

  const bgColor = () => {
    if (props.isFocused) return P().surface0;
    return "transparent";
  };

  return (
    <box flexDirection="column" flexShrink={0}>
      <box
        flexDirection="row"
        backgroundColor={bgColor()}
        onMouseDown={props.onSelect}
        paddingLeft={1}
      >
        {/* Left accent — space-preserving, only colored for meaningful states */}
        <text style={{ fg: accentColor() }}>{accentColor() === "transparent" ? " " : "▌"}</text>

        {/* Index */}
        <box width={3} flexShrink={0}>
          <text style={{ fg: indexColor() }}>{String(props.index).padStart(2)}</text>
        </box>

        {/* Content */}
        <box flexDirection="column" flexGrow={1} paddingRight={1}>
          {/* Row 1: name + status */}
          <box flexDirection="row">
            <text truncate flexGrow={1}>
              <span style={{ fg: nameColor(), attributes: props.isFocused || props.isCurrent ? BOLD : undefined }}>
                {truncName()}
              </span>
            </text>
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
                  <span style={{ fg: props.isFocused ? P().pink : P().overlay0 }}>
                    {truncBranch()}
                  </span>
                </text>
              </Show>
              <Show when={portHint()}>
                <text flexShrink={0}>
                  <span style={{ fg: props.isFocused ? P().sky : P().overlay0 }}>
                    {props.session.branch ? " " : ""}
                    {portHint()}
                  </span>
                </text>
              </Show>
            </box>
          </Show>
        </box>
      </box>

      {/* Breathing room — 1 empty line between cards */}
      <box height={1} />
    </box>
  );
}

async function main() {
  await ensureServer();
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
