import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, rmSync, appendFileSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { ClaudeCodeAgentWatcher, determineStatus, isToolUseEntry } from "../src/agents/watchers/claude-code";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext } from "../src/contracts/agent-watcher";

// --- determineStatus ---

describe("Claude Code determineStatus", () => {
  test("returns idle for entry with no message", () => {
    expect(determineStatus({})).toBe("idle");
  });

  test("returns running for assistant with tool_use", () => {
    expect(determineStatus({
      message: { role: "assistant", content: [{ type: "tool_use" }] },
    })).toBe("running");
  });

  test("returns done for assistant with text only", () => {
    expect(determineStatus({
      message: { role: "assistant", content: [{ type: "text", text: "hello" }] },
    })).toBe("done");
  });

  test("returns running for user message", () => {
    expect(determineStatus({
      message: { role: "user", content: "hello" },
    })).toBe("running");
  });

  test("returns done for assistant with string content", () => {
    expect(determineStatus({
      message: { role: "assistant", content: "thinking..." },
    })).toBe("done");
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
      resolveSession: (dir) => dir === "/projects/myapp" ? "myapp-session" : null,
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

    // Create file before watcher starts — seed scan records size
    const filePath = join(projDir, "session-001.jsonl");
    writeFileSync(filePath, JSON.stringify({ message: { role: "user", content: "initial" } }) + "\n");

    watcher.start(ctx);
    // Wait for seed scan
    await new Promise((r) => setTimeout(r, 200));
    const seedCount = events.length; // Seed emits for non-idle sessions

    // Append assistant response — triggers status change (running → done)
    appendFileSync(filePath, JSON.stringify({ message: { role: "assistant", content: [{ type: "text", text: "I'll fix it" }] } }) + "\n");
    await new Promise((r) => setTimeout(r, 2500));

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
    await new Promise((r) => setTimeout(r, 200));

    appendFileSync(join(projDir, "session-002.jsonl"),
      JSON.stringify({ message: { role: "user", content: "hello" } }) + "\n");
    await new Promise((r) => setTimeout(r, 2500));

    expect(events.length).toBe(0);
  });

  test("detects status transition after seed", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-003.jsonl");
    // Seed with a user message
    writeFileSync(filePath, JSON.stringify({ message: { role: "user", content: "start" } }) + "\n");

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    const seedCount = events.length; // Seed emits for non-idle sessions

    // Append assistant → triggers done
    appendFileSync(filePath, JSON.stringify({
      message: { role: "assistant", content: [{ type: "text", text: "done" }] },
    }) + "\n");
    await new Promise((r) => setTimeout(r, 2500));

    const postSeed = events.slice(seedCount);
    const lastEvent = postSeed[postSeed.length - 1]!;
    expect(lastEvent.status).toBe("done");
  });

  test("promotes tool_use running to waiting after timeout", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-004.jsonl");
    // Seed with idle content
    writeFileSync(filePath, JSON.stringify({ message: { role: "assistant", content: [{ type: "text", text: "hi" }] } }) + "\n");

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    const seedCount = events.length;

    // Append assistant tool_use — initially "running"
    appendFileSync(filePath, JSON.stringify({
      message: { role: "assistant", content: [{ type: "tool_use" }] },
    }) + "\n");
    await new Promise((r) => setTimeout(r, 2500));

    const runningEvents = events.slice(seedCount).filter((e) => e.status === "running");
    expect(runningEvents.length).toBeGreaterThanOrEqual(1);

    // Wait for the promotion threshold (TOOL_USE_WAIT_MS = 3s) + a poll cycle
    await new Promise((r) => setTimeout(r, 3500));

    const waitingEvents = events.slice(seedCount).filter((e) => e.status === "waiting");
    expect(waitingEvents.length).toBeGreaterThanOrEqual(1);
    expect(waitingEvents[0]!.agent).toBe("claude-code");
    expect(waitingEvents[0]!.session).toBe("myapp-session");
  }, 10_000);
});
