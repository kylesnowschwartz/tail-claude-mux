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
import { TmuxClient } from "@opensessions/tmux-sdk";

const SPINNERS = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const UNSEEN_ICON = "●";
const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;

const THEME_NAMES = Object.keys(BUILTIN_THEMES);
const sdk = new TmuxClient();

function getClientTty(): string {
  // Initial best-effort — will be replaced by server-provided TTY from hooks
  const paneId = process.env.TMUX_PANE;
  if (paneId) {
    const sessName = sdk.display("#{session_name}", { target: paneId });
    if (sessName) {
      const clients = sdk.listClients();
      const client = clients.find((c) => c.sessionName === sessName);
      if (client) return client.tty;
    }
  }
  return sdk.getClientTty();
}

function App() {
  const renderer = useRenderer();

  // --- Theme state (driven by server) ---
  const [theme, setTheme] = createSignal<Theme>(resolveTheme(undefined));
  const P = () => theme().palette;
  const S = () => theme().status;

  const [sessions, setSessions] = createStore<SessionData[]>([]);
  const [focusedSession, setFocusedSession] = createSignal<string | null>(null);
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
    const paneId = process.env.TMUX_PANE;
    if (!paneId) return;
    // Re-query session (pane may have moved), server replies with your-session + clientTty
    const sessName = sdk.display("#{session_name}", { target: paneId });
    if (sessName) send({ type: "identify-pane", paneId, sessionName: sessName });
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
    const socket = new WebSocket(`ws://${SERVER_HOST}:${SERVER_PORT}`);
    ws = socket;

    socket.onopen = () => {
      setConnected(true);
      const tty = clientTty();
      if (tty) send({ type: "identify", clientTty: tty });
      const paneId = process.env.TMUX_PANE;
      if (paneId) {
        const sessName = sdk.display("#{session_name}", { target: paneId });
        if (sessName) send({ type: "identify-pane", paneId, sessionName: sessName });
      }
    };

    socket.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data as string) as ServerMessage;
        batch(() => {
          if (msg.type === "state") {
            setSessions(reconcile(msg.sessions, { key: "name" }));
            setFocusedSession(msg.focusedSession);
            setTheme(resolveTheme(msg.theme));
          } else if (msg.type === "focus") {
            setFocusedSession(msg.focusedSession);
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

  const unseenCount = createMemo(() =>
    sessions.filter((s) => s.unseen).length,
  );

  const isFocused = createSelector(focusedSession);

  return (
    <box flexDirection="column" flexGrow={1} backgroundColor={P().crust}>
      {/* Header */}
      <box flexDirection="column" paddingLeft={2} paddingTop={1} flexShrink={0}>
        <text>
          <span style={{ fg: P().blue, attributes: BOLD }}>⚡ Sessions</span>
          {"  "}
          <span style={{ fg: runningCount() > 0 ? P().text : P().overlay0 }}>{String(sessions.length)}</span>
          {runningCount() > 0 ? " " : ""}
          {runningCount() > 0 ? <span style={{ fg: P().yellow }}>{"⚡"}{runningCount()}</span> : ""}
          {unseenCount() > 0 ? " " : ""}
          {unseenCount() > 0 ? <span style={{ fg: P().teal }}>{"●"}{unseenCount()}</span> : ""}
        </text>
        <text style={{ fg: P().surface2 }}>{"─".repeat(22)}</text>
      </box>

      {/* Session list */}
      <scrollbox flexGrow={1}>
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

      {/* Footer */}
      <box flexDirection="column" paddingLeft={2} paddingBottom={1} flexShrink={0}>
        <text style={{ fg: P().surface2 }}>{"─".repeat(22)}</text>
        <text>
          <span style={{ fg: P().overlay0, attributes: DIM }}>⇥</span>
          {" "}
          <span style={{ fg: P().overlay1 }}>cycle</span>
          {"  "}
          <span style={{ fg: P().overlay0, attributes: DIM }}>1-9</span>
          {" "}
          <span style={{ fg: P().overlay1 }}>jump</span>
          {"  "}
          <span style={{ fg: P().overlay0, attributes: DIM }}>⏎</span>
          {" "}
          <span style={{ fg: P().overlay1 }}>go</span>
          {"  "}
          <span style={{ fg: P().overlay0, attributes: DIM }}>t</span>
          {" "}
          <span style={{ fg: P().overlay1 }}>theme</span>
          {"  "}
          <span style={{ fg: P().overlay0, attributes: DIM }}>q</span>
          {" "}
          <span style={{ fg: P().overlay1 }}>quit</span>
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
    if (s === "running") return P().yellow;
    if (props.isCurrent) return P().green;
    if (props.isFocused) return P().blue;
    return P().crust;
  };

  const unseenAccentColor = () => {
    const s = status();
    if (s === "error") return P().red;
    if (s === "interrupted") return P().peach;
    return P().teal;
  };

  const statusIcon = () => {
    if (isUnseenTerminal()) return UNSEEN_ICON;
    const s = status();
    if (s === "running") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
    return "";
  };

  const statusColor = () => {
    if (isUnseenTerminal()) return unseenAccentColor();
    const s = status();
    if (s === "running") return SC()[s];
    return "";
  };

  const nameColor = () =>
    props.isFocused ? P().text : props.isCurrent ? P().subtext1 : P().subtext0;

  const truncName = () => {
    const n = props.session.name;
    return n.length > 20 ? n.slice(0, 19) + "…" : n;
  };

  const truncBranch = () => {
    const b = props.session.branch;
    if (!b) return "";
    return b.length > 17 ? b.slice(0, 16) + "…" : b;
  };

  return (
    <box flexDirection="column">
      <box
        flexDirection="row"
        flexShrink={0}
        backgroundColor={props.isFocused ? P().surface0 : "transparent"}
        paddingTop={1}
        paddingBottom={1}
        onMouseDown={props.onSelect}
      >
        {/* Left accent bar */}
        <text style={{ fg: accentColor() }}>▎</text>

        {/* Index column */}
        <box width={2} flexShrink={0}>
          <text style={{ fg: props.isFocused ? P().overlay1 : P().surface2, attributes: DIM }}>{props.index}</text>
        </box>

        {/* Content column */}
        <box flexDirection="column" flexGrow={1} paddingRight={1}>
          {/* Row 1: name + spinner */}
          <box flexDirection="row">
            <text truncate flexGrow={1}>
              {props.isFocused || props.isCurrent
                ? <span style={{ fg: nameColor(), attributes: BOLD }}>{truncName()}</span>
                : <span style={{ fg: nameColor() }}>{truncName()}</span>}
            </text>
            <Show when={statusIcon()}>
              <text flexShrink={0}><span style={{ fg: statusColor() }}>{statusIcon()}</span></text>
            </Show>
          </box>

          {/* Row 2: branch */}
          <Show when={props.session.branch}>
            <text truncate>
              <span style={{ fg: P().pink }}>{truncBranch()}</span>
            </text>
          </Show>
        </box>
      </box>
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
