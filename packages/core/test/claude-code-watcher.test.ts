import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, rmSync, appendFileSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { ClaudeCodeAgentWatcher, determineStatus } from "../src/agents/watchers/claude-code";
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

  test("returns waiting for assistant with text only", () => {
    expect(determineStatus({
      message: { role: "assistant", content: [{ type: "text", text: "hello" }] },
    })).toBe("waiting");
  });

  test("returns running for user message", () => {
    expect(determineStatus({
      message: { role: "user", content: "hello" },
    })).toBe("running");
  });

  test("returns waiting for assistant with string content", () => {
    expect(determineStatus({
      message: { role: "assistant", content: "thinking..." },
    })).toBe("waiting");
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

  test("emits event from JSONL in project subdirectory", async () => {
    // Create encoded project dir: /projects/myapp → -projects-myapp
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const entry = JSON.stringify({
      message: { role: "user", content: "fix the bug" },
    });
    writeFileSync(join(projDir, "session-001.jsonl"), entry + "\n");

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));

    expect(events.length).toBe(1);
    expect(events[0]!.agent).toBe("claude-code");
    expect(events[0]!.session).toBe("myapp-session");
    expect(events[0]!.status).toBe("running");
    expect(events[0]!.threadId).toBe("session-001");
  });

  test("skips when session cannot be resolved", async () => {
    const projDir = join(tmpDir, "-unknown-project");
    mkdirSync(projDir, { recursive: true });

    const entry = JSON.stringify({
      message: { role: "user", content: "hello" },
    });
    writeFileSync(join(projDir, "session-002.jsonl"), entry + "\n");

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));

    expect(events.length).toBe(0);
  });

  test("detects status change on file append", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const filePath = join(projDir, "session-003.jsonl");
    writeFileSync(filePath, JSON.stringify({ message: { role: "user", content: "start" } }) + "\n");

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));
    expect(events.length).toBe(1);
    expect(events[0]!.status).toBe("running");

    // Append assistant response (text only → waiting)
    appendFileSync(filePath, JSON.stringify({
      message: { role: "assistant", content: [{ type: "text", text: "done" }] },
    }) + "\n");

    await new Promise((r) => setTimeout(r, 2500));
    expect(events.length).toBe(2);
    expect(events[1]!.status).toBe("waiting");
  });

  test("extracts thread name from first user message", async () => {
    const projDir = join(tmpDir, "-projects-myapp");
    mkdirSync(projDir, { recursive: true });

    const entry = JSON.stringify({
      message: { role: "user", content: "Fix the login flow" },
    });
    writeFileSync(join(projDir, "session-004.jsonl"), entry + "\n");

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));

    expect(events[0]!.threadName).toBe("Fix the login flow");
  });
});
