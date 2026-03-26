import { render } from "@opentui/solid";
import { createSignal, createEffect, onCleanup, onMount, batch, For, Show, createMemo, createSelector, type Accessor } from "solid-js";
import { createStore, reconcile } from "solid-js/store";
import { useKeyboard, useRenderer } from "@opentui/solid";
import { TextAttributes } from "@opentui/core";

import { ensureServer } from "@opensessions/core";
import {
  type ServerMessage,
  type SessionData,
  type ClientCommand,
  type Theme,
  SERVER_PORT,
  SERVER_HOST,
  BUILTIN_THEMES,
  resolveTheme,
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

  // --- Modal state ---
  const [modal, setModal] = createSignal<"none" | "theme-picker" | "confirm-kill">("none");
  const [killTarget, setKillTarget] = createSignal<string | null>(null);

  const [clientTty, setClientTty] = createSignal(getClientTty());
  let ws: WebSocket | null = null;

  function send(cmd: ClientCommand) {
    if (connected() && ws) ws.send(JSON.stringify(cmd));
  }

  function switchToSession(name: string) {
    send({ type: "mark-seen", name });
    // Route through server — it has authoritative client TTY from hooks
    send({ type: "switch-session", name });
  }

  function reIdentify() {
    if (muxCtx.type === "tmux") {
      const { sdk, paneId } = muxCtx;
      const sessName = sdk.display("#{session_name}", { target: paneId });
      if (sessName) send({ type: "identify-pane", paneId, sessionName: sessName });
    } else if (muxCtx.type === "zellij") {
      const sessName = process.env.ZELLIJ_SESSION_NAME;
      if (sessName) send({ type: "identify-pane", paneId: muxCtx.paneId, sessionName: sessName });
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

  function spawnSessionizer() {
    if (muxCtx.type !== "tmux") return; // Only works in tmux for now
    renderer.destroy();
    const proc = Bun.spawnSync(["/usr/local/bin/tmux-sessionizer"], {
      stdin: "inherit",
      stdout: "inherit",
      stderr: "inherit",
    });
    // Re-launch TUI after sessionizer exits
    render(() => <App />, {
      exitOnCtrlC: true,
      targetFPS: 30,
      useMouse: true,
    });
  }

  onMount(() => {
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
      if (muxCtx.type === "tmux") {
        const { sdk, paneId } = muxCtx;
        const sessName = sdk.display("#{session_name}", { target: paneId });
        if (sessName) send({ type: "identify-pane", paneId, sessionName: sessName });
      } else if (muxCtx.type === "zellij") {
        const sessName = process.env.ZELLIJ_SESSION_NAME;
        if (sessName) send({ type: "identify-pane", paneId: muxCtx.paneId, sessionName: sessName });
      }
    };

    socket.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data as string) as ServerMessage;
        batch(() => {
          if (msg.type === "state") {
            setSessions(reconcile(msg.sessions, { key: "name" }));
            setFocusedSession(msg.focusedSession);
            setCurrentSession(msg.currentSession);
            setTheme(resolveTheme(msg.theme));
          } else if (msg.type === "focus") {
            setFocusedSession(msg.focusedSession);
            setCurrentSession(msg.currentSession);
          } else if (msg.type === "your-session") {
            setMySession(msg.name);
            if (msg.clientTty) setClientTty(msg.clientTty);
          } else if (msg.type === "re-identify") {
            reIdentify();
          }
        });
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
      case "d":
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
        spawnSessionizer();
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
      <scrollbox flexGrow={1} paddingTop={1}>
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

      {/* Detail panel — focused session info */}
      <Show when={focusedData()}>
        {(data) => (
          <DetailPanel session={data()} theme={theme} statusColors={S} spinIdx={spinIdx} />
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
}

function DetailPanel(props: DetailPanelProps) {
  const P = () => props.theme().palette;
  const SC = () => props.statusColors();

  const agents = () => props.session.agents ?? [];
  const hasAgents = () => agents().length > 0;

  const sparkline = () => buildSparkline(props.session.eventTimestamps ?? [], 20);
  const hasActivity = () => (props.session.eventTimestamps ?? []).length >= 3;

  const truncDir = () => {
    const d = props.session.dir;
    if (!d) return "";
    const home = process.env.HOME ?? "";
    const short = home && d.startsWith(home) ? "~" + d.slice(home.length) : d;
    return short.length > 24 ? "…" + short.slice(short.length - 23) : short;
  };

  const truncThread = (name?: string) => {
    if (!name) return "";
    return name.length > 24 ? name.slice(0, 23) + "…" : name;
  };

  const statusLabel = (status: string): string => {
    if (status === "running") return "running";
    if (status === "done") return "done";
    if (status === "error") return "error";
    if (status === "interrupted") return "stopped";
    if (status === "waiting") return "waiting";
    return "";
  };

  return (
    <box flexDirection="column" flexShrink={0} paddingLeft={1}>
      <text style={{ fg: P().surface2 }}>{"─".repeat(26)}</text>

      {/* Directory */}
      <text truncate>
        <span style={{ fg: P().overlay0, attributes: DIM }}>{"  "}{truncDir()}</span>
      </text>

      {/* Agent instances — two lines per agent for clarity */}
      <Show when={hasAgents()}>
        <box height={1} />
        <For each={agents()}>
          {(agent) => {
            const icon = () => {
              if (agent.status === "running") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
              if (agent.status === "error") return "✗";
              if (agent.status === "done") return "✓";
              if (agent.status === "interrupted") return "⚠";
              if (agent.status === "waiting") return "◉";
              return "○";
            };
            const color = () => SC()[agent.status];
            return (
              <box flexDirection="column" flexShrink={0}>
                {/* Line 1: icon + agent name + status label */}
                <text truncate>
                  <span style={{ fg: color() }}>{"  "}{icon()}</span>
                  <span style={{ fg: P().subtext1 }}>{" "}{agent.agent}</span>
                  <span style={{ fg: color(), attributes: DIM }}>{" "}{statusLabel(agent.status)}</span>
                </text>
                {/* Line 2: thread name, indented */}
                {agent.threadName
                  ? <text truncate>
                      <span style={{ fg: P().overlay0, attributes: DIM }}>{"   "}{truncThread(agent.threadName)}</span>
                    </text>
                  : ""}
              </box>
            );
          }}
        </For>
      </Show>

      {/* Sparkline — activity over last 30m */}
      <Show when={hasActivity()}>
        <box height={1} />
        <text truncate>
          <span style={{ fg: P().lavender, attributes: DIM }}>{"  "}{sparkline()}</span>
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

          {/* Row 2: branch (only if exists) */}
          <Show when={props.session.branch}>
            <text truncate>
              <span style={{ fg: props.isFocused ? P().pink : P().overlay0 }}>
                {truncBranch()}
              </span>
            </text>
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
