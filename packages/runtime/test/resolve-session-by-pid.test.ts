/**
 * Tests for pid-based session resolution.
 *
 * The watcher receives a hook carrying the agent's pid; the server walks
 * upward from that pid through the process tree until it lands on a pid that
 * is some tmux pane's shell pid, and attributes the hook to that pane's
 * session. This is the routing path that replaces the cwd-based
 * `resolveSession` for live hook delivery.
 *
 * Tested as a pure function over snapshots — no shell, no tmux.
 */

import { describe, expect, test } from "bun:test";

import {
  buildPanePidIndex,
  resolveSessionByPid,
} from "../src/agents/resolve-session-by-pid";

import { parseProcessSnapshot } from "../src/agents/resolve-agent-pid";

describe("buildPanePidIndex", () => {
  test("parses tmux list-panes output into pid → session map", () => {
    // Production format: "#{session_name}|#{pane_pid}"
    const out = buildPanePidIndex(
      ["pi-dev|5055", "pi-dev|89112", "ai-engineering-domain|12345"].join("\n"),
    );
    expect(out.size).toBe(3);
    expect(out.get(5055)).toBe("pi-dev");
    expect(out.get(89112)).toBe("pi-dev");
    expect(out.get(12345)).toBe("ai-engineering-domain");
  });

  test("ignores blank lines and malformed entries", () => {
    const out = buildPanePidIndex(
      ["", "pi-dev|5055", "junk-no-pipe", "name|notanumber", "  "].join("\n"),
    );
    expect(out.size).toBe(1);
    expect(out.get(5055)).toBe("pi-dev");
  });

  test("rejects non-positive pids", () => {
    const out = buildPanePidIndex(["s|0", "s|-1"].join("\n"));
    expect(out.size).toBe(0);
  });
});

describe("resolveSessionByPid", () => {
  // Realistic chain: tmux pane shell (89112) → bash spawning pi (89539)
  //                 → pi process (89555).
  // Mirrors the live ps tree captured during bug repro.
  const snapshot = parseProcessSnapshot(
    [
      "    1     0 launchd",
      "89112     1 bash",
      "89539 89112 /bin/sh /usr/bin/command pi",
      "89555 89539 pi",
    ].join("\n"),
  );

  const panePidIndex = buildPanePidIndex(["pi-dev|89112"].join("\n"));

  test("walks up from pi pid to its pane and returns the session", () => {
    expect(resolveSessionByPid(89555, panePidIndex, snapshot)).toBe("pi-dev");
  });

  test("walks through wrapper layers", () => {
    // Same chain, lookup from the middle wrapper still resolves.
    expect(resolveSessionByPid(89539, panePidIndex, snapshot)).toBe("pi-dev");
  });

  test("returns null when pid is not in snapshot", () => {
    // pid not present (process exited before lookup) — caller's failure
    // mode is "drop the hook", not "guess".
    expect(resolveSessionByPid(99999, panePidIndex, snapshot)).toBeNull();
  });

  test("returns null when ancestor chain has no pane_pid", () => {
    // Process tree is intact but the chain never crosses a pane shell. This
    // happens for processes that aren't inside any tmux pane (e.g. agents
    // launched from a system service).
    const orphanSnapshot = parseProcessSnapshot(
      [
        "    1     0 launchd",
        "55555     1 some-daemon",
        "55556 55555 pi",
      ].join("\n"),
    );
    expect(resolveSessionByPid(55556, panePidIndex, orphanSnapshot)).toBeNull();
  });

  test("multi-session: each pane resolves to its own session", () => {
    const multiIndex = buildPanePidIndex(
      ["pi-dev|89112", "ai-eng|5055"].join("\n"),
    );
    const multiSnapshot = parseProcessSnapshot(
      [
        "89112     1 bash",
        "89555 89112 pi",
        " 5055     1 bash",
        " 7762  5055 bun",
      ].join("\n"),
    );
    expect(resolveSessionByPid(89555, multiIndex, multiSnapshot)).toBe(
      "pi-dev",
    );
    expect(resolveSessionByPid(7762, multiIndex, multiSnapshot)).toBe("ai-eng");
  });

  test("invalid pid (0, negative, NaN) returns null", () => {
    expect(resolveSessionByPid(0, panePidIndex, snapshot)).toBeNull();
    expect(resolveSessionByPid(-1, panePidIndex, snapshot)).toBeNull();
    expect(resolveSessionByPid(NaN, panePidIndex, snapshot)).toBeNull();
  });

  test("cycle-safe: pid that points to itself does not loop", () => {
    // Defensive: a corrupt snapshot where a pid is its own parent must not
    // hang the resolver.
    const cycleSnapshot = parseProcessSnapshot(
      ["44444 44444 weird-self-cycle"].join("\n"),
    );
    const idx = buildPanePidIndex(["s|99999"].join("\n"));
    expect(resolveSessionByPid(44444, idx, cycleSnapshot)).toBeNull();
  });
});

describe("live bug scenario reproduction", () => {
  // Coordinates from the live system at bug repro time:
  //   pi-dev session has panes with pane_pids including 89112 (the bash
  //   hosting pi 89555). pi 89555 emits hooks; pid-based resolution should
  //   return "pi-dev" regardless of where the active pane has navigated.
  test("pi 89555 resolves to pi-dev even when active pane cwd diverges", () => {
    const snapshot = parseProcessSnapshot(
      [
        "89112     1 bash",
        "89539 89112 /bin/sh /usr/bin/command pi",
        "89555 89539 pi",
      ].join("\n"),
    );
    const panePidIndex = buildPanePidIndex(
      [
        // Only pi-dev runs pi here. The fact that pi-dev's active pane is
        // currently in /Users/kyle/Code/my-projects/kylesnowschwartz.github.io
        // is irrelevant — we route by pid, not by path.
        "pi-dev|89112",
      ].join("\n"),
    );

    expect(resolveSessionByPid(89555, panePidIndex, snapshot)).toBe("pi-dev");
  });
});
