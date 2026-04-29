import { existsSync, readFileSync, writeFileSync, mkdirSync } from "fs";
import { join } from "path";

import type { PartialTheme } from "./themes";

export interface TcmConfig {
  /** Custom server port */
  port?: number;
  /** Theme: builtin name (e.g. "catppuccin-latte") or partial inline theme object */
  theme?: string | PartialTheme;
  /** Sidebar column width (default DEFAULT_SIDEBAR_WIDTH = 33; see sidebar-width-sync.ts) */
  sidebarWidth?: number;
  /** Sidebar position relative to the terminal window (default "left") */
  sidebarPosition?: "left" | "right";
  /** Persisted detail panel heights keyed by mux session name */
  detailPanelHeights?: Record<string, number>;
}

const DEFAULTS: TcmConfig = {};

/**
 * Load config from ~/.config/tcm/config.json
 * @param homeDir — override home directory (for testing)
 */
export function loadConfig(homeDir?: string): TcmConfig {
  const home = homeDir ?? process.env.HOME ?? process.env.USERPROFILE ?? "";
  const configPath = join(home, ".config", "tcm", "config.json");

  if (!existsSync(configPath)) {
    return { ...DEFAULTS };
  }

  try {
    const raw = readFileSync(configPath, "utf-8");
    const parsed = JSON.parse(raw) as Partial<TcmConfig>;
    return {
      ...DEFAULTS,
      ...parsed,
    };
  } catch {
    return { ...DEFAULTS };
  }
}

/**
 * Save partial config updates to ~/.config/tcm/config.json
 * Merges with existing config on disk to preserve fields.
 * @param updates — partial config fields to write
 * @param homeDir — override home directory (for testing)
 */
export function saveConfig(updates: Partial<TcmConfig>, homeDir?: string): void {
  const home = homeDir ?? process.env.HOME ?? process.env.USERPROFILE ?? "";
  const configDir = join(home, ".config", "tcm");
  const configPath = join(configDir, "config.json");

  const existing = loadConfig(homeDir);
  const merged = { ...existing, ...updates };

  mkdirSync(configDir, { recursive: true });
  writeFileSync(configPath, JSON.stringify(merged, null, 2) + "\n");
}
