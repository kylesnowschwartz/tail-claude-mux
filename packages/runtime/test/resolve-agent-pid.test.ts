import { describe, expect, test } from "bun:test";

import {
  parseProcessSnapshot,
  resolveAgentSessionPid,
  resolveAgentSessionPidFromSnapshot,
} from "../src/agents/resolve-agent-pid";

// Path-segment aware: matches `claude` or `claude-code` when it appears at
// the start of the command, after a path separator, with end-of-string,
// whitespace, or another `/` immediately after. Avoids false positives on
// directory names that happen to contain `claude` (e.g. `meta-claude`).
const CLAUDE_RE = /(?:^|\/)claude(?:-code)?(?=\s|\/|$)/i;

describe("parseProcessSnapshot", () => {
  test("parses leading-space ps output", () => {
    const out = parseProcessSnapshot(
      [
        "  100     1 /bin/launchd",
        "  200   100 node /opt/homebrew/bin/claude --foo",
        "  300   200 /bin/sh -c hook.sh PreToolUse",
        "  400   300 /bin/bash hook.sh",
      ].join("\n"),
    );
    expect(out.size).toBe(4);
    expect(out.get(200)?.command).toBe("node /opt/homebrew/bin/claude --foo");
    expect(out.get(300)?.ppid).toBe(200);
  });

  test("ignores blank lines and malformed entries", () => {
    const out = parseProcessSnapshot(
      [
        "",
        "  not a number",
        "  500   600 /bin/example",
        "junk",
        "",
      ].join("\n"),
    );
    expect(out.size).toBe(1);
    expect(out.get(500)?.command).toBe("/bin/example");
  });

  test("rejects non-positive pids", () => {
    const out = parseProcessSnapshot(["  0 0 swapper", "  -1 0 weird"].join("\n"));
    expect(out.size).toBe(0);
  });
});

describe("resolveAgentSessionPid", () => {
  const snapshot = parseProcessSnapshot(
    [
      "  100     1 /sbin/launchd",
      "  200   100 node /opt/homebrew/bin/claude --foo",
      "  300   200 /bin/sh -c hook.sh PreToolUse",
      "  400   300 /bin/bash hook.sh",
    ].join("\n"),
  );

  test("walks from hook.sh up to claude", () => {
    expect(resolveAgentSessionPid(400, CLAUDE_RE, snapshot)).toBe(200);
  });

  test("walks from the sh -c wrapper to claude", () => {
    expect(resolveAgentSessionPid(300, CLAUDE_RE, snapshot)).toBe(200);
  });

  test("returns claude itself when reported pid IS claude", () => {
    expect(resolveAgentSessionPid(200, CLAUDE_RE, snapshot)).toBe(200);
  });

  test("returns input pid unchanged when no match in ancestry", () => {
    const noClaude = parseProcessSnapshot(
      [
        "  100     1 /sbin/launchd",
        "  200   100 /bin/sh -c something else",
        "  300   200 /bin/bash hook.sh",
      ].join("\n"),
    );
    expect(resolveAgentSessionPid(300, CLAUDE_RE, noClaude)).toBe(300);
  });

  test("returns input pid unchanged for pid <= 1", () => {
    expect(resolveAgentSessionPid(0, CLAUDE_RE, snapshot)).toBe(0);
    expect(resolveAgentSessionPid(1, CLAUDE_RE, snapshot)).toBe(1);
    expect(resolveAgentSessionPid(-5, CLAUDE_RE, snapshot)).toBe(-5);
  });

  test("returns input pid unchanged when reported pid not in snapshot", () => {
    expect(resolveAgentSessionPid(999, CLAUDE_RE, snapshot)).toBe(999);
  });

  test("is cycle-safe (self-parent)", () => {
    const cyclic = parseProcessSnapshot("  500   500 self-parent loop");
    expect(resolveAgentSessionPid(500, CLAUDE_RE, cyclic)).toBe(500);
  });

  test("is cycle-safe (two-node cycle)", () => {
    const cyclic = parseProcessSnapshot(
      ["  600   700 a", "  700   600 b"].join("\n"),
    );
    expect(resolveAgentSessionPid(600, CLAUDE_RE, cyclic)).toBe(600);
  });

  test("terminates at ppid <= 1 without false match", () => {
    const snap = parseProcessSnapshot(
      [
        "  100     1 /sbin/launchd",
        "  200   100 /bin/zsh",
      ].join("\n"),
    );
    expect(resolveAgentSessionPid(200, CLAUDE_RE, snap)).toBe(200);
  });

  test("matches claude even when wrapped behind 'node'", () => {
    // Real-world: macOS install runs claude as `node /path/to/claude/cli.js`.
    // The regex matches the word `claude` anywhere in the command string.
    const realistic = parseProcessSnapshot(
      [
        "  100     1 /sbin/launchd",
        "  200   100 node /Users/kyle/.nvm/versions/node/v20.18.0/lib/node_modules/@anthropic-ai/claude-code/cli.js",
        "  300   200 /bin/sh -c /Users/kyle/Code/meta-claude/tail-claude-mux/scripts/hook.sh PreToolUse",
      ].join("\n"),
    );
    expect(resolveAgentSessionPid(300, CLAUDE_RE, realistic)).toBe(200);
  });
});

describe("resolveAgentSessionPidFromSnapshot — parse+walk integration", () => {
  test("end-to-end resolution from raw ps text", () => {
    const ps = [
      "  100     1 /sbin/launchd",
      "  200   100 node /opt/homebrew/bin/claude",
      "  300   200 /bin/sh -c hook.sh",
      "  400   300 /bin/bash hook.sh",
    ].join("\n");
    expect(resolveAgentSessionPidFromSnapshot(400, CLAUDE_RE, ps)).toBe(200);
  });
});
