// Ported from packages/runtime/test/resolve-agent-pid.test.ts and
// packages/runtime/test/resolve-session-by-pid.test.ts. Subtest names match
// the TS test names one-to-one.
package procwalk

import (
	"regexp"
	"strings"
	"testing"
)

// Path-segment aware: matches `claude` or `claude-code` when it appears at
// the start of the command, after a path separator, with end-of-string,
// whitespace, or another `/` immediately after. Avoids false positives on
// directory names that happen to contain `claude` (e.g. `meta-claude`).
//
// The TS suite uses the lookahead form /(?:^|\/)claude(?:-code)?(?=\s|\/|$)/i;
// RE2 has no lookahead, so this is the consuming equivalent used in the Go
// consumer.
var claudeRE = regexp.MustCompile(`(?i)(?:^|/)claude(?:-code)?($|[\s/])`)

func lines(ls ...string) string {
	return strings.Join(ls, "\n")
}

func TestParseProcessSnapshot(t *testing.T) {
	t.Run("parses leading-space ps output", func(t *testing.T) {
		out := ParseProcessSnapshot(lines(
			"  100     1 /bin/launchd",
			"  200   100 node /opt/homebrew/bin/claude --foo",
			"  300   200 /bin/sh -c hook.sh PreToolUse",
			"  400   300 /bin/bash hook.sh",
		))
		if len(out) != 4 {
			t.Fatalf("len = %d, want 4", len(out))
		}
		if got := out[200].Command; got != "node /opt/homebrew/bin/claude --foo" {
			t.Errorf("out[200].Command = %q", got)
		}
		if got := out[300].PPID; got != 200 {
			t.Errorf("out[300].PPID = %d, want 200", got)
		}
	})

	t.Run("ignores blank lines and malformed entries", func(t *testing.T) {
		out := ParseProcessSnapshot(lines(
			"",
			"  not a number",
			"  500   600 /bin/example",
			"junk",
			"",
		))
		if len(out) != 1 {
			t.Fatalf("len = %d, want 1", len(out))
		}
		if got := out[500].Command; got != "/bin/example" {
			t.Errorf("out[500].Command = %q", got)
		}
	})

	t.Run("rejects non-positive pids", func(t *testing.T) {
		out := ParseProcessSnapshot(lines("  0 0 swapper", "  -1 0 weird"))
		if len(out) != 0 {
			t.Fatalf("len = %d, want 0", len(out))
		}
	})
}

func TestResolveAgentSessionPid(t *testing.T) {
	snapshot := ParseProcessSnapshot(lines(
		"  100     1 /sbin/launchd",
		"  200   100 node /opt/homebrew/bin/claude --foo",
		"  300   200 /bin/sh -c hook.sh PreToolUse",
		"  400   300 /bin/bash hook.sh",
	))

	t.Run("walks from hook.sh up to claude", func(t *testing.T) {
		if got := ResolveAgentSessionPid(400, claudeRE, snapshot); got != 200 {
			t.Errorf("got %d, want 200", got)
		}
	})

	t.Run("walks from the sh -c wrapper to claude", func(t *testing.T) {
		if got := ResolveAgentSessionPid(300, claudeRE, snapshot); got != 200 {
			t.Errorf("got %d, want 200", got)
		}
	})

	t.Run("returns claude itself when reported pid IS claude", func(t *testing.T) {
		if got := ResolveAgentSessionPid(200, claudeRE, snapshot); got != 200 {
			t.Errorf("got %d, want 200", got)
		}
	})

	t.Run("returns input pid unchanged when no match in ancestry", func(t *testing.T) {
		noClaude := ParseProcessSnapshot(lines(
			"  100     1 /sbin/launchd",
			"  200   100 /bin/sh -c something else",
			"  300   200 /bin/bash hook.sh",
		))
		if got := ResolveAgentSessionPid(300, claudeRE, noClaude); got != 300 {
			t.Errorf("got %d, want 300", got)
		}
	})

	t.Run("returns input pid unchanged for pid <= 1", func(t *testing.T) {
		if got := ResolveAgentSessionPid(0, claudeRE, snapshot); got != 0 {
			t.Errorf("pid 0: got %d, want 0", got)
		}
		if got := ResolveAgentSessionPid(1, claudeRE, snapshot); got != 1 {
			t.Errorf("pid 1: got %d, want 1", got)
		}
		if got := ResolveAgentSessionPid(-5, claudeRE, snapshot); got != -5 {
			t.Errorf("pid -5: got %d, want -5", got)
		}
	})

	t.Run("returns input pid unchanged when reported pid not in snapshot", func(t *testing.T) {
		if got := ResolveAgentSessionPid(999, claudeRE, snapshot); got != 999 {
			t.Errorf("got %d, want 999", got)
		}
	})

	t.Run("is cycle-safe (self-parent)", func(t *testing.T) {
		cyclic := ParseProcessSnapshot("  500   500 self-parent loop")
		if got := ResolveAgentSessionPid(500, claudeRE, cyclic); got != 500 {
			t.Errorf("got %d, want 500", got)
		}
	})

	t.Run("is cycle-safe (two-node cycle)", func(t *testing.T) {
		cyclic := ParseProcessSnapshot(lines("  600   700 a", "  700   600 b"))
		if got := ResolveAgentSessionPid(600, claudeRE, cyclic); got != 600 {
			t.Errorf("got %d, want 600", got)
		}
	})

	t.Run("terminates at ppid <= 1 without false match", func(t *testing.T) {
		snap := ParseProcessSnapshot(lines(
			"  100     1 /sbin/launchd",
			"  200   100 /bin/zsh",
		))
		if got := ResolveAgentSessionPid(200, claudeRE, snap); got != 200 {
			t.Errorf("got %d, want 200", got)
		}
	})

	t.Run("matches claude even when wrapped behind 'node'", func(t *testing.T) {
		// Real-world: macOS install runs claude as `node /path/to/claude/cli.js`.
		// The regex matches the word `claude` anywhere in the command string.
		realistic := ParseProcessSnapshot(lines(
			"  100     1 /sbin/launchd",
			"  200   100 node /Users/kyle/.nvm/versions/node/v20.18.0/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			"  300   200 /bin/sh -c /Users/kyle/Code/meta-claude/tail-claude-mux/scripts/hook.sh PreToolUse",
		))
		if got := ResolveAgentSessionPid(300, claudeRE, realistic); got != 200 {
			t.Errorf("got %d, want 200", got)
		}
	})
}

// TS: resolveAgentSessionPidFromSnapshot — parse+walk integration. Go has no
// FromSnapshot convenience wrapper; the composition is the same behavior.
func TestResolveAgentSessionPidFromSnapshot(t *testing.T) {
	t.Run("end-to-end resolution from raw ps text", func(t *testing.T) {
		ps := lines(
			"  100     1 /sbin/launchd",
			"  200   100 node /opt/homebrew/bin/claude",
			"  300   200 /bin/sh -c hook.sh",
			"  400   300 /bin/bash hook.sh",
		)
		if got := ResolveAgentSessionPid(400, claudeRE, ParseProcessSnapshot(ps)); got != 200 {
			t.Errorf("got %d, want 200", got)
		}
	})
}

func TestBuildPanePidIndex(t *testing.T) {
	t.Run("parses tmux list-panes output into pid → session map", func(t *testing.T) {
		// Production format: "#{session_name}|#{pane_pid}"
		out := BuildPanePidIndex(lines("pi-dev|5055", "pi-dev|89112", "ai-engineering-domain|12345"))
		if len(out) != 3 {
			t.Fatalf("len = %d, want 3", len(out))
		}
		if got := out[5055]; got != "pi-dev" {
			t.Errorf("out[5055] = %q, want pi-dev", got)
		}
		if got := out[89112]; got != "pi-dev" {
			t.Errorf("out[89112] = %q, want pi-dev", got)
		}
		if got := out[12345]; got != "ai-engineering-domain" {
			t.Errorf("out[12345] = %q, want ai-engineering-domain", got)
		}
	})

	t.Run("ignores blank lines and malformed entries", func(t *testing.T) {
		out := BuildPanePidIndex(lines("", "pi-dev|5055", "junk-no-pipe", "name|notanumber", "  "))
		if len(out) != 1 {
			t.Fatalf("len = %d, want 1", len(out))
		}
		if got := out[5055]; got != "pi-dev" {
			t.Errorf("out[5055] = %q, want pi-dev", got)
		}
	})

	t.Run("rejects non-positive pids", func(t *testing.T) {
		out := BuildPanePidIndex(lines("s|0", "s|-1"))
		if len(out) != 0 {
			t.Fatalf("len = %d, want 0", len(out))
		}
	})
}

func TestResolveSessionByPid(t *testing.T) {
	// Realistic chain: tmux pane shell (89112) → bash spawning pi (89539)
	//                 → pi process (89555).
	// Mirrors the live ps tree captured during bug repro.
	snapshot := ParseProcessSnapshot(lines(
		"    1     0 launchd",
		"89112     1 bash",
		"89539 89112 /bin/sh /usr/bin/command pi",
		"89555 89539 pi",
	))

	panePidIndex := BuildPanePidIndex("pi-dev|89112")

	t.Run("walks up from pi pid to its pane and returns the session", func(t *testing.T) {
		if got := ResolveSessionByPid(89555, panePidIndex, snapshot); got != "pi-dev" {
			t.Errorf("got %q, want pi-dev", got)
		}
	})

	t.Run("walks through wrapper layers", func(t *testing.T) {
		// Same chain, lookup from the middle wrapper still resolves.
		if got := ResolveSessionByPid(89539, panePidIndex, snapshot); got != "pi-dev" {
			t.Errorf("got %q, want pi-dev", got)
		}
	})

	t.Run("returns null when pid is not in snapshot", func(t *testing.T) {
		// pid not present (process exited before lookup) — caller's failure
		// mode is "drop the hook", not "guess".
		if got := ResolveSessionByPid(99999, panePidIndex, snapshot); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})

	t.Run("returns null when ancestor chain has no pane_pid", func(t *testing.T) {
		// Process tree is intact but the chain never crosses a pane shell. This
		// happens for processes that aren't inside any tmux pane (e.g. agents
		// launched from a system service).
		orphanSnapshot := ParseProcessSnapshot(lines(
			"    1     0 launchd",
			"55555     1 some-daemon",
			"55556 55555 pi",
		))
		if got := ResolveSessionByPid(55556, panePidIndex, orphanSnapshot); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})

	t.Run("multi-session: each pane resolves to its own session", func(t *testing.T) {
		multiIndex := BuildPanePidIndex(lines("pi-dev|89112", "ai-eng|5055"))
		multiSnapshot := ParseProcessSnapshot(lines(
			"89112     1 bash",
			"89555 89112 pi",
			" 5055     1 bash",
			" 7762  5055 bun",
		))
		if got := ResolveSessionByPid(89555, multiIndex, multiSnapshot); got != "pi-dev" {
			t.Errorf("got %q, want pi-dev", got)
		}
		if got := ResolveSessionByPid(7762, multiIndex, multiSnapshot); got != "ai-eng" {
			t.Errorf("got %q, want ai-eng", got)
		}
	})

	t.Run("invalid pid (0, negative, NaN) returns null", func(t *testing.T) {
		// NaN is unrepresentable as a Go int; the 0 and negative arms carry
		// the invalid-pid contract here.
		if got := ResolveSessionByPid(0, panePidIndex, snapshot); got != "" {
			t.Errorf("pid 0: got %q, want \"\"", got)
		}
		if got := ResolveSessionByPid(-1, panePidIndex, snapshot); got != "" {
			t.Errorf("pid -1: got %q, want \"\"", got)
		}
	})

	t.Run("cycle-safe: pid that points to itself does not loop", func(t *testing.T) {
		// Defensive: a corrupt snapshot where a pid is its own parent must not
		// hang the resolver.
		cycleSnapshot := ParseProcessSnapshot("44444 44444 weird-self-cycle")
		idx := BuildPanePidIndex("s|99999")
		if got := ResolveSessionByPid(44444, idx, cycleSnapshot); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
}

func TestLiveBugScenarioReproduction(t *testing.T) {
	// Coordinates from the live system at bug repro time:
	//   pi-dev session has panes with pane_pids including 89112 (the bash
	//   hosting pi 89555). pi 89555 emits hooks; pid-based resolution should
	//   return "pi-dev" regardless of where the active pane has navigated.
	t.Run("pi 89555 resolves to pi-dev even when active pane cwd diverges", func(t *testing.T) {
		snapshot := ParseProcessSnapshot(lines(
			"89112     1 bash",
			"89539 89112 /bin/sh /usr/bin/command pi",
			"89555 89539 pi",
		))
		// Only pi-dev runs pi here. The fact that pi-dev's active pane is
		// currently in /Users/kyle/Code/my-projects/kylesnowschwartz.github.io
		// is irrelevant — we route by pid, not by path.
		panePidIndex := BuildPanePidIndex("pi-dev|89112")

		if got := ResolveSessionByPid(89555, panePidIndex, snapshot); got != "pi-dev" {
			t.Errorf("got %q, want pi-dev", got)
		}
	})
}
