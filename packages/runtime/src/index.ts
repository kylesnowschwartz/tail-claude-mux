export type {
  MuxProvider,
  MuxProviderV1,
  MuxSessionInfo,
  ActiveWindow,
  SidebarPane,
  SidebarPosition,
  WindowCapable,
  SidebarCapable,
  BatchCapable,
  FullMuxProvider,
} from "./contracts/mux";
export {
  isWindowCapable,
  isSidebarCapable,
  isBatchCapable,
  isFullSidebarCapable,
} from "./contracts/mux";
export type { AgentStatus, AgentLiveness, AgentEvent, PanePresenceInput } from "./contracts/agent";
export { TERMINAL_STATUSES } from "./contracts/agent";
export type { AgentWatcher, AgentWatcherContext, HookPayload, HookReceiver } from "./contracts/agent-watcher";
export { isHookReceiver } from "./contracts/agent-watcher";
export { AgentTracker } from "./agents/tracker";
export { ClaudeCodeHookAdapter, toolDescription } from "./agents/watchers/claude-code-hooks";
export { PiHookAdapter, piToolDescription } from "./agents/watchers/pi-hooks";
export { loadConfig, saveConfig } from "./config";
export type { TcmConfig } from "./config";
export { resolveTheme, BUILTIN_THEMES, DEFAULT_THEME } from "./themes";
export type { Theme, ThemePalette, PartialTheme } from "./themes";
export { startServer } from "./server/index";
export { STATUSLINE_LAST_WINDOW, STATUSLINE_SHELL } from "./server/tmux-header-sync";
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
