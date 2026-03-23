import { describe, test, expect } from "bun:test";
import { join } from "path";

const HOOK_SCRIPT = join(import.meta.dir, "../../../integrations/claude-code/opensessions-hook.sh");

function runHook(event: string, stdin?: string, env?: Record<string, string>): { stdout: string; stderr: string; exitCode: number } {
  const eventsFile = env?.OPENSESSIONS_EVENTS_FILE ?? `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
  const envPrefix = `OPENSESSIONS_URL="http://127.0.0.1:19999/event" OPENSESSIONS_EVENTS_FILE="${eventsFile}"`;

  const cmd = stdin
    ? `echo '${stdin.replace(/'/g, "'\\''")}' | ${envPrefix} bash "${HOOK_SCRIPT}" ${event}`
    : `${envPrefix} bash "${HOOK_SCRIPT}" ${event}`;

  const result = Bun.spawnSync(["bash", "-c", cmd], {
    stdout: "pipe",
    stderr: "pipe",
  });

  return {
    stdout: result.stdout.toString().trim(),
    stderr: result.stderr.toString().trim(),
    exitCode: result.exitCode,
  };
}

function readEventsFile(path: string): any[] {
  try {
    const { readFileSync } = require("fs");
    return readFileSync(path, "utf-8")
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line: string) => JSON.parse(line));
  } catch {
    return [];
  }
}

describe("Claude Code hook script", () => {
  test("prompt-submit maps to running", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    runHook("prompt-submit", undefined, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].agent).toBe("claude-code");
    expect(events[0].status).toBe("running");
    expect(typeof events[0].ts).toBe("number");
  });

  test("stop maps to idle", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    runHook("stop", undefined, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].status).toBe("idle");
  });

  test("post-tool-use maps to running", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    runHook("post-tool-use", undefined, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].status).toBe("running");
  });

  test("session-end maps to done", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    runHook("session-end", undefined, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].status).toBe("done");
  });

  test("notification with permission_prompt maps to waiting", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    const stdin = JSON.stringify({ notification_type: "permission_prompt" });
    runHook("notification", stdin, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].status).toBe("waiting");
  });

  test("notification with elicitation_dialog maps to waiting", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    const stdin = JSON.stringify({ notification_type: "elicitation_dialog" });
    runHook("notification", stdin, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].status).toBe("waiting");
  });

  test("notification with idle_prompt maps to idle", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    const stdin = JSON.stringify({ notification_type: "idle_prompt" });
    runHook("notification", stdin, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    expect(events[0].status).toBe("idle");
  });

  test("unknown event exits cleanly with no output", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    const result = runHook("unknown-event", undefined, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    expect(result.exitCode).toBe(0);
    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(0);
  });

  test("exits with 0 even when server is unreachable", () => {
    const result = runHook("prompt-submit");
    expect(result.exitCode).toBe(0);
  });

  test("payload includes session name from tmux", () => {
    const eventsFile = `/tmp/opensessions-hook-test-${Date.now()}.jsonl`;
    runHook("prompt-submit", undefined, { OPENSESSIONS_EVENTS_FILE: eventsFile });

    const events = readEventsFile(eventsFile);
    expect(events.length).toBe(1);
    // Session should be a non-empty string (tmux session name or "unknown")
    expect(typeof events[0].session).toBe("string");
    expect(events[0].session.length).toBeGreaterThan(0);
  });
});
