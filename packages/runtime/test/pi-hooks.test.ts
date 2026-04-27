import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync, utimesSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { PiHookAdapter, piToolDescription } from "../src/agents/watchers/pi-hooks";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext, HookPayload } from "../src/contracts/agent-watcher";
import { isHookReceiver } from "../src/contracts/agent-watcher";

function makeCtx(sessionMap: Record<string, string> = {}): AgentWatcherContext & { events: AgentEvent[] } {
  const events: AgentEvent[] = [];
  return {
    events,
    resolveSession(projectDir: string) {
      if (sessionMap[projectDir]) return sessionMap[projectDir];
      for (const [key, val] of Object.entries(sessionMap)) {
        if (projectDir.endsWith(key) || key.endsWith(projectDir)) return val;
      }
      return null;
    },
    emit(event: AgentEvent) {
      events.push(event);
    },
  };
}

/** Build a pi HookPayload. `agent` defaults to "pi" so tests stay concise. */
function hook(event: string, session_id: string, cwd: string, extra?: Partial<HookPayload>): HookPayload {
  return { agent: "pi", event, session_id, cwd, ...extra };
}

describe("PiHookAdapter", () => {
  let adapter: PiHookAdapter;
  let ctx: ReturnType<typeof makeCtx>;

  beforeEach(() => {
    // Point the adapter at a non-existent sessions dir so the cold-start
    // seed is a no-op unless a specific test sets one up.
    adapter = new PiHookAdapter(join(tmpdir(), "tcm-pi-test-" + Math.random().toString(36).slice(2)));
    ctx = makeCtx({ "/tmp/myproject": "myproject" });
    adapter.start(ctx);
  });

  afterEach(() => {
    adapter.stop();
  });

  // --- Basic wiring ---

  test("has name 'pi'", () => {
    expect(adapter.name).toBe("pi");
  });

  test("implements HookReceiver", () => {
    expect(isHookReceiver(adapter)).toBe(true);
  });

  // --- Agent discriminator ---

  test("payload with agent: 'claude-code' is ignored", () => {
    adapter.handleHook({
      agent: "claude-code",
      event: "SessionStart",
      session_id: "sess-cc-1",
      cwd: "/tmp/myproject",
    });

    expect(ctx.events).toHaveLength(0);
  });

  test("payload without agent field is ignored (default routes to Claude Code)", () => {
    adapter.handleHook({
      event: "session_start",
      session_id: "sess-1",
      cwd: "/tmp/myproject",
    });

    expect(ctx.events).toHaveLength(0);
  });

  // --- session_start ---

  test("session_start emits idle", () => {
    adapter.handleHook(hook("session_start", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0]).toMatchObject({
      agent: "pi",
      session: "myproject",
      status: "idle",
      threadId: "sess-1",
    });
  });

  test("session_start propagates session_name as threadName", () => {
    adapter.handleHook(hook("session_start", "sess-1", "/tmp/myproject", { session_name: "Refactor auth" }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].threadName).toBe("Refactor auth");
  });

  // --- agent_start / agent_end ---

  test("agent_start emits running with no toolDescription", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
    expect(ctx.events[0].toolDescription).toBeUndefined();
  });

  test("agent_end stop_reason 'stop' emits done", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", { stop_reason: "stop" }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].status).toBe("done");
  });

  test("agent_end stop_reason 'length' emits done", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", { stop_reason: "length" }));

    expect(ctx.events[1].status).toBe("done");
  });

  test("agent_end stop_reason 'toolUse' emits done (turn boundary)", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", { stop_reason: "toolUse" }));

    expect(ctx.events[1].status).toBe("done");
  });

  test("agent_end stop_reason 'aborted' emits interrupted", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", { stop_reason: "aborted" }));

    expect(ctx.events[1].status).toBe("interrupted");
    expect(ctx.events[1].toolDescription).toBeUndefined();
  });

  test("agent_end stop_reason 'error' emits error with truncated error_message", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", {
      stop_reason: "error",
      error_message: "boom: provider returned 500",
    }));

    expect(ctx.events[1].status).toBe("error");
    expect(ctx.events[1].toolDescription).toBe("boom: provider returned 500");
  });

  test("agent_end stop_reason 'error' truncates long error_message to 80 chars", () => {
    const long = "oops ".repeat(40); // 200 chars
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", {
      stop_reason: "error",
      error_message: long,
    }));

    const desc = ctx.events[1].toolDescription!;
    expect(desc.length).toBe(80);
    expect(desc.endsWith("\u2026")).toBe(true);
  });

  // --- tool_execution_start / tool_execution_end ---

  test("tool_execution_start for read emits 'Reading <basename>'", () => {
    adapter.handleHook(hook("tool_execution_start", "sess-1", "/tmp/myproject", {
      tool_name: "read",
      tool_input: { path: "/tmp/myproject/x.ts" },
    }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
    expect(ctx.events[0].toolDescription).toBe("Reading x.ts");
  });

  test("tool_execution_start for bash emits 'Running <command>'", () => {
    adapter.handleHook(hook("tool_execution_start", "sess-1", "/tmp/myproject", {
      tool_name: "bash",
      tool_input: { command: "git status" },
    }));

    expect(ctx.events[0].toolDescription).toBe("Running git status");
  });

  test("tool_execution_end with tool_is_error keeps status running and description", () => {
    adapter.handleHook(hook("tool_execution_start", "sess-1", "/tmp/myproject", {
      tool_name: "bash",
      tool_input: { command: "npm test" },
    }));
    adapter.handleHook(hook("tool_execution_end", "sess-1", "/tmp/myproject", {
      tool_name: "bash",
      tool_is_error: true,
    }));

    // Both running, same description — dedup suppresses the second emission.
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
    expect(ctx.events[0].toolDescription).toBe("Running npm test");
  });

  test("consecutive tool_execution_start events with new descriptions both emit", () => {
    adapter.handleHook(hook("tool_execution_start", "sess-1", "/tmp/myproject", {
      tool_name: "read",
      tool_input: { path: "/tmp/a.ts" },
    }));
    adapter.handleHook(hook("tool_execution_start", "sess-1", "/tmp/myproject", {
      tool_name: "edit",
      tool_input: { path: "/tmp/b.ts" },
    }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[0].toolDescription).toBe("Reading a.ts");
    expect(ctx.events[1].toolDescription).toBe("Editing b.ts");
  });

  test("agent_start after tool_execution_start clears toolDescription", () => {
    adapter.handleHook(hook("tool_execution_start", "sess-1", "/tmp/myproject", {
      tool_name: "read",
      tool_input: { path: "/tmp/a.ts" },
    }));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", { stop_reason: "stop" }));
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[2].toolDescription).toBeUndefined();
  });

  // --- session_shutdown ---

  test("session_shutdown emits done with ended: true and drops state", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("session_shutdown", "sess-1", "/tmp/myproject", { shutdown_reason: "quit" }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].status).toBe("done");
    expect(ctx.events[1].ended).toBe(true);

    // A new session_start for the same UUID should be treated as a fresh thread.
    adapter.handleHook(hook("session_start", "sess-1", "/tmp/myproject"));
    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[2].status).toBe("idle");
  });

  test("session_shutdown after agent_end still emits (bypasses dedup)", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_end", "sess-1", "/tmp/myproject", { stop_reason: "stop" }));
    adapter.handleHook(hook("session_shutdown", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[2].status).toBe("done");
    expect(ctx.events[2].ended).toBe(true);
  });

  // --- Multiple threads ---

  test("two concurrent session_ids in same cwd track separately", () => {
    adapter.handleHook(hook("session_start", "sess-a", "/tmp/myproject"));
    adapter.handleHook(hook("session_start", "sess-b", "/tmp/myproject"));
    adapter.handleHook(hook("agent_start", "sess-a", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[0]).toMatchObject({ threadId: "sess-a", status: "idle" });
    expect(ctx.events[1]).toMatchObject({ threadId: "sess-b", status: "idle" });
    expect(ctx.events[2]).toMatchObject({ threadId: "sess-a", status: "running" });
  });

  // --- Deduplication ---

  test("consecutive agent_start events for same thread emit once", () => {
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("agent_start", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
  });

  // --- Unresolved session ---

  test("unresolved cwd emits nothing", () => {
    adapter.handleHook(hook("session_start", "sess-1", "/tmp/unknown-project"));

    expect(ctx.events).toHaveLength(0);
  });

  // --- Unknown event ---

  test("unknown event emits nothing (after creating thread)", () => {
    adapter.handleHook(hook("some_future_event", "sess-1", "/tmp/myproject"));

    // The adapter registers the thread but the event itself is ignored,
    // so no emission goes through.
    expect(ctx.events).toHaveLength(0);
  });
});

// --- Cold-start JSONL seed ---

describe("PiHookAdapter — JSONL cold-start seed", () => {
  let tmpRoot: string;
  let sessionsDir: string;
  let projectDir: string;

  beforeEach(() => {
    tmpRoot = mkdtempSync(join(tmpdir(), "tcm-pi-seed-"));
    sessionsDir = join(tmpRoot, "sessions");
    mkdirSync(sessionsDir, { recursive: true });
    // The project dir has to exist on disk so decodePiProjectDir resolves it.
    projectDir = join(tmpRoot, "myproject");
    mkdirSync(projectDir, { recursive: true });
  });

  afterEach(() => {
    rmSync(tmpRoot, { recursive: true, force: true });
  });

  function writeSession(uuid: string, entries: unknown[]): string {
    // Pi encodes project dirs as `--<path-with-slashes-as-dashes>--`, but the
    // seed no longer depends on the folder name — it reads SessionHeader.cwd
    // directly. Any per-project directory name works here.
    const dir = join(sessionsDir, "--project--");
    mkdirSync(dir, { recursive: true });
    const filePath = join(dir, `2026-01-01T00-00-00-000Z_${uuid}.jsonl`);
    const body = entries.map((e) => JSON.stringify(e)).join("\n");
    writeFileSync(filePath, body);
    // Make sure mtime is fresh so the seed considers the file current.
    const now = Date.now() / 1000;
    utimesSync(filePath, now, now);
    return filePath;
  }

  test("seeds a running pi session without any hook", async () => {
    writeSession("uuid-1", [
      { type: "session", version: 3, id: "uuid-1", timestamp: "2026-01-01T00:00:00Z", cwd: projectDir },
      { type: "message", id: "a", parentId: null, timestamp: "2026-01-01T00:00:01Z",
        message: { role: "user", content: "Hello, please help me" } },
      { type: "message", id: "b", parentId: "a", timestamp: "2026-01-01T00:00:02Z",
        message: { role: "assistant", content: [{ type: "text", text: "Sure" }], stopReason: "toolUse" } },
    ]);

    const adapter = new PiHookAdapter(sessionsDir);
    const ctx = makeCtx({ [projectDir]: "myproject" });
    adapter.start(ctx);

    // Seed is async; wait a tick.
    await new Promise((r) => setTimeout(r, 50));

    expect(ctx.events.length).toBeGreaterThanOrEqual(1);
    const seeded = ctx.events.find((e) => e.threadId === "uuid-1");
    expect(seeded).toBeDefined();
    expect(seeded!.status).toBe("running");
    expect(seeded!.threadName).toBe("Hello, please help me");

    adapter.stop();
  });

  test("does not re-seed a thread already populated by a hook", async () => {
    writeSession("uuid-2", [
      { type: "session", version: 3, id: "uuid-2", timestamp: "2026-01-01T00:00:00Z", cwd: projectDir },
      { type: "message", id: "a", parentId: null, timestamp: "2026-01-01T00:00:01Z",
        message: { role: "user", content: "First message" } },
      { type: "message", id: "b", parentId: "a", timestamp: "2026-01-01T00:00:02Z",
        message: { role: "assistant", content: [{ type: "text", text: "ok" }], stopReason: "toolUse" } },
    ]);

    const adapter = new PiHookAdapter(sessionsDir);
    const ctx = makeCtx({ [projectDir]: "myproject" });

    // Simulate a hook arriving before the seed completes by calling start
    // and a hook immediately.
    adapter.start(ctx);
    adapter.handleHook({ agent: "pi", event: "agent_start", session_id: "uuid-2", cwd: projectDir });
    await new Promise((r) => setTimeout(r, 50));

    // We expect exactly one entry per threadId in terms of seed — hook wins.
    const running = ctx.events.filter((e) => e.threadId === "uuid-2" && e.status === "running");
    expect(running.length).toBeGreaterThanOrEqual(1);
    // No "idle" seed entry should leak in.
    const idle = ctx.events.filter((e) => e.threadId === "uuid-2" && e.status === "idle");
    expect(idle.length).toBe(0);

    adapter.stop();
  });

  test("skips sessions whose trailing status is terminal", async () => {
    writeSession("uuid-3", [
      { type: "session", version: 3, id: "uuid-3", timestamp: "2026-01-01T00:00:00Z", cwd: projectDir },
      { type: "message", id: "a", parentId: null, timestamp: "2026-01-01T00:00:01Z",
        message: { role: "user", content: "Done already" } },
      { type: "message", id: "b", parentId: "a", timestamp: "2026-01-01T00:00:02Z",
        message: { role: "assistant", content: [{ type: "text", text: "All done" }], stopReason: "stop" } },
    ]);

    const adapter = new PiHookAdapter(sessionsDir);
    const ctx = makeCtx({ [projectDir]: "myproject" });
    adapter.start(ctx);
    await new Promise((r) => setTimeout(r, 50));

    expect(ctx.events.find((e) => e.threadId === "uuid-3")).toBeUndefined();

    adapter.stop();
  });
});

// --- piToolDescription unit tests ---

describe("piToolDescription", () => {
  test("read with path returns basename", () => {
    expect(piToolDescription("read", { path: "/home/kyle/project/src/config.ts" }))
      .toBe("Reading config.ts");
  });

  test("edit with path returns basename", () => {
    expect(piToolDescription("edit", { path: "/tmp/main.go" }))
      .toBe("Editing main.go");
  });

  test("write with path returns basename", () => {
    expect(piToolDescription("write", { path: "/tmp/out.json" }))
      .toBe("Writing out.json");
  });

  test("ls with path returns basename", () => {
    expect(piToolDescription("ls", { path: "/tmp/dir" })).toBe("Listing dir");
  });

  test("read without path returns verb only", () => {
    expect(piToolDescription("read", {})).toBe("Reading");
  });

  test("bash truncates long commands to 30 chars", () => {
    const long = "a".repeat(50);
    expect(piToolDescription("bash", { command: long })).toBe(`Running ${long.slice(0, 30)}`);
  });

  test("bash without command returns fallback", () => {
    expect(piToolDescription("bash", {})).toBe("Running command");
  });

  test("grep / glob / find share the searching shape", () => {
    expect(piToolDescription("grep", { pattern: "TODO" })).toBe("Searching TODO");
    expect(piToolDescription("glob", { pattern: "**/*.ts" })).toBe("Searching **/*.ts");
    expect(piToolDescription("find", { pattern: "*.md" })).toBe("Searching *.md");
  });

  test("agent truncates long descriptions to 40 chars", () => {
    const long = "a".repeat(60);
    expect(piToolDescription("agent", { description: long })).toBe(long.slice(0, 40));
  });

  test("web_fetch returns static string", () => {
    expect(piToolDescription("web_fetch", {})).toBe("Fetching URL");
  });

  test("web_search with query", () => {
    expect(piToolDescription("web_search", { query: "bun docs" })).toBe("Search: bun docs");
  });

  test("ask_user_question with question", () => {
    expect(piToolDescription("ask_user_question", { question: "Which theme?" }))
      .toBe("Question: Which theme?");
  });

  test("todo_write returns static string", () => {
    expect(piToolDescription("todo_write", {})).toBe("Updating todos");
  });

  test("unknown tool returns its name", () => {
    expect(piToolDescription("my_custom_tool", {})).toBe("my_custom_tool");
  });

  test("undefined tool_name returns undefined", () => {
    expect(piToolDescription(undefined, {})).toBeUndefined();
  });

  test("undefined tool_input still works", () => {
    expect(piToolDescription("bash", undefined)).toBe("Running command");
  });
});
