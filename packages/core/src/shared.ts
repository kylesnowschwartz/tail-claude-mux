import type { AgentStatus, AgentEvent } from "./contracts/agent";
import type { MuxSessionInfo } from "./contracts/mux";

export const SERVER_PORT = 7391;
export const SERVER_HOST = "127.0.0.1";
export const PID_FILE = "/tmp/opensessions.pid";
export const SERVER_IDLE_TIMEOUT_MS = 30_000;
export const STUCK_RUNNING_TIMEOUT_MS = 3 * 60 * 1000;
export const EVENTS_FILE = "/tmp/opensessions-events.jsonl";

export interface SessionData {
  name: string;
  createdAt: number;
  dir: string;
  branch: string;
  dirty: boolean;
  isWorktree: boolean;
  unseen: boolean;
  panes: number;
  windows: number;
  uptime: string;
  agentState: AgentEvent | null;
}

export interface ServerState {
  type: "state";
  sessions: SessionData[];
  focusedSession: string | null;
  currentSession: string | null;
  theme: string | undefined;
  sidebarWidth: number;
  ts: number;
}

export interface FocusUpdate {
  type: "focus";
  focusedSession: string | null;
  currentSession: string | null;
}

export interface ResizeNotify {
  type: "resize";
  width: number;
}

export interface QuitNotify {
  type: "quit";
}

export interface YourSession {
  type: "your-session";
  name: string;
  clientTty: string | null;
}

export interface ReIdentify {
  type: "re-identify";
}

export type ServerMessage = ServerState | FocusUpdate | ResizeNotify | QuitNotify | YourSession | ReIdentify;

export type ClientCommand =
  | { type: "switch-session"; name: string; clientTty?: string }
  | { type: "switch-index"; index: number }
  | { type: "new-session" }
  | { type: "kill-session"; name: string }
  | { type: "reorder-session"; name: string; delta: -1 | 1 }
  | { type: "refresh" }
  | { type: "move-focus"; delta: -1 | 1 }
  | { type: "focus-session"; name: string }
  | { type: "mark-seen"; name: string }
  | { type: "set-theme"; theme: string }
  | { type: "identify"; clientTty: string }
  | { type: "report-width"; width: number }
  | { type: "quit" }
  | { type: "identify-pane"; paneId: string; sessionName: string };

// Catppuccin Mocha palette
export const C = {
  blue: "#89b4fa",
  lavender: "#b4befe",
  pink: "#cba6f7",
  mauve: "#cba6f7",
  yellow: "#f9e2af",
  green: "#a6e3a1",
  red: "#f38ba8",
  peach: "#fab387",
  teal: "#94e2d5",
  sky: "#89dceb",
  text: "#cdd6f4",
  subtext0: "#a6adc8",
  subtext1: "#bac2de",
  overlay0: "#6c7086",
  overlay1: "#7f849c",
  surface0: "#313244",
  surface1: "#45475a",
  surface2: "#585b70",
  base: "#1e1e2e",
  mantle: "#181825",
  crust: "#11111b",
} as const;

export const STATUS_COLORS: Record<AgentStatus, string> = {
  idle: C.surface2,
  running: C.yellow,
  done: C.green,
  error: C.red,
  waiting: C.blue,
  interrupted: C.peach,
};

export const STATUS_ICONS: Record<AgentStatus, string> = {
  idle: "○",
  running: "●",
  done: "✓",
  error: "✗",
  waiting: "◉",
  interrupted: "⚠",
};
