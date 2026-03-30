import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, rmSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { AmpAgentWatcher, determineStatus } from "../src/agents/watchers/amp";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext } from "../src/contracts/agent-watcher";

// --- determineStatus ---

describe("Amp determineStatus", () => {
  // Null / empty cases
  test("returns idle for null message", () => {
    expect(determineStatus(null)).toBe("idle");
  });

  test("returns idle for message with no role", () => {
    expect(determineStatus({})).toBe("idle");
  });

  test("returns idle for empty messages array (no last message)", () => {
    expect(determineStatus(null)).toBe("idle");
  });

  // User messages — always running (new prompt or tool result)
  test("returns running for user message (new prompt)", () => {
    expect(determineStatus({ role: "user" })).toBe("running");
  });

  test("returns running for user message with text content", () => {
    expect(determineStatus({ role: "user", content: [{ type: "text" }] })).toBe("running");
  });

  test("returns running for user message with tool_result", () => {
    expect(determineStatus({ role: "user", content: [{ type: "tool_result" }] })).toBe("running");
  });

  test("returns running for user message with interrupted=true", () => {
    // User sent new message while agent was running — still means "running"
    expect(determineStatus({ role: "user", interrupted: true, content: [{ type: "text" }] })).toBe("running");
  });

  // Assistant streaming — model actively generating
  test("returns running for assistant with no state (pre-streaming)", () => {
    expect(determineStatus({ role: "assistant" })).toBe("running");
  });

  test("returns running for assistant with empty state", () => {
    expect(determineStatus({ role: "assistant", state: {} })).toBe("running");
  });

  test("returns running for streaming assistant (thinking)", () => {
    expect(determineStatus({ role: "assistant", state: { type: "streaming" } })).toBe("running");
  });

  test("returns running for streaming assistant with tool_use content", () => {
    expect(determineStatus({
      role: "assistant",
      state: { type: "streaming" },
      content: [{ type: "thinking" }, { type: "tool_use" }],
    })).toBe("running");
  });

  // Assistant complete — check stopReason
  test("returns running for complete with tool_use stopReason", () => {
    expect(determineStatus({ role: "assistant", state: { type: "complete", stopReason: "tool_use" } })).toBe("running");
  });

  test("returns done for complete with end_turn stopReason", () => {
    expect(determineStatus({ role: "assistant", state: { type: "complete", stopReason: "end_turn" } })).toBe("done");
  });

  test("returns error for complete with max_tokens stopReason", () => {
    expect(determineStatus({ role: "assistant", state: { type: "complete", stopReason: "max_tokens" } })).toBe("error");
  });

  test("returns error for complete with unknown stopReason", () => {
    expect(determineStatus({ role: "assistant", state: { type: "complete", stopReason: "unknown_reason" } })).toBe("error");
  });

  // Assistant cancelled — user interrupt (Escape or new message while streaming)
  test("returns interrupted for cancelled state", () => {
    expect(determineStatus({ role: "assistant", state: { type: "cancelled" } })).toBe("interrupted");
  });

  test("returns interrupted for cancelled with thinking content", () => {
    expect(determineStatus({
      role: "assistant",
      state: { type: "cancelled" },
      content: [{ type: "thinking" }],
    })).toBe("interrupted");
  });

  test("returns interrupted for cancelled with tool_use content", () => {
    expect(determineStatus({
      role: "assistant",
      state: { type: "cancelled" },
      content: [{ type: "tool_use" }],
    })).toBe("interrupted");
  });

  test("returns interrupted for cancelled with text content", () => {
    expect(determineStatus({
      role: "assistant",
      state: { type: "cancelled" },
      content: [{ type: "text" }],
    })).toBe("interrupted");
  });

  test("returns interrupted for cancelled with empty content", () => {
    expect(determineStatus({
      role: "assistant",
      state: { type: "cancelled" },
      content: [],
    })).toBe("interrupted");
  });

  // Unknown state type — defensive
  test("returns running for unknown assistant state type", () => {
    expect(determineStatus({ role: "assistant", state: { type: "some_future_state" } })).toBe("running");
  });

  // Unknown role — defensive
  test("returns idle for unknown role", () => {
    expect(determineStatus({ role: "system" })).toBe("idle");
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
    watcher = new AmpAgentWatcher();
    (watcher as any).threadsDir = tmpDir;
  });

  afterEach(() => {
    watcher.stop();
    rmSync(tmpDir, { recursive: true, force: true });
  });

  test("seed scan emits current non-idle threads with titles", async () => {
    writeThread("T-test-001", {
      v: 1,
      title: "Thread one",
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
      messages: [{ role: "user" }],
    });
    writeThread("T-test-002", {
      v: 1,
      title: "Thread two",
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
      messages: [{ role: "assistant", state: { type: "streaming" } }],
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));

    expect(events).toHaveLength(2);
    expect(events.map((event) => event.threadId).sort()).toEqual(["T-test-001", "T-test-002"]);
    expect(events.map((event) => event.threadName).sort()).toEqual(["Thread one", "Thread two"]);
    expect(events.every((event) => event.status === "running")).toBe(true);
  });

  test("emits on version bump after seed", async () => {
    writeThread("T-test-003", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    // Bump version
    writeThread("T-test-003", {
      v: 2,
      title: "Test thread",
      messages: [{ role: "assistant", state: { type: "complete", stopReason: "end_turn" } }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    expect(events.length).toBeGreaterThanOrEqual(1);
    expect(events[0]!.status).toBe("done");
    expect(events[0]!.session).toBe("myapp-session");
    expect(events[0]!.threadName).toBe("Test thread");
  });

  test("does not emit when session resolves to unknown", async () => {
    writeThread("T-test-004", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///unknown/dir" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    writeThread("T-test-004", {
      v: 2,
      messages: [{ role: "assistant", state: { type: "complete", stopReason: "end_turn" } }],
      env: { initial: { trees: [{ uri: "file:///unknown/dir" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    expect(events.length).toBe(0);
  });

  test("emits title updates even when status stays running", async () => {
    writeThread("T-test-005", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    writeThread("T-test-005", {
      v: 2,
      title: "Named thread",
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    expect(events).toHaveLength(1);
    expect(events[0]!.status).toBe("running");
    expect(events[0]!.threadName).toBe("Named thread");
  });

  test("emits error when Amp ends a thread with a terminal failure stop reason", async () => {
    writeThread("T-test-006", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    writeThread("T-test-006", {
      v: 2,
      title: "Token limit hit",
      messages: [{ role: "assistant", state: { type: "complete", stopReason: "max_tokens" } }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    expect(events.length).toBeGreaterThanOrEqual(1);
    expect(events[0]!.status).toBe("error");
    expect(events[0]!.threadName).toBe("Token limit hit");
  });

  test("emits interrupted for cancelled assistant state", async () => {
    writeThread("T-test-007", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    // User pressed Escape while assistant was streaming
    writeThread("T-test-007", {
      v: 2,
      title: "Cancelled thread",
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "cancelled" }, content: [{ type: "thinking" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    expect(events.length).toBeGreaterThanOrEqual(1);
    expect(events[0]!.status).toBe("interrupted");
    expect(events[0]!.threadName).toBe("Cancelled thread");
  });

  test("emits running after cancel when user sends new message", async () => {
    writeThread("T-test-008", {
      v: 1,
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "cancelled" }, content: [{ type: "thinking" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    // User sends new message after cancelling — interrupted=true on the user msg
    writeThread("T-test-008", {
      v: 2,
      title: "Resumed thread",
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "cancelled" }, content: [{ type: "thinking" }] },
        { role: "user", interrupted: true, content: [{ type: "text" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    expect(events.length).toBeGreaterThanOrEqual(1);
    expect(events[0]!.status).toBe("running");
  });

  test("keeps running through tool_use → tool_result cycle", async () => {
    writeThread("T-test-009", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    // Streaming → tool_use complete → tool_result in one write
    writeThread("T-test-009", {
      v: 3,
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "complete", stopReason: "tool_use" }, content: [{ type: "thinking" }, { type: "tool_use" }] },
        { role: "user", content: [{ type: "tool_result" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    // Status stayed "running" throughout — no done events
    const doneEvents = events.filter((e) => e.status === "done");
    expect(doneEvents.length).toBe(0);

    // No interrupted events either
    const interruptedEvents = events.filter((e) => e.status === "interrupted");
    expect(interruptedEvents.length).toBe(0);
  });

  test("detects stuck running and promotes to done (process killed)", async () => {
    writeThread("T-test-010", {
      v: 1,
      messages: [{ role: "assistant", state: { type: "streaming" }, content: [{ type: "thinking" }] }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    const seedCount = events.length;

    // Backdate lastGrowthAt to simulate process killed 16s ago
    const snapshot = (watcher as any).threads.get("T-test-010");
    snapshot.lastGrowthAt = Date.now() - 16_000;

    // Wait for next poll cycle to detect stuck
    await new Promise((r) => setTimeout(r, 2500));

    const doneEvents = events.slice(seedCount).filter((e) => e.status === "done");
    expect(doneEvents.length).toBeGreaterThanOrEqual(1);
  }, 10_000);

  test("detects stuck running for tool_result last message (killed between turns)", async () => {
    writeThread("T-test-011", {
      v: 1,
      messages: [
        { role: "assistant", state: { type: "complete", stopReason: "tool_use" }, content: [{ type: "tool_use" }] },
        { role: "user", content: [{ type: "tool_result" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    const seedCount = events.length;

    // Backdate lastGrowthAt
    const snapshot = (watcher as any).threads.get("T-test-011");
    snapshot.lastGrowthAt = Date.now() - 16_000;

    await new Promise((r) => setTimeout(r, 2500));

    const doneEvents = events.slice(seedCount).filter((e) => e.status === "done");
    expect(doneEvents.length).toBeGreaterThanOrEqual(1);
  }, 10_000);

  test("streaming state during seed emits running", async () => {
    // Thread is actively streaming when watcher starts
    writeThread("T-test-012", {
      v: 50,
      title: "Active stream",
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "streaming" }, content: [{ type: "thinking" }, { type: "text" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));

    expect(events).toHaveLength(1);
    expect(events[0]!.status).toBe("running");
    expect(events[0]!.threadName).toBe("Active stream");
  });

  test("does not emit for idle threads (done status is not idle)", async () => {
    // A done thread should still emit during seed
    writeThread("T-test-013", {
      v: 10,
      title: "Completed",
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "complete", stopReason: "end_turn" }, content: [{ type: "text" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));

    expect(events).toHaveLength(1);
    expect(events[0]!.status).toBe("done");
  });

  test("does not emit for truly idle threads (no messages)", async () => {
    // Empty thread — idle, should not emit
    writeThread("T-test-014", {
      v: 0,
      messages: [],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));

    expect(events).toHaveLength(0);
  });

  test("cancelled then tool_result pattern stays running", async () => {
    // This happens when a cancel occurs during tool execution —
    // the tool result is still written after the cancelled message
    writeThread("T-test-015", {
      v: 1,
      messages: [{ role: "user" }],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });

    watcher.start(ctx);
    await new Promise((r) => setTimeout(r, 200));
    events = [];

    writeThread("T-test-015", {
      v: 2,
      messages: [
        { role: "user" },
        { role: "assistant", state: { type: "cancelled" }, content: [{ type: "tool_use" }] },
        { role: "user", content: [{ type: "tool_result" }] },
      ],
      env: { initial: { trees: [{ uri: "file:///projects/myapp" }] } },
    });
    await new Promise((r) => setTimeout(r, 2500));

    // Last msg is user/tool_result → running, not interrupted
    const interruptedEvents = events.filter((e) => e.status === "interrupted");
    expect(interruptedEvents.length).toBe(0);
  });
});
