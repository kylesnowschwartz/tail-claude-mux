import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, rmSync, appendFileSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { ClaudeCodeAgentWatcher, determineStatus, isToolUseEntry } from "../src/agents/watchers/claude-code";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext } from "../src/contracts/agent-watcher";

// --- determineStatus ---

describe("Claude Code determineStatus", () => {
  // Control entries (no message) → null (skip)
  test("returns null for entry with no message (queue-operation)", () => {
    expect(determineStatus({ type: "queue-operation" })).toBeNull();
  });

  test("returns null for file-history-snapshot", () => {
    expect(determineStatus({ type: "file-history-snapshot" })).toBeNull();
  });

  test("returns null for last-prompt", () => {
    expect(determineStatus({ type: "last-prompt" })).toBeNull();
  });

  test("returns null for empty object", () => {
    expect(determineStatus({})).toBeNull();
  });

  // Assistant entries — streaming lifecycle
  test("returns running for assistant with thinking only", () => {
    expect(determineStatus({
      message: { role: "assistant", content: [{ type: "thinking" }] },
    })).toBe("running");
  });

  test("returns running for assistant with tool_use", () => {
    expect(determineStatus({
      message: { role: "assistant", content: [{ type: "tool_use" }] },
    })).toBe("running");
  });

  test("returns running for assistant streaming partial (stop_reason=null)", () => {
    expect(determineStatus({
      message: { role: "assistant", stop_reason: null, content: [{ type: "text", text: "partial" }] },
    })).toBe("running");
  });

  test("returns running for assistant text with no stop_reason (streaming)", () => {
    expect(determineStatus({
      message: { role: "assistant", content: [{ type: "text", text: "partial" }] },
    })).toBe("running");
  });

  test("returns done for assistant with stop_reason=end_turn", () => {
    expect(determineStatus({
      message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "done" }] },
    })).toBe("done");
  });

  test("returns running for assistant with stop_reason=tool_use", () => {
    expect(determineStatus({
      message: { role: "assistant", stop_reason: "tool_use", content: [{ type: "tool_use" }] },
    })).toBe("running");
  });

  test("returns done for assistant with string content and no stop_reason", () => {
    // String content (legacy format) with no stop_reason → running (streaming)
    expect(determineStatus({
      message: { role: "assistant", content: "thinking..." },
    })).toBe("running");
  });

  test("returns done for assistant with string content and stop_reason=end_turn", () => {
    expect(determineStatus({
      message: { role: "assistant", stop_reason: "end_turn", content: "final answer" },
    })).toBe("done");
  });

  // User entries — prompt, tool_result, interrupt, exit
  test("returns running for user text message", () => {
    expect(determineStatus({
      message: { role: "user", content: "hello" },
    })).toBe("running");
  });

  test("returns running for user tool_result", () => {
    expect(determineStatus({
      message: { role: "user", content: [{ type: "tool_result", text: "output" }] },
    })).toBe("running");
  });

  test("returns interrupted for user interrupt marker (Escape)", () => {
    expect(determineStatus({
      message: { role: "user", content: [{ type: "text", text: "[Request interrupted by user for tool use]" }] },
    })).toBe("interrupted");
  });

  test("returns interrupted for user interrupt marker (SIGINT)", () => {
    expect(determineStatus({
      message: { role: "user", content: [{ type: "text", text: "[Request interrupted by user]" }] },
    })).toBe("interrupted");
  });

  test("returns interrupted for user interrupt marker (string content)", () => {
    expect(determineStatus({
      message: { role: "user", content: "[Request interrupted by user]" },
    })).toBe("interrupted");
  });

  test("returns done for /exit command", () => {
    expect(determineStatus({
      message: { role: "user", content: "<command-name>/exit</command-name>             <command-message>Goodbye!</command-message>" },
    })).toBe("done");
  });

  test("returns null for /vim slash command (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<command-name>/vim</command-name>             <command-message>vim</command-message>" },
    })).toBeNull();
  });

  test("returns null for /clear slash command (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<command-name>/clear</command-name>             <command-message>clear</command-message>" },
    })).toBeNull();
  });

  test("returns null for /model slash command (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<command-name>/model</command-name>             <command-args>opus</command-args>" },
    })).toBeNull();
  });

  test("returns null for local-command-caveat (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<local-command-caveat>Caveat: The messages below were generated by the user while running a local command</local-command-caveat>" },
    })).toBeNull();
  });

  test("returns null for bash-input from ! command (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<bash-input>pwd</bash-input>" },
    })).toBeNull();
  });

  test("returns null for bash-stdout from ! command (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<bash-stdout>/Users/kyle/Code/dotfiles</bash-stdout><bash-stderr></bash-stderr>" },
    })).toBeNull();
  });

  test("returns null for bash-stderr from ! command (skip)", () => {
    expect(determineStatus({
      message: { role: "user", content: "<bash-stderr>fatal: not a git repository</bash-stderr>" },
    })).toBeNull();
  });
});

// --- isToolUseEntry ---

describe("Claude Code isToolUseEntry", () => {
  test("returns true for assistant with tool_use content", () => {
    expect(isToolUseEntry({
      message: { role: "assistant", content: [{ type: "tool_use" }] },
    })).toBe(true);
  });

  test("returns false for assistant with text only", () => {
    expect(isToolUseEntry({
      message: { role: "assistant", content: [{ type: "text", text: "hello" }] },
    })).toBe(false);
  });

  test("returns false for user message", () => {
    expect(isToolUseEntry({
      message: { role: "user", content: "hello" },
    })).toBe(false);
  });

  test("returns false for entry with no message", () => {
    expect(isToolUseEntry({})).toBe(false);
  });
});

// --- ClaudeCodeAgentWatcher integration ---

/**
 * decodeProjectDir returns "__encoded__:<name>" when the naive path doesn't
 * exist on disk (always the case in tests). resolveSession must handle both
 * the decoded path and the encoded fallback form.
 */
function resolveSession(dir: string): string | null {
  if (dir === "/projects/myapp" || dir === "__encoded__:-projects-myapp") return "myapp-session";
  return null;
}

describe("ClaudeCodeAgentWatcher", () => {
  let tmpDir: string;
  let watcher: ClaudeCodeAgentWatcher;
  let events: AgentEvent[];
  let ctx: AgentWatcherContext;

  beforeEach(() => {
    tmpDir = join(tmpdir(), `claude-watcher-test-${Date.now()}`);
    mkdirSync(tmpDir, { recursive: true });
    events = [];
    ctx = {
      resolveSession,
      emit: (event) => events.push(event),
    };
    watcher = new ClaudeCodeAgentWatcher();
    (watcher as any).projectsDir = tmpDir;
  });

  afterEach(() => {
    watcher.stop();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("emits event on file change after seed scan", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-001.jsonl");
    writeFileSync(filePath, JSON.stringify({ message: { role: "user", content: "initial" } }) + "\n");

    watcher.start(ctx);
    await watcher.flush(); // seed scan complete
    const seedCount = events.length;

    appendFileSync(filePath, JSON.stringify({ message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "I'll fix it" }] } }) + "\n");
    await watcher.flush();

    const postSeed = events.slice(seedCount);
    expect(postSeed.length).toBeGreaterThanOrEqual(1);
    expect(postSeed[0]!.agent).toBe("claude-code");
    expect(postSeed[0]!.session).toBe("myapp-session");
    expect(postSeed[0]!.status).toBe("done");
  });

  test("skips when session cannot be resolved", async () => {
    const projDir = join(tmpDir, "-unknown-project");
    mkdirSync(projDir, { recursive: true });

    writeFileSync(join(projDir, "session-002.jsonl"), "");

    watcher.start(ctx);
    await watcher.flush();

    appendFileSync(join(projDir, "session-002.jsonl"),
      JSON.stringify({ message: { role: "user", content: "hello" } }) + "\n");
    await watcher.flush();

    expect(events.length).toBe(0);
  });

  test("detects status transition after seed", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-003.jsonl");
    writeFileSync(filePath, JSON.stringify({ message: { role: "user", content: "start" } }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    appendFileSync(filePath, JSON.stringify({
      message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "done" }] },
    }) + "\n");
    await watcher.flush();

    const postSeed = events.slice(seedCount);
    const lastEvent = postSeed[postSeed.length - 1]!;
    expect(lastEvent.status).toBe("done");
  });

  test("promotes tool_use running to waiting after timeout", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-004.jsonl");
    writeFileSync(filePath, JSON.stringify({ message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "hi" }] } }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    // Append assistant tool_use — initially "running"
    appendFileSync(filePath, JSON.stringify({
      message: { role: "assistant", stop_reason: "tool_use", content: [{ type: "tool_use" }] },
    }) + "\n");
    await watcher.flush();

    const runningEvents = events.slice(seedCount).filter((e) => e.status === "running");
    expect(runningEvents.length).toBeGreaterThanOrEqual(1);

    // Backdate toolUseSeenAt to simulate TOOL_USE_WAIT_MS elapsed
    const state = (watcher as any).sessions.get("session-004");
    state.toolUseSeenAt = Date.now() - 4000; // past the 3s threshold

    // Next scan sees file unchanged + toolUseSeenAt expired → "waiting"
    await watcher.flush();

    const waitingEvents = events.slice(seedCount).filter((e) => e.status === "waiting");
    expect(waitingEvents.length).toBeGreaterThanOrEqual(1);
    expect(waitingEvents[0]!.agent).toBe("claude-code");
    expect(waitingEvents[0]!.session).toBe("myapp-session");
  });

  test("skips control entries without changing status", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-005.jsonl");
    writeFileSync(filePath, JSON.stringify({ message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "done" }] } }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    // Append control entries — should NOT change status from "done"
    appendFileSync(filePath, JSON.stringify({ type: "file-history-snapshot" }) + "\n");
    appendFileSync(filePath, JSON.stringify({ type: "queue-operation", operation: "enqueue" }) + "\n");
    await watcher.flush();

    // No new status events should have fired (status stayed "done")
    const postSeed = events.slice(seedCount);
    expect(postSeed.length).toBe(0);
  });

  test("detects interrupt marker as interrupted", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-006.jsonl");
    writeFileSync(filePath, JSON.stringify({
      message: { role: "assistant", stop_reason: "tool_use", content: [{ type: "tool_use" }] },
    }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    appendFileSync(filePath, JSON.stringify({
      message: { role: "user", content: [{ type: "tool_result", text: "aborted" }] },
    }) + "\n");
    appendFileSync(filePath, JSON.stringify({
      message: { role: "user", content: [{ type: "text", text: "[Request interrupted by user for tool use]" }] },
    }) + "\n");
    await watcher.flush();

    const postSeed = events.slice(seedCount);
    const lastEvent = postSeed[postSeed.length - 1]!;
    expect(lastEvent.status).toBe("interrupted");
  });

  test("detects /exit command as done", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-007.jsonl");
    writeFileSync(filePath, JSON.stringify({
      message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "done" }] },
    }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    appendFileSync(filePath, JSON.stringify({
      message: { role: "user", content: "<command-name>/exit</command-name>             <command-message>Goodbye!</command-message>" },
    }) + "\n");
    await watcher.flush();

    // Status should remain "done" (or re-emit "done")
    const postSeed = events.slice(seedCount);
    // The /exit returns "done", which is same as prev status, so may not emit
    // But importantly it should NOT be "running"
    const runningEvents = postSeed.filter((e) => e.status === "running");
    expect(runningEvents.length).toBe(0);
  });

  test("detects stuck running and promotes to done", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-008.jsonl");
    writeFileSync(filePath, JSON.stringify({
      message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "hi" }] },
    }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    // User sends message → running
    appendFileSync(filePath, JSON.stringify({
      message: { role: "user", content: "do something" },
    }) + "\n");
    await watcher.flush();

    const runningEvents = events.slice(seedCount).filter((e) => e.status === "running");
    expect(runningEvents.length).toBeGreaterThanOrEqual(1);

    // Backdate lastGrowthAt to simulate STUCK_RUNNING_MS elapsed
    const state = (watcher as any).sessions.get("session-008");
    state.lastGrowthAt = Date.now() - 16_000;

    // Next scan sees file unchanged + lastGrowthAt expired → "done"
    await watcher.flush();

    const doneEvents = events.slice(seedCount).filter((e) => e.status === "done");
    expect(doneEvents.length).toBeGreaterThanOrEqual(1);
  });

  test("keeps running during streaming partials (thinking -> text -> tool_use)", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-009.jsonl");
    writeFileSync(filePath, JSON.stringify({
      message: { role: "assistant", stop_reason: "end_turn", content: [{ type: "text", text: "hi" }] },
    }) + "\n");

    watcher.start(ctx);
    await watcher.flush();
    const seedCount = events.length;

    // Simulate a full streaming sequence in one batch
    appendFileSync(filePath, [
      JSON.stringify({ message: { role: "user", content: "read a file" } }),
      JSON.stringify({ message: { role: "assistant", stop_reason: null, content: [{ type: "thinking" }] } }),
      JSON.stringify({ message: { role: "assistant", stop_reason: null, content: [{ type: "text", text: "Let me read that" }] } }),
      JSON.stringify({ message: { role: "assistant", stop_reason: "tool_use", content: [{ type: "tool_use" }] } }),
    ].join("\n") + "\n");
    await watcher.flush();

    const postSeed = events.slice(seedCount);
    const doneEvents = postSeed.filter((e) => e.status === "done");
    expect(doneEvents.length).toBe(0);

    const runningEvents = postSeed.filter((e) => e.status === "running");
    expect(runningEvents.length).toBeGreaterThanOrEqual(1);
  });
});
