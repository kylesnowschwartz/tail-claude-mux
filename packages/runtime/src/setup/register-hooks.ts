/**
 * Idempotent registration of opensessions hooks in Claude Code's settings.json.
 *
 * Patches ~/.claude/settings.json to register lifecycle hooks that invoke
 * scripts/hook.sh for each supported event. Existing hooks with the same
 * command are skipped. Creates a .bak backup before writing.
 *
 * Claude Code settings.json hooks schema:
 *   "hooks": {
 *     "EventName": [{ "hooks": [{ "type": "command", "command": "..." }] }]
 *   }
 */

import { readFileSync, writeFileSync, copyFileSync, existsSync, mkdirSync } from "fs";
import { join, dirname } from "path";
import { homedir } from "os";

const HOOK_EVENTS = ["UserPromptSubmit", "PreToolUse", "PermissionRequest", "PostToolUse", "Stop", "Notification"] as const;

/**
 * Register opensessions hooks in Claude Code's settings.json.
 *
 * @param opensessionsDir - Root of the opensessions project (for locating hook.sh)
 * @param overrideSettingsPath - Override settings.json path (for testing)
 * @returns List of newly registered event names, or empty if all already present
 */
export function registerHooks(opensessionsDir: string, overrideSettingsPath?: string): string[] {
  const settingsPath = overrideSettingsPath ?? join(homedir(), ".claude", "settings.json");
  const hookScript = join(opensessionsDir, "scripts", "hook.sh");

  // Read existing settings (or start fresh)
  let settings: Record<string, any> = {};
  if (existsSync(settingsPath)) {
    try {
      settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    } catch {
      // Corrupted settings — start fresh but don't lose the file
      console.error(`Warning: could not parse ${settingsPath}, creating backup`);
    }
  } else {
    // Ensure directory exists
    const dir = dirname(settingsPath);
    if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
  }

  // Ensure hooks map exists
  if (!settings.hooks || typeof settings.hooks !== "object" || Array.isArray(settings.hooks)) {
    settings.hooks = {};
  }
  const hooksMap = settings.hooks as Record<string, any[]>;

  const added: string[] = [];

  for (const event of HOOK_EVENTS) {
    const command = `${hookScript} ${event}`;

    if (hasHookCommand(hooksMap, event, command)) continue;

    appendHook(hooksMap, event, command);
    added.push(event);
  }

  if (added.length === 0) return [];

  // Back up before writing
  if (existsSync(settingsPath)) {
    copyFileSync(settingsPath, `${settingsPath}.bak`);
  }

  writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + "\n");
  return added;
}

/** Check whether the hooks map already contains a matching command for the event. */
function hasHookCommand(hooksMap: Record<string, any[]>, event: string, command: string): boolean {
  const entries = hooksMap[event];
  if (!Array.isArray(entries)) return false;

  for (const entry of entries) {
    if (!entry || typeof entry !== "object" || !Array.isArray(entry.hooks)) continue;
    for (const hook of entry.hooks) {
      if (hook?.type === "command" && hook?.command === command) return true;
    }
  }
  return false;
}

/** Append a hook command to the event's entry array.
 *  Hooks are async (fire-and-forget) — they must never block the agent. */
function appendHook(hooksMap: Record<string, any[]>, event: string, command: string): void {
  if (!Array.isArray(hooksMap[event])) {
    hooksMap[event] = [];
  }

  hooksMap[event].push({
    hooks: [{ type: "command", command, async: true }],
  });
}
