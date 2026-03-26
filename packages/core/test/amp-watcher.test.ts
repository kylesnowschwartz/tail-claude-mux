import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, rmSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { AmpAgentWatcher, determineStatus } from "../src/agents/watchers/amp";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext } from "../src/contracts/agent-watcher";

// --- determineStatus ---

describe("determineStatus", () => {
  test("returns idle for null message", () => {
    expect(determineStatus(null)).toBe("idle");
  });

  test("returns idle for message with no role", () => {
    expect(determineStatus({})).toBe("idle");
  });

  test("returns running for user message", () => {
    expect(determineStatus({ role: "user" })).toBe("running");
  });

  test("returns running for assistant with no state (pre-streaming)", () => {
    expect(determineStatus({ role: "assistant" })).toBe("running");
  });

  test("returns running for streaming assistant", () => {
    expect(determineStatus({ role: "assistant", state: { type: "streaming" } })).toBe("running");
  });

  test("returns running for tool_use stop reason", () => {
    expect(determineStatus({ role: "assistant", state: { type: "complete", stopReason: "tool_use" } })).toBe("running");
  });

  test("returns done for end_turn stop reason", () => {
    expect(determineStatus({ role: "assistant", state: { type: "complete", stopReason: "end_turn" } })).toBe("done");
  });

  test("returns interrupted for cancelled", () => {
    expect(determineStatus({ role: "assistant", state: { type: "cancelled" } })).toBe("interrupted");
  });

  test("returns waiting for unknown assistant state", () => {
    expect(determineStatus({ role: "assistant", state: {} })).toBe("waiting");
  });
});

// --- AmpAgentWatcher integration ---

describe("AmpAgentWatcher", () => {
  let tmpDir: string;
  let watcher: AmpAgentWatcher;
  let events: AgentEvent[];
  let ctx: AgentWatcherContext;

  function writeThread(id: string, data: Record<string, unknown>) {
    writeFileSync(join(tmpDir, `${id}.json`), JSON.stringify(data));
  }

  beforeEach(() => {
    tmpDir = join(tmpdir(), `amp-watcher-test-${Date.now()}`);
    mkdirSync(tmpDir, { recursive: true });
    events = [];
    ctx = {
      resolveSession: (dir) => dir === "/projects/myapp" ? "myapp-session" : null,
      emit: (event) => events.push(event),
    };
    // @ts-ignore — override private threadsDir for testing
    watcher = new AmpAgentWatcher();
    (watcher as any).threadsDir = tmpDir;
  });

  afterEach(() => {
    watcher.stop();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("emits event when thread status changes", async () => {
    writeThread("T-test-001", {
      v: 1,
      title: "Test thread",
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
      messages: [{ role: "user" }],
    });

    watcher.start(ctx);
    // Wait for initial scan
    await new Promise((r) => setTimeout(r, 100));

    expect(events.length).toBe(1);
    expect(events[0]!.agent).toBe("amp");
    expect(events[0]!.session).toBe("myapp-session");
    expect(events[0]!.status).toBe("running");
    expect(events[0]!.threadId).toBe("T-test-001");
    expect(events[0]!.threadName).toBe("Test thread");
  });

  test("skips thread when version unchanged", async () => {
    writeThread("T-test-002", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));
    expect(events.length).toBe(1);

    // Write same version — should not emit
    writeThread("T-test-002", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));
    expect(events.length).toBe(1);
  });

  test("does not emit when session resolves to unknown", async () => {
    writeThread("T-test-003", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///unknown/dir" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));
    expect(events.length).toBe(0);
  });

  test("emits on version bump with new status", async () => {
    writeThread("T-test-004", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 100));
    expect(events.length).toBe(1);
    expect(events[0]!.status).toBe("running");

    // Bump version with done status
    writeThread("T-test-004", {
      v: 2,
      messages: [{ role: "assistant", state: { type: "complete", stopReason: "end_turn" } }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));
    expect(events.length).toBe(2);
    expect(events[1]!.status).toBe("done");
  });
});
