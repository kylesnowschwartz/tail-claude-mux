// ─── Types ───────────────────────────────────────────────────────────────────
export type {
  MuxSessionInfo,
  ActiveWindow,
  SidebarPane,
  SidebarPosition,
  MuxProviderV1,
  WindowCapable,
  SidebarCapable,
  BatchCapable,
  FullMuxProvider,
  MuxProvider,
} from "./types";

// ─── Type guards ─────────────────────────────────────────────────────────────
export {
  isWindowCapable,
  isSidebarCapable,
  isBatchCapable,
  isFullSidebarCapable,
} from "./types";
