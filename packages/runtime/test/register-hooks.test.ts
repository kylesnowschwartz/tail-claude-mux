import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { registerHooks } from "../src/setup/register-hooks";

describe("registerHooks", () => {
  let tmpDir: string;
  let settingsPath: string;
  let fakeTcmDir: string;

  beforeEach(() => {
    tmpDir = join(tmpdir(), `register-hooks-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    const claudeDir = join(tmpDir, ".claude");
    mkdirSync(claudeDir, { recursive: true });
    settingsPath = join(claudeDir, "settings.json");

    fakeTcmDir = join(tmpDir, "tcm");
    mkdirSync(join(fakeTcmDir, "scripts"), { recursive: true });
    writeFileSync(join(fakeTcmDir, "scripts", "hook.sh"), "#!/bin/bash\n");
  });

  afterEach(() => {
    try { rmSync(tmpDir, { recursive: true }); } catch {}
  });

  test("creates settings.json with all hooks when file does not exist", () => {
    const added = registerHooks(fakeTcmDir, settingsPath);

    expect(added).toEqual(["SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "Stop", "Notification", "SessionEnd"]);
    expect(existsSync(settingsPath)).toBe(true);

    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(Object.keys(settings.hooks)).toHaveLength(8);

    // Verify structure
    const hookScript = join(fakeTcmDir, "scripts", "hook.sh");
    const entry = settings.hooks.UserPromptSubmit[0];
    expect(entry.hooks[0].type).toBe("command");
    expect(entry.hooks[0].command).toBe(`${hookScript} UserPromptSubmit`);
    expect(entry.hooks[0].async).toBe(true);
  });

  test("preserves existing settings and hooks", () => {
    writeFileSync(settingsPath, JSON.stringify({
      someOtherSetting: true,
      hooks: {
        SomeOtherHook: [{ hooks: [{ type: "command", command: "other-tool" }] }],
      },
    }));

    const added = registerHooks(fakeTcmDir, settingsPath);

    expect(added).toHaveLength(8);
    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(settings.someOtherSetting).toBe(true);
    expect(settings.hooks.SomeOtherHook).toBeDefined();
    expect(Object.keys(settings.hooks)).toHaveLength(9); // 8 new + 1 existing
  });

  test("is idempotent — running twice registers nothing the second time", () => {
    const first = registerHooks(fakeTcmDir, settingsPath);
    expect(first).toHaveLength(8);

    const second = registerHooks(fakeTcmDir, settingsPath);
    expect(second).toHaveLength(0);

    // Verify no duplicate entries
    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(settings.hooks.UserPromptSubmit).toHaveLength(1);
  });

  test("creates backup before writing", () => {
    writeFileSync(settingsPath, JSON.stringify({ original: true }));

    registerHooks(fakeTcmDir, settingsPath);

    expect(existsSync(`${settingsPath}.bak`)).toBe(true);
    const backup = JSON.parse(readFileSync(`${settingsPath}.bak`, "utf-8"));
    expect(backup.original).toBe(true);
  });
});
