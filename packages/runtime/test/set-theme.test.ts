import { describe, test, expect } from "bun:test";
import { rmSync } from "fs";
import type { ClientCommand, ServerState } from "../src/shared";
import { resolveTheme } from "../src/themes";
import { saveConfig, loadConfig } from "../src/config";

describe("set-theme command", () => {
  test("ClientCommand union accepts set-theme type", () => {
    const cmd: ClientCommand = { type: "set-theme", theme: "tokyo-night" };
    expect(cmd.type).toBe("set-theme");
    expect(cmd).toHaveProperty("theme", "tokyo-night");
  });

  test("ServerState includes theme field", () => {
    const state: ServerState = {
      type: "state",
      sessions: [],
      focusedSession: null,
      currentSession: null,
      theme: "dracula",
      sidebarWidth: 26,
      ts: Date.now(),
    };
    expect(state.theme).toBe("dracula");
  });
  test("ServerState.theme accepts a PartialTheme so the panel client can", () => {
    // Regression for the bug where the server only shipped the theme name.
    // External themes (e.g. the-themer's opensessions adapter) have names
    // that aren't in BUILTIN_THEMES; resolveTheme(name) would fall through to
    // catppuccin-mocha, leaving the panel dark under light themes. Shipping
    // the PartialTheme directly lets resolveTheme() merge the palette over
    // the default builtin.
    const state: ServerState = {
      type: "state",
      sessions: [],
      focusedSession: null,
      currentSession: null,
      theme: {
        name: "tekapo-sunset-light",
        variant: "light",
        palette: { text: "#1a1e26", base: "#ede3e0", blue: "#416895" },
      },
      sidebarWidth: 26,
      ts: Date.now(),
    };
    // Type assertion succeeds at compile time. Runtime check is incidental.
    expect(typeof state.theme).toBe("object");
    expect((state.theme as { palette?: { base?: string } }).palette?.base).toBe("#ede3e0");
  });

  test("set-theme persists to config and roundtrips", () => {
    const tmpDir = `/tmp/opensessions-test-theme-${Date.now()}`;
    saveConfig({ theme: "nord" }, tmpDir);
    const config = loadConfig(tmpDir);
    expect(config.theme).toBe("nord");
    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("set-theme with new theme resolves correctly", () => {
    const theme = resolveTheme("matrix");
    expect(theme.palette.text).toBe("#62ff94"); // matrix green text
  });
});
