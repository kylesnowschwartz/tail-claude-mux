import { describe, expect, test } from "bun:test";

import { parseHookPayload } from "../src/contracts/parse-hook-payload";

describe("parseHookPayload — valid payloads", () => {
  test("accepts a minimal Claude Code payload", () => {
    const out = parseHookPayload({
      event: "SessionStart",
      session_id: "abc-123",
      cwd: "/Users/kyle/Code/project",
    });
    expect(out).not.toBeNull();
    expect(out!.event).toBe("SessionStart");
    expect(out!.session_id).toBe("abc-123");
    expect(out!.cwd).toBe("/Users/kyle/Code/project");
    // Optional fields are undefined, not absent — the explicit-copy
    // construction means every documented field is set.
    expect(out!.agent).toBeUndefined();
    expect(out!.tool_name).toBeUndefined();
  });

  test("accepts a full Claude Code PreToolUse payload", () => {
    const out = parseHookPayload({
      agent: "claude-code",
      event: "PreToolUse",
      session_id: "abc-123",
      cwd: "/p",
      tool_name: "Bash",
      tool_input: { command: "git status", description: "check" },
    });
    expect(out).not.toBeNull();
    expect(out!.tool_name).toBe("Bash");
    expect(out!.tool_input).toEqual({ command: "git status", description: "check" });
  });

  test("accepts a pi session_start payload", () => {
    const out = parseHookPayload({
      agent: "pi",
      event: "session_start",
      session_id: "pi-uuid",
      cwd: "/p",
      session_name: "My pi session",
    });
    expect(out).not.toBeNull();
    expect(out!.agent).toBe("pi");
    expect(out!.session_name).toBe("My pi session");
  });

  test("accepts a pi agent_end with valid stop_reason", () => {
    const out = parseHookPayload({
      agent: "pi",
      event: "agent_end",
      session_id: "pi-uuid",
      cwd: "/p",
      stop_reason: "error",
      error_message: "boom",
    });
    expect(out).not.toBeNull();
    expect(out!.stop_reason).toBe("error");
    expect(out!.error_message).toBe("boom");
  });

  test("accepts a pi session_shutdown with valid shutdown_reason", () => {
    const out = parseHookPayload({
      agent: "pi",
      event: "session_shutdown",
      session_id: "pi-uuid",
      cwd: "/p",
      shutdown_reason: "quit",
    });
    expect(out).not.toBeNull();
    expect(out!.shutdown_reason).toBe("quit");
  });

  test("accepts unknown event names — gatekeeping is the watcher's job", () => {
    const out = parseHookPayload({
      event: "FutureUnknownEvent",
      session_id: "id",
      cwd: "/p",
    });
    expect(out).not.toBeNull();
    expect(out!.event).toBe("FutureUnknownEvent");
  });
});

describe("parseHookPayload — required field rejection", () => {
  test("rejects null / undefined / primitive input", () => {
    expect(parseHookPayload(null)).toBeNull();
    expect(parseHookPayload(undefined)).toBeNull();
    expect(parseHookPayload("hello")).toBeNull();
    expect(parseHookPayload(42)).toBeNull();
    expect(parseHookPayload(true)).toBeNull();
  });

  test("rejects arrays", () => {
    expect(parseHookPayload([])).toBeNull();
    expect(parseHookPayload([{ event: "x", session_id: "y", cwd: "/z" }])).toBeNull();
  });

  test("rejects missing event", () => {
    expect(parseHookPayload({ session_id: "x", cwd: "/p" })).toBeNull();
  });

  test("rejects missing session_id", () => {
    expect(parseHookPayload({ event: "x", cwd: "/p" })).toBeNull();
  });

  test("rejects missing cwd", () => {
    expect(parseHookPayload({ event: "x", session_id: "y" })).toBeNull();
  });

  test("rejects empty-string required fields", () => {
    expect(parseHookPayload({ event: "", session_id: "y", cwd: "/p" })).toBeNull();
    expect(parseHookPayload({ event: "x", session_id: "", cwd: "/p" })).toBeNull();
    expect(parseHookPayload({ event: "x", session_id: "y", cwd: "" })).toBeNull();
  });

  test("rejects non-string required fields", () => {
    expect(parseHookPayload({ event: 1, session_id: "y", cwd: "/p" })).toBeNull();
    expect(parseHookPayload({ event: "x", session_id: null, cwd: "/p" })).toBeNull();
    expect(parseHookPayload({ event: "x", session_id: "y", cwd: { a: 1 } })).toBeNull();
  });

  test("rejects oversized event / session_id / cwd", () => {
    expect(parseHookPayload({ event: "x".repeat(200), session_id: "y", cwd: "/p" })).toBeNull();
    expect(parseHookPayload({ event: "x", session_id: "y".repeat(300), cwd: "/p" })).toBeNull();
    expect(parseHookPayload({ event: "x", session_id: "y", cwd: "/".repeat(70_000) })).toBeNull();
  });
});

describe("parseHookPayload — optional field laundering", () => {
  const base = { event: "x", session_id: "y", cwd: "/p" };

  test("drops malformed tool_input (array, primitive, null) without rejecting whole payload", () => {
    expect(parseHookPayload({ ...base, tool_input: [1, 2] })!.tool_input).toBeUndefined();
    expect(parseHookPayload({ ...base, tool_input: "string" })!.tool_input).toBeUndefined();
    expect(parseHookPayload({ ...base, tool_input: null })!.tool_input).toBeUndefined();
  });

  test("drops oversized tool_input by key count", () => {
    const huge: Record<string, unknown> = {};
    for (let i = 0; i < 400; i++) huge[`k${i}`] = i;
    expect(parseHookPayload({ ...base, tool_input: huge })!.tool_input).toBeUndefined();
  });

  test("drops invalid stop_reason / shutdown_reason", () => {
    expect(parseHookPayload({ ...base, stop_reason: "unknown" })!.stop_reason).toBeUndefined();
    expect(parseHookPayload({ ...base, shutdown_reason: "weird" })!.shutdown_reason).toBeUndefined();
  });

  test("drops non-boolean tool_is_error", () => {
    expect(parseHookPayload({ ...base, tool_is_error: "true" })!.tool_is_error).toBeUndefined();
    expect(parseHookPayload({ ...base, tool_is_error: 1 })!.tool_is_error).toBeUndefined();
  });

  test("drops oversized string fields", () => {
    expect(parseHookPayload({ ...base, tool_name: "x".repeat(200) })!.tool_name).toBeUndefined();
    expect(parseHookPayload({ ...base, agent: "y".repeat(100) })!.agent).toBeUndefined();
  });

  test("drops empty-string optional fields", () => {
    expect(parseHookPayload({ ...base, tool_name: "" })!.tool_name).toBeUndefined();
    expect(parseHookPayload({ ...base, agent: "" })!.agent).toBeUndefined();
  });
});

describe("parseHookPayload — pid and process_snapshot", () => {
  const base = { event: "x", session_id: "y", cwd: "/p" };

  test("accepts a positive-integer pid", () => {
    const out = parseHookPayload({ ...base, pid: 12345 });
    expect(out!.pid).toBe(12345);
  });

  test("drops pid <= 1 (kernel / init / malformed)", () => {
    expect(parseHookPayload({ ...base, pid: 0 })!.pid).toBeUndefined();
    expect(parseHookPayload({ ...base, pid: 1 })!.pid).toBeUndefined();
    expect(parseHookPayload({ ...base, pid: -42 })!.pid).toBeUndefined();
  });

  test("drops non-integer pid", () => {
    expect(parseHookPayload({ ...base, pid: 3.14 })!.pid).toBeUndefined();
    expect(parseHookPayload({ ...base, pid: "1234" })!.pid).toBeUndefined();
    expect(parseHookPayload({ ...base, pid: null })!.pid).toBeUndefined();
  });

  test("accepts a bounded process_snapshot string", () => {
    const snap = "  100   1 /sbin/launchd\n  200 100 node /path/claude";
    expect(parseHookPayload({ ...base, process_snapshot: snap })!.process_snapshot).toBe(snap);
  });

  test("drops oversized process_snapshot", () => {
    const huge = "x".repeat(300_000);
    expect(parseHookPayload({ ...base, process_snapshot: huge })!.process_snapshot).toBeUndefined();
  });

  test("drops non-string process_snapshot", () => {
    expect(parseHookPayload({ ...base, process_snapshot: 123 })!.process_snapshot).toBeUndefined();
    expect(parseHookPayload({ ...base, process_snapshot: { foo: 1 } })!.process_snapshot).toBeUndefined();
  });
});

describe("parseHookPayload — output shape integrity", () => {
  test("output contains only documented fields — unknown input keys are dropped", () => {
    const out = parseHookPayload({
      event: "x",
      session_id: "y",
      cwd: "/p",
      unknown_future_field: "should-not-appear",
      __proto__: { polluted: true },
    });
    expect(out).not.toBeNull();
    // The explicit-copy construction means future/unknown keys can't leak into
    // the type-asserted downstream code.
    expect((out as unknown as Record<string, unknown>).unknown_future_field).toBeUndefined();
    // Prototype pollution defense: object literals don't actually pollute
    // Object.prototype via __proto__ in modern engines, but verify the output
    // is a clean own-property bag.
    expect((Object.prototype as Record<string, unknown>).polluted).toBeUndefined();
  });

  test("output is structurally a HookPayload — required fields verbatim", () => {
    const out = parseHookPayload({
      event: "PreToolUse",
      session_id: "sid",
      cwd: "/cwd",
    });
    expect(out).not.toBeNull();
    expect(out!.event).toBe("PreToolUse");
    expect(out!.session_id).toBe("sid");
    expect(out!.cwd).toBe("/cwd");
  });
});
