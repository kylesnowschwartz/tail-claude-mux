import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { ClaudeCodeHookAdapter } from "../src/agents/watchers/claude-code-hooks";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext, HookPayload } from "../src/contracts/agent-watcher";
import { isHookReceiver } from "../src/contracts/agent-watcher";

function makeCtx(sessionMap: Record<string, string> = {}): AgentWatcherContext & { events: AgentEvent[] } {
  const events: AgentEvent[] = [];
  return {
    events,
    resolveSession(projectDir: string) {
      // Direct match first
      if (sessionMap[projectDir]) return sessionMap[projectDir];
      // Check if any key is a suffix of projectDir (for absolute path matching)
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

function hook(event: string, session_id: string, cwd: string, extra?: Partial<HookPayload>): HookPayload {
  return { event, session_id, cwd, ...extra };
}

describe("ClaudeCodeHookAdapter", () => {
  let adapter: ClaudeCodeHookAdapter;
  let ctx: ReturnType<typeof makeCtx>;

  beforeEach(() => {
    adapter = new ClaudeCodeHookAdapter();
    ctx = makeCtx({ "/tmp/myproject": "myproject" });
    // start without seed (no projectsDir to scan)
    adapter.start(ctx);
  });

  afterEach(() => {
    adapter.stop();
  });

  test("implements HookReceiver", () => {
    expect(isHookReceiver(adapter)).toBe(true);
  });

  test("has name 'claude-code'", () => {
    expect(adapter.name).toBe("claude-code");
  });

  // --- UserPromptSubmit ---

  test("UserPromptSubmit emits running", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
    expect(ctx.events[0].session).toBe("myproject");
    expect(ctx.events[0].threadId).toBe("sess-1");
    expect(ctx.events[0].agent).toBe("claude-code");
  });

  // --- PreToolUse ---

  test("PreToolUse emits running", () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", { tool_name: "Read" }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
  });

  test("PreToolUse promotes to waiting after 3s with no follow-up", async () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject"));

    // Immediately: running
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");

    // Wait for the 3s timer to fire
    await new Promise((r) => setTimeout(r, 3200));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].status).toBe("waiting");
    expect(ctx.events[1].threadId).toBe("sess-1");
  });

  test("PreToolUse followed by Stop within 3s suppresses waiting", async () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject"));
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");

    // Stop arrives 100ms later — within the 3s window
    await new Promise((r) => setTimeout(r, 100));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].status).toBe("done");

    // Wait past the 3s mark — no waiting emission should appear
    await new Promise((r) => setTimeout(r, 3200));
    expect(ctx.events).toHaveLength(2);
  });

  // --- Stop ---

  test("Stop emits done", () => {
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("done");
  });

  // --- Notification ---

  test("Notification emits done", () => {
    adapter.handleHook(hook("Notification", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("done");
  });

  // --- Unknown event ---

  test("unknown event emits nothing", () => {
    adapter.handleHook(hook("SomeNewEvent", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(0);
  });

  // --- Unresolved session ---

  test("unresolved cwd emits nothing", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/unknown-project"));

    expect(ctx.events).toHaveLength(0);
  });

  // --- Multiple threads ---

  test("tracks independent threads", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("UserPromptSubmit", "sess-2", "/tmp/myproject"));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[0]).toMatchObject({ threadId: "sess-1", status: "running" });
    expect(ctx.events[1]).toMatchObject({ threadId: "sess-2", status: "running" });
    expect(ctx.events[2]).toMatchObject({ threadId: "sess-1", status: "done" });
  });

  // --- Deduplication ---

  test("does not emit duplicate status for same thread", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject"));

    // Both are "running" — second should be suppressed
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
  });

  test("emits when status changes", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events.map((e) => e.status)).toEqual(["running", "done", "running"]);
  });
});
