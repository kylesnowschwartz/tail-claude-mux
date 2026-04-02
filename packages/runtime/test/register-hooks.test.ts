import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { registerHooks } from "../src/setup/register-hooks";

describe("registerHooks", () => {
  let tmpDir: string;
  let settingsPath: string;
  let fakeOpensessionsDir: string;

  beforeEach(() => {
    tmpDir = join(tmpdir(), `register-hooks-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    const claudeDir = join(tmpDir, ".claude");
    mkdirSync(claudeDir, { recursive: true });
    settingsPath = join(claudeDir, "settings.json");

    fakeOpensessionsDir = join(tmpDir, "opensessions");
    mkdirSync(join(fakeOpensessionsDir, "scripts"), { recursive: true });
    writeFileSync(join(fakeOpensessionsDir, "scripts", "hook.sh"), "#!/bin/bash\n");
  });

  afterEach(() => {
    try { rmSync(tmpDir, { recursive: true }); } catch {}
  });

  test("creates settings.json with all hooks when file does not exist", () => {
    const added = registerHooks(fakeOpensessionsDir, settingsPath);

    expect(added).toEqual(["UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "Stop", "Notification"]);
    expect(existsSync(settingsPath)).toBe(true);

    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(Object.keys(settings.hooks)).toHaveLength(6);

    // Verify structure
    const hookScript = join(fakeOpensessionsDir, "scripts", "hook.sh");
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

    const added = registerHooks(fakeOpensessionsDir, settingsPath);

    expect(added).toHaveLength(6);
    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(settings.someOtherSetting).toBe(true);
    expect(settings.hooks.SomeOtherHook).toBeDefined();
    expect(Object.keys(settings.hooks)).toHaveLength(7); // 6 new + 1 existing
  });

  test("is idempotent — running twice registers nothing the second time", () => {
    const first = registerHooks(fakeOpensessionsDir, settingsPath);
    expect(first).toHaveLength(6);

    const second = registerHooks(fakeOpensessionsDir, settingsPath);
    expect(second).toHaveLength(0);

    // Verify no duplicate entries
    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(settings.hooks.UserPromptSubmit).toHaveLength(1);
  });

  test("creates backup before writing", () => {
    writeFileSync(settingsPath, JSON.stringify({ original: true }));

    registerHooks(fakeOpensessionsDir, settingsPath);

    expect(existsSync(`${settingsPath}.bak`)).toBe(true);
    const backup = JSON.parse(readFileSync(`${settingsPath}.bak`, "utf-8"));
    expect(backup.original).toBe(true);
  });
});
