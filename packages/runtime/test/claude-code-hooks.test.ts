import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { ClaudeCodeHookAdapter, toolDescription } from "../src/agents/watchers/claude-code-hooks";
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

  test("Notification with idle_prompt emits done (idle at prompt, not waiting)", () => {
    adapter.handleHook(hook("Notification", "sess-1", "/tmp/myproject", {
      notification_type: "idle_prompt",
    } as any));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("done");
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

  test("does not emit duplicate status for non-tool events", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    // PostToolUse also maps to "running" and is not a tool-context event
    adapter.handleHook(hook("PostToolUse", "sess-1", "/tmp/myproject"));

    // Both are "running", neither is PreToolUse/PermissionRequest — second suppressed
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("running");
  });

  test("PreToolUse still emits even when status unchanged (carries tool description)", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Read",
      tool_input: { file_path: "/tmp/foo.ts" },
    }));

    // Both are "running", but PreToolUse carries tool context so it still emits
    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[0].toolDescription).toBeUndefined();
    expect(ctx.events[1].toolDescription).toBe("Reading foo.ts");
  });

  test("emits when status changes", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events.map((e) => e.status)).toEqual(["running", "done", "running"]);
  });

  // --- Tool descriptions ---

  test("PreToolUse emits toolDescription for Read", () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Read",
      tool_input: { file_path: "/home/user/project/src/config.ts" },
    }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].toolDescription).toBe("Reading config.ts");
  });

  test("PreToolUse emits toolDescription for Bash", () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Bash",
      tool_input: { command: "git status" },
    }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].toolDescription).toBe("Running git status");
  });

  test("PermissionRequest includes toolDescription", () => {
    adapter.handleHook(hook("PermissionRequest", "sess-1", "/tmp/myproject", {
      tool_name: "Bash",
      tool_input: { command: "git push origin main" },
    }));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("waiting");
    expect(ctx.events[0].toolDescription).toBe("Running git push origin main");
  });

  test("consecutive PreToolUse events with same status still emit (new tool description)", () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Read",
      tool_input: { file_path: "/tmp/a.ts" },
    }));
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Edit",
      tool_input: { file_path: "/tmp/b.ts" },
    }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[0].toolDescription).toBe("Reading a.ts");
    expect(ctx.events[1].toolDescription).toBe("Editing b.ts");
  });

  test("UserPromptSubmit clears toolDescription", () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Read",
      tool_input: { file_path: "/tmp/a.ts" },
    }));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[2].toolDescription).toBeUndefined();
  });

  test("Stop clears toolDescription", () => {
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Bash",
      tool_input: { command: "npm test" },
    }));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].toolDescription).toBeUndefined();
  });

  // --- SessionStart ---

  test("SessionStart emits idle", () => {
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("idle");
    expect(ctx.events[0].session).toBe("myproject");
    expect(ctx.events[0].threadId).toBe("sess-1");
  });

  test("SessionStart followed by UserPromptSubmit transitions to running", () => {
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[0].status).toBe("idle");
    expect(ctx.events[1].status).toBe("running");
  });

  // --- SessionEnd ---

  test("SessionEnd emits done", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].status).toBe("done");
  });

  test("SessionEnd cleans up thread state", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"));
    // New SessionStart for same session_id should create fresh state
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[2].status).toBe("idle");
  });

  test("SessionEnd emits ended=true", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"));

    expect(ctx.events[1].ended).toBe(true);
  });

  test("SessionEnd after Stop still emits (bypasses dedup)", () => {
    // Regression: Stop sets status=done, then SessionEnd would be deduped
    // because status is unchanged. SessionEnd must bypass dedup so the
    // tracker receives the ended signal and removes the instance.
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("Stop", "sess-1", "/tmp/myproject"));
    adapter.handleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(3);
    expect(ctx.events[2].status).toBe("done");
    expect(ctx.events[2].ended).toBe(true);
  });
});

// --- toolDescription unit tests ---

describe("toolDescription", () => {
  test("Read with file_path returns basename", () => {
    expect(toolDescription("Read", { file_path: "/home/user/project/src/config.ts" }))
      .toBe("Reading config.ts");
  });

  test("Edit with file_path returns basename", () => {
    expect(toolDescription("Edit", { file_path: "/tmp/main.go" }))
      .toBe("Editing main.go");
  });

  test("Write with file_path returns basename", () => {
    expect(toolDescription("Write", { file_path: "/tmp/out.json" }))
      .toBe("Writing out.json");
  });

  test("Read without file_path returns verb only", () => {
    expect(toolDescription("Read", {})).toBe("Reading");
  });

  test("Bash with command returns truncated command", () => {
    expect(toolDescription("Bash", { command: "git status" }))
      .toBe("Running git status");
  });

  test("Bash truncates long commands to 30 chars", () => {
    const long = "a".repeat(50);
    expect(toolDescription("Bash", { command: long }))
      .toBe(`Running ${long.slice(0, 30)}`);
  });

  test("Bash without command returns fallback", () => {
    expect(toolDescription("Bash", {})).toBe("Running command");
  });

  test("Glob with pattern", () => {
    expect(toolDescription("Glob", { pattern: "**/*.tsx" }))
      .toBe("Searching **/*.tsx");
  });

  test("Grep with pattern", () => {
    expect(toolDescription("Grep", { pattern: "function main" }))
      .toBe("Searching function main");
  });

  test("Agent with description", () => {
    expect(toolDescription("Agent", { description: "Explore codebase structure" }))
      .toBe("Explore codebase structure");
  });

  test("Agent truncates long descriptions to 40 chars", () => {
    const long = "a".repeat(60);
    expect(toolDescription("Agent", { description: long }))
      .toBe(long.slice(0, 40));
  });

  test("WebFetch returns static string", () => {
    expect(toolDescription("WebFetch", {})).toBe("Fetching URL");
  });

  test("WebSearch with query", () => {
    expect(toolDescription("WebSearch", { query: "bun test runner" }))
      .toBe("Search: bun test runner");
  });

  test("AskUserQuestion with question", () => {
    expect(toolDescription("AskUserQuestion", { question: "Which framework do you prefer?" }))
      .toBe("Question: Which framework do you prefer?");
  });

  test("unknown tool returns tool name", () => {
    expect(toolDescription("TodoRead", {})).toBe("TodoRead");
  });

  test("undefined tool_name returns undefined", () => {
    expect(toolDescription(undefined, {})).toBeUndefined();
  });

  test("undefined tool_input still works", () => {
    expect(toolDescription("Bash", undefined)).toBe("Running command");
  });
});
