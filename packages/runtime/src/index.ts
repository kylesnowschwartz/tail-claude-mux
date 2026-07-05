export type { AgentStatus, AgentLiveness, AgentEvent, PanePresenceInput } from "./contracts/agent";
export { TERMINAL_STATUSES } from "./contracts/agent";
export {
  sanitizeForDisplay,
  stringWidth,
  stripAnsiEscapes,
  stripNonPrintingControlChars,
  truncateToWidth,
} from "./text";
export { glowPhase, lerpHex } from "./glow";
export { resolveTheme, BUILTIN_THEMES, DEFAULT_THEME } from "./themes";
export type { Theme, ThemePalette, PartialTheme } from "./themes";
export { STATUSLINE_LAST_WINDOW, STATUSLINE_SHELL, AGENT_GLYPHS } from "./server/tmux-header-sync";
export { ensureServer } from "./server/launcher";
export {
  SERVER_PORT,
  SERVER_HOST,
  PID_FILE,
  SERVER_IDLE_TIMEOUT_MS,
  STUCK_RUNNING_TIMEOUT_MS,
  C,
} from "./shared";
export type {
  SessionData,
  ServerState,
  FocusUpdate,
  ResizeNotify,
  QuitNotify,
  ServerMessage,
  ClientCommand,
  MetadataTone,
  MetadataStatus,
  MetadataProgress,
  MetadataLogEntry,
  SessionMetadata,
} from "./shared";
