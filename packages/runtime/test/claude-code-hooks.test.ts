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

  test("PreToolUse does not promote to waiting (no timer heuristic)", async () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");

    // Wait well past old 3s timer — no waiting emission should appear
    await new Promise((r) => setTimeout(r, 3500));

    // Still just the one "running" event — no timer-based promotion
    expect(ctx.events).toHaveLength(1);
  });

  // --- PostToolUse ---

  test("PostToolUse emits running", () => {
    // First set to waiting via PermissionRequest, then PostToolUse returns to running
    adapter.handleHook(hook("PermissionRequest", "sess-1", "/tmp/myproject", { tool_name: "Bash" }));
    adapter.handleHook(hook("PostToolUse", "sess-1", "/tmp/myproject", { tool_name: "Bash" }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].status).toBe("running");
  });

  // --- PermissionRequest ---

  test("PermissionRequest emits waiting", () => {
    adapter.handleHook(hook("PermissionRequest", "sess-1", "/tmp/myproject", { tool_name: "Bash" }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("waiting");
    expect(ctx.events[0].session).toBe("myproject");
    expect(ctx.events[0].threadId).toBe("sess-1");
  });

  test("PermissionRequest followed by PostToolUse transitions to running", () => {
    adapter.handleHook(hook("PermissionRequest", "sess-1", "/tmp/myproject", { tool_name: "Bash" }));
    adapter.handleHook(hook("PostToolUse", "sess-1", "/tmp/myproject", { tool_name: "Bash" }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[0].status).toBe("waiting");
    expect(ctx.events[1].status).toBe("running");
  });

  // --- Stop ---

  test("Stop emits done", () => {
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("done");
  });

  // --- Notification ---

  test("Notification with permission_prompt emits waiting", () => {
    adapter.handleHook(hook("Notification", "sess-1", "/tmp/myproject", {
      notification_type: "permission_prompt",
    } as any));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("waiting");
  });

  test("Notification with idle_prompt emits waiting", () => {
    adapter.handleHook(hook("Notification", "sess-1", "/tmp/myproject", {
      notification_type: "idle_prompt",
    } as any));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("waiting");
  });

  test("Notification without notification_type is ignored", () => {
    adapter.handleHook(hook("Notification", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(0);
  });

  test("Notification with auth_success is ignored", () => {
    adapter.handleHook(hook("Notification", "sess-1", "/tmp/myproject", {
      notification_type: "auth_success",
    } as any));

    expect(ctx.events).toHaveLength(0);
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
