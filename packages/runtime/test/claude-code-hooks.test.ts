import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync, unlinkSync, mkdirSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { ClaudeCodeHookAdapter, toolDescription, classifySessionStatus } from "../src/agents/watchers/claude-code-hooks";
import type { AgentEvent } from "../src/contracts/agent";
import type { AgentWatcherContext, HookPayload } from "../src/contracts/agent-watcher";
import { isHookReceiver } from "../src/contracts/agent-watcher";

function makeCtx(
  sessionMap: Record<string, string> = {},
  pidMap: Record<number, string> = {},
): AgentWatcherContext & { events: AgentEvent[] } {
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
    resolveSessionByPid(pid: number) {
      return pidMap[pid] ?? null;
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

  // --- Agent discriminator ---

  test("payload with agent: 'pi' is ignored", () => {
    adapter.handleHook({
      agent: "pi",
      event: "session_start",
      session_id: "sess-pi-1",
      cwd: "/tmp/myproject",
    });

    expect(ctx.events).toHaveLength(0);
  });

  test("payload with agent: 'claude-code' still dispatches", () => {
    adapter.handleHook({
      agent: "claude-code",
      event: "SessionStart",
      session_id: "sess-1",
      cwd: "/tmp/myproject",
    });

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].status).toBe("idle");
  });

  test("payload without agent field still dispatches (legacy)", () => {
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].agent).toBe("claude-code");
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

// --- sessions/<pid>.json subagent enrichment ---

describe("ClaudeCodeHookAdapter subagent enrichment", () => {
  let sessionsDir: string;
  let adapter: ClaudeCodeHookAdapter;
  let ctx: ReturnType<typeof makeCtx>;

  function writeSession(pid: number, payload: Record<string, unknown>): void {
    writeFileSync(join(sessionsDir, `${pid}.json`), JSON.stringify(payload));
  }

  beforeEach(() => {
    sessionsDir = mkdtempSync(join(tmpdir(), "cc-sessions-"));
    adapter = new ClaudeCodeHookAdapter(undefined, sessionsDir);
    ctx = makeCtx({ "/tmp/myproject": "myproject" });
    // These tests exercise subagent enrichment, not routing — once
    // refreshSubagent resolves a pid from the sessions file, later hooks route
    // by it, so make pid routing always resolve to the project session.
    ctx.resolveSessionByPid = () => "myproject";
    adapter.start(ctx);
  });

  afterEach(() => {
    adapter.stop();
    rmSync(sessionsDir, { recursive: true, force: true });
  });

  test("emits subagent from sessions/<pid>.json when agent field is present", () => {
    writeSession(42000, {
      pid: 42000,
      sessionId: "sess-1",
      procStart: "Sat May 16 09:00:00 2026",
      agent: "rb-orchestrator",
    });

    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].subagent).toBe("rb-orchestrator");
  });

  test("omits subagent when sessions/<pid>.json lacks an agent field", () => {
    writeSession(42001, {
      pid: 42001,
      sessionId: "sess-1",
      procStart: "Sat May 16 09:00:00 2026",
    });

    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].subagent).toBeUndefined();
  });

  test("omits subagent when no sessions file matches the threadId", () => {
    adapter.handleHook(hook("UserPromptSubmit", "sess-orphan", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].subagent).toBeUndefined();
  });

  test("re-reads file across events so subagent transitions reflect", () => {
    writeSession(42002, {
      pid: 42002,
      sessionId: "sess-1",
      procStart: "Sat May 16 09:00:00 2026",
      agent: "rb-orchestrator",
    });

    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    expect(ctx.events[0].subagent).toBe("rb-orchestrator");

    // Subagent finishes — agent field cleared by CC
    writeSession(42002, {
      pid: 42002,
      sessionId: "sess-1",
      procStart: "Sat May 16 09:00:00 2026",
    });

    // Re-emission: a new tool description forces an emit
    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Read",
      tool_input: { file_path: "/tmp/x.ts" },
    }));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].subagent).toBeUndefined();
  });

  test("detects PID reuse via sessionId mismatch", () => {
    writeSession(42003, {
      pid: 42003,
      sessionId: "sess-old",
      procStart: "Sat May 16 09:00:00 2026",
      agent: "rb-orchestrator",
    });

    adapter.handleHook(hook("UserPromptSubmit", "sess-old", "/tmp/myproject"));
    expect(ctx.events[0].subagent).toBe("rb-orchestrator");

    // PID 42003 reused by a different CC process for sess-new
    writeSession(42003, {
      pid: 42003,
      sessionId: "sess-new",
      procStart: "Sat May 16 10:00:00 2026",
      agent: "doc-writer",
    });

    adapter.handleHook(hook("UserPromptSubmit", "sess-new", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(2);
    expect(ctx.events[1].threadId).toBe("sess-new");
    expect(ctx.events[1].subagent).toBe("doc-writer");
  });

  test("file read errors do not propagate (subagent stays undefined)", () => {
    // No file written — resolvePidFromSessions returns undefined, read fails
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].subagent).toBeUndefined();
  });

  test("malformed sessions file does not throw", () => {
    writeFileSync(join(sessionsDir, "42004.json"), "{not json");

    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));

    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].subagent).toBeUndefined();
  });

  test("disappearance of sessions file mid-flight leaves prior subagent on emitted state intact via tracker", () => {
    // (Watcher-level) re-emission with file gone should result in undefined.
    // The preservation behaviour lives in the tracker; this test asserts the
    // watcher contract: on next emit after file removal, subagent is undefined.
    writeSession(42005, {
      pid: 42005,
      sessionId: "sess-1",
      procStart: "Sat May 16 09:00:00 2026",
      agent: "rb-orchestrator",
    });

    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"));
    expect(ctx.events[0].subagent).toBe("rb-orchestrator");

    unlinkSync(join(sessionsDir, "42005.json"));

    adapter.handleHook(hook("PreToolUse", "sess-1", "/tmp/myproject", {
      tool_name: "Read",
      tool_input: { file_path: "/tmp/x.ts" },
    }));

    expect(ctx.events[1].subagent).toBeUndefined();
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

  test("Bash truncates long commands to 30 cells with ellipsis", () => {
    const long = "a".repeat(50);
    // truncateToWidth reserves one cell for the ellipsis, so a 50-char ASCII
    // command with budget 30 yields 29 chars + "…" = 30 cells.
    expect(toolDescription("Bash", { command: long }))
      .toBe(`Running ${"a".repeat(29)}…`);
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

  test("Agent truncates long descriptions to 40 cells with ellipsis", () => {
    const long = "a".repeat(60);
    expect(toolDescription("Agent", { description: long }))
      .toBe(`${"a".repeat(39)}…`);
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

describe("ClaudeCodeHookAdapter — pid resolution", () => {
  let adapter: ClaudeCodeHookAdapter;
  let ctx: ReturnType<typeof makeCtx>;

  beforeEach(() => {
    adapter = new ClaudeCodeHookAdapter();
    ctx = makeCtx({ "/tmp/myproject": "myproject" });
    // Pid is the routing channel; route any resolved claude pid to the project.
    ctx.resolveSessionByPid = () => "myproject";
    adapter.start(ctx);
  });

  afterEach(() => {
    adapter.stop();
  });

  /** Helper to build a process_snapshot where pid 400 (the hook) is a
   *  descendant of pid 200 (the long-lived claude). */
  function snapshotWithClaudeAt200(): string {
    return [
      "  100     1 /sbin/launchd",
      "  200   100 node /Users/kyle/.nvm/versions/node/v20/lib/node_modules/@anthropic-ai/claude-code/cli.js",
      "  300   200 /bin/sh -c hook.sh PreToolUse",
      "  400   300 /bin/bash /Users/kyle/Code/meta-claude/tail-claude-mux/scripts/hook.sh PreToolUse",
    ].join("\n");
  }

  test("resolves wrapper-shell pid to the long-lived claude pid", () => {
    adapter.handleHook(
      hook("SessionStart", "sess-1", "/tmp/myproject", {
        pid: 400,
        process_snapshot: snapshotWithClaudeAt200(),
      }),
    );
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].pid).toBe(200);
  });

  test("uses payload pid directly when it already matches claude in the snapshot", () => {
    adapter.handleHook(
      hook("SessionStart", "sess-1", "/tmp/myproject", {
        pid: 200,
        process_snapshot: snapshotWithClaudeAt200(),
      }),
    );
    expect(ctx.events[0].pid).toBe(200);
  });

  test("drops pid when walker gives up and reported pid is not claude itself", () => {
    // Walker can't reach claude in this snapshot.
    const noClaude = [
      "  100     1 /sbin/launchd",
      "  200   100 /bin/bash",
      "  400   200 /bin/bash /path/hook.sh",
    ].join("\n");
    adapter.handleHook(
      hook("SessionStart", "sess-1", "/tmp/myproject", {
        pid: 400,
        process_snapshot: noClaude,
      }),
    );
    // The wrapper pid would false-fire the liveness sweep, so we drop it.
    expect(ctx.events[0].pid).toBeUndefined();
  });

  test("subsequent hooks reuse the resolved pid (resolved once per thread)", () => {
    adapter.handleHook(
      hook("SessionStart", "sess-1", "/tmp/myproject", {
        pid: 400,
        process_snapshot: snapshotWithClaudeAt200(),
      }),
    );
    // Second hook with a totally different (e.g. stale) pid+snapshot should
    // not re-resolve — pid is per-thread, captured once.
    adapter.handleHook(
      hook("PreToolUse", "sess-1", "/tmp/myproject", {
        pid: 999,
        process_snapshot: "",
        tool_name: "Bash",
        tool_input: { command: "ls" },
      }),
    );
    const last = ctx.events[ctx.events.length - 1];
    expect(last.pid).toBe(200);
  });

  test("works without pid/process_snapshot (legacy payloads)", () => {
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject"));
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].pid).toBeUndefined();
  });
});

describe("ClaudeCodeHookAdapter — pid-first session routing", () => {
  let adapter: ClaudeCodeHookAdapter;

  beforeEach(() => {
    adapter = new ClaudeCodeHookAdapter();
  });
  afterEach(() => adapter.stop());

  function snapshotClaudeAt200(): string {
    return [
      "  100     1 /sbin/launchd",
      "  200   100 node /path/@anthropic-ai/claude-code/cli.js",
      "  400   200 /bin/sh -c hook.sh PreToolUse",
    ].join("\n");
  }

  test("routes by resolved pid, not cwd, when a pid is available", () => {
    const ctx = makeCtx({ "/tmp/myproject": "cwd-session" }, { 200: "pid-session" });
    adapter.start(ctx);
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject", {
      pid: 400,
      process_snapshot: snapshotClaudeAt200(),
    }));
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].session).toBe("pid-session"); // pid wins over cwd
  });

  test("drops the event when pid resolves but the pane lookup fails", () => {
    const ctx = makeCtx({ "/tmp/myproject": "cwd-session" }, {}); // empty pidMap
    adapter.start(ctx);
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject", {
      pid: 400,
      process_snapshot: snapshotClaudeAt200(),
    }));
    expect(ctx.events).toHaveLength(0); // no silent cwd fallback
  });

  test("falls back to cwd routing when no pid is resolved", () => {
    const ctx = makeCtx({ "/tmp/myproject": "cwd-session" }, {});
    adapter.start(ctx);
    adapter.handleHook(hook("SessionStart", "sess-1", "/tmp/myproject")); // no pid
    expect(ctx.events).toHaveLength(1);
    expect(ctx.events[0].session).toBe("cwd-session");
  });
});

describe("ClaudeCodeHookAdapter — StopFailure", () => {
  test("StopFailure maps to error, clearing the running spinner", () => {
    const ctx = makeCtx({ "/tmp/myproject": "myproject" });
    const adapter = new ClaudeCodeHookAdapter();
    adapter.start(ctx);
    adapter.handleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject")); // → running
    adapter.handleHook(hook("StopFailure", "sess-1", "/tmp/myproject"));
    const last = ctx.events[ctx.events.length - 1];
    expect(last.status).toBe("error");
    adapter.stop();
  });
});

describe("classifySessionStatus", () => {
  const now = 1_000_000_000_000;
  const HUNG = 30 * 60 * 1000;

  test("absent file → null (no signal)", () => {
    expect(classifySessionStatus(undefined, "t", now)).toBeNull();
  });

  test("status absent (sdk-cli) → null", () => {
    expect(classifySessionStatus({ sessionId: "t" }, "t", now)).toBeNull();
  });

  test("busy + fresh updatedAt → working", () => {
    expect(classifySessionStatus({ sessionId: "t", status: "busy", updatedAt: now - 1000 }, "t", now)).toBe("working");
  });

  test("busy + updatedAt older than hung ceiling → ended", () => {
    expect(classifySessionStatus({ sessionId: "t", status: "busy", updatedAt: now - HUNG - 1 }, "t", now)).toBe("ended");
  });

  test("busy with no updatedAt → working (no staleness evidence)", () => {
    expect(classifySessionStatus({ sessionId: "t", status: "busy" }, "t", now)).toBe("working");
  });

  test("idle / waiting → ended", () => {
    expect(classifySessionStatus({ sessionId: "t", status: "idle" }, "t", now)).toBe("ended");
    expect(classifySessionStatus({ sessionId: "t", status: "waiting" }, "t", now)).toBe("ended");
  });

  test("sessionId mismatch (pid reused) → ended even if busy", () => {
    expect(classifySessionStatus({ sessionId: "other", status: "busy", updatedAt: now }, "t", now)).toBe("ended");
  });
});

describe("ClaudeCodeHookAdapter.probeLiveStatus", () => {
  let sessionsDir: string;
  let adapter: ClaudeCodeHookAdapter;

  beforeEach(() => {
    sessionsDir = mkdtempSync(join(tmpdir(), "cc-probe-"));
    adapter = new ClaudeCodeHookAdapter(undefined, sessionsDir);
  });
  afterEach(() => {
    adapter.stop();
    rmSync(sessionsDir, { recursive: true, force: true });
  });

  test("reads sessions/<pid>.json and classifies an idle session as ended", () => {
    writeFileSync(join(sessionsDir, "555.json"), JSON.stringify({ pid: 555, sessionId: "t-1", status: "idle", updatedAt: Date.now() }));
    expect(adapter.probeLiveStatus(555, "t-1")).toBe("ended");
  });

  test("classifies a busy fresh session as working", () => {
    writeFileSync(join(sessionsDir, "556.json"), JSON.stringify({ pid: 556, sessionId: "t-2", status: "busy", updatedAt: Date.now() }));
    expect(adapter.probeLiveStatus(556, "t-2")).toBe("working");
  });

  test("missing file → null", () => {
    expect(adapter.probeLiveStatus(999, "t-3")).toBeNull();
  });

  // OSC-title cross-check: the session file is authoritative; the pane title
  // only fills the gap when the file yields no verdict (sdk-cli / absent).
  test("file null + braille title → working (title fills the gap)", () => {
    // No session file written for this pid → file verdict is null.
    expect(adapter.probeLiveStatus(701, "t-osc-1", "⠋ Reading config.ts")).toBe("working");
  });

  test("file null + sparkle title → ended (title fills the gap)", () => {
    expect(adapter.probeLiveStatus(702, "t-osc-2", "✳ ~/Code/project")).toBe("ended");
  });

  test("file null + plain title → null (no signal from either source)", () => {
    expect(adapter.probeLiveStatus(703, "t-osc-3", "~/Code/project")).toBeNull();
  });

  test("file busy wins over a sparkle title — definitive file verdict is never overridden", () => {
    writeFileSync(join(sessionsDir, "704.json"), JSON.stringify({ pid: 704, sessionId: "t-osc-4", status: "busy", updatedAt: Date.now() }));
    expect(adapter.probeLiveStatus(704, "t-osc-4", "✳ idle-looking title")).toBe("working");
  });

  test("file idle wins over a braille title — file ended is never overridden to working", () => {
    writeFileSync(join(sessionsDir, "705.json"), JSON.stringify({ pid: 705, sessionId: "t-osc-5", status: "idle", updatedAt: Date.now() }));
    expect(adapter.probeLiveStatus(705, "t-osc-5", "⠋ busy-looking title")).toBe("ended");
  });
});

describe("ClaudeCodeHookAdapter — cold-start seed routes by pid", () => {
  let projectsDir: string;
  let sessionsDir: string;
  let adapter: ClaudeCodeHookAdapter;

  beforeEach(() => {
    projectsDir = mkdtempSync(join(tmpdir(), "cc-proj-"));
    sessionsDir = mkdtempSync(join(tmpdir(), "cc-sess-"));
    adapter = new ClaudeCodeHookAdapter(projectsDir, sessionsDir);
  });
  afterEach(() => {
    adapter.stop();
    rmSync(projectsDir, { recursive: true, force: true });
    rmSync(sessionsDir, { recursive: true, force: true });
  });

  test("seeded running entry routes by pid (not cwd) and carries the resolved pid", async () => {
    const threadId = "seed-thread-1";
    // A project dir holding one running conversation transcript.
    const projDir = join(projectsDir, "-tmp-myproject");
    mkdirSync(projDir, { recursive: true });
    writeFileSync(
      join(projDir, `${threadId}.jsonl`),
      JSON.stringify({ message: { role: "user", content: [{ type: "text", text: "do the thing" }] } }) + "\n",
    );
    // sessions/<pid>.json lets the seed resolve the long-lived pid from threadId.
    writeFileSync(join(sessionsDir, "4242.json"), JSON.stringify({ pid: 4242, sessionId: threadId }));

    // cwd resolves to one session, pid to another — pid must win.
    const ctx = makeCtx({}, { 4242: "pid-session" });
    ctx.resolveSession = () => "cwd-session";
    adapter.start(ctx);

    // seedFromJsonl is async and not awaited by start(); let it settle.
    await new Promise((r) => setTimeout(r, 50));

    const seeded = ctx.events.find((e) => e.threadId === threadId);
    expect(seeded).toBeDefined();
    expect(seeded!.session).toBe("pid-session");
    expect(seeded!.pid).toBe(4242);
  });
});
