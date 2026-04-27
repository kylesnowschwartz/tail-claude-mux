import { describe, test, expect } from "bun:test";
import { join } from "path";
import { mkdirSync, rmSync, readFileSync } from "fs";
import { saveConfig, loadConfig } from "../src/config";

describe("saveConfig", () => {
  test("writes theme to config.json, preserving existing fields", async () => {
    const tmpDir = `/tmp/tcm-test-save-${Date.now()}`;
    const configDir = join(tmpDir, ".config", "tcm");
    mkdirSync(configDir, { recursive: true });
    await Bun.write(
      join(configDir, "config.json"),
      JSON.stringify({ mux: "tmux", plugins: ["some-plugin"] }),
    );

    saveConfig({ theme: "tokyo-night" }, tmpDir);

    const written = JSON.parse(readFileSync(join(configDir, "config.json"), "utf-8"));
    expect(written.theme).toBe("tokyo-night");
    expect(written.mux).toBe("tmux");
    expect(written.plugins).toEqual(["some-plugin"]);

    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("creates config.json if it does not exist", () => {
    const tmpDir = `/tmp/tcm-test-save-${Date.now()}`;

    saveConfig({ theme: "dracula" }, tmpDir);

    const configPath = join(tmpDir, ".config", "tcm", "config.json");
    const written = JSON.parse(readFileSync(configPath, "utf-8"));
    expect(written.theme).toBe("dracula");
    expect(written.plugins).toEqual([]);

    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("saveConfig then loadConfig roundtrips correctly", () => {
    const tmpDir = `/tmp/tcm-test-save-${Date.now()}`;

    saveConfig({ theme: "nord" }, tmpDir);
    const config = loadConfig(tmpDir);

    expect(config.theme).toBe("nord");

    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("preserves per-session detail panel heights across saves", () => {
    const tmpDir = `/tmp/tcm-test-save-${Date.now()}`;

    saveConfig({ detailPanelHeights: { alpha: 12 } }, tmpDir);
    saveConfig({ theme: "nord" }, tmpDir);

    const config = loadConfig(tmpDir);

    expect(config.detailPanelHeights).toEqual({ alpha: 12 });
    expect(config.theme).toBe("nord");

    rmSync(tmpDir, { recursive: true, force: true });
  });
});
