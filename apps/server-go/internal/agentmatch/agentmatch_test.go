// Ported from packages/runtime/test/agent-from-command.test.ts and
// packages/runtime/test/comm-matches.test.ts. Subtest names match the TS
// test names one-to-one. TS `undefined` maps to "" here.
package agentmatch

import "testing"

// Ported from herdr's `identify_agent_in_job` test suite (src/detect/mod.rs),
// narrowed to the agent tcm receives hooks from via the fallback path. Each
// case is a wrapper invocation that the comm-only fast path (CommMatches)
// would miss.
func TestAgentFromCommandWrapperIdentification(t *testing.T) {
	cases := []struct {
		name    string
		comm    string
		cmdline string
		want    string
	}{
		{
			name:    "nix-wrapped claude — comm is .claude-code-wrapped, argv0 resolves to claude-code",
			comm:    ".claude-code-wrapped",
			cmdline: "/nix/store/example/bin/claude-code",
			want:    AgentClaudeCode,
		},
		{
			name:    "nix-wrapped claude with trailing args",
			comm:    ".claude-code-wrapped",
			cmdline: "/nix/store/abc/bin/claude-code --resume xyz",
			want:    AgentClaudeCode,
		},
		{
			name:    "npx-wrapped claude (bare package name)",
			comm:    "npx",
			cmdline: "npx @anthropic-ai/claude-code",
			want:    AgentClaudeCode,
		},
		{
			name:    "node-wrapped claude binary by basename",
			comm:    "node",
			cmdline: "node /usr/local/lib/node_modules/.bin/claude",
			want:    AgentClaudeCode,
		},
		{
			// --require consumes its value token; the next bare token is the script.
			name:    "node-wrapped claude with eval-looking but real script after value flag",
			comm:    "node",
			cmdline: "node --require ./pre.js /opt/bin/claude",
			want:    AgentClaudeCode,
		},
		{
			name:    "script after `--` separator",
			comm:    "node",
			cmdline: "node -- /opt/bin/claude",
			want:    AgentClaudeCode,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentFromCommand(tc.comm, tc.cmdline); got != tc.want {
				t.Errorf("AgentFromCommand(%q, %q) = %q, want %q", tc.comm, tc.cmdline, got, tc.want)
			}
		})
	}
}

func TestAgentFromCommandFalsePositiveRejection(t *testing.T) {
	cases := []struct {
		name    string
		comm    string
		cmdline string
	}{
		{
			name:    "python -c with codex in inline code is rejected (eval flag, not a script)",
			comm:    "python",
			cmdline: `python -c "import x; run_codex()"`,
		},
		{
			name:    "node -e with claude in inline code is rejected",
			comm:    "node",
			cmdline: `node -e "console.log('claude')"`,
		},
		{
			name:    "python -m module is rejected",
			comm:    "python",
			cmdline: "python -m claude",
		},
		{
			name:    "a directory named meta-claude in an unrelated command does not match",
			comm:    "node",
			cmdline: "node /Users/x/Code/meta-claude/build.js",
		},
		{
			// vim opening a file named claude must not be identified as the
			// claude agent — vim is not a generic runtime, so script-arg
			// walking never runs.
			name:    "a non-runtime comm that is not an agent does not match via script args",
			comm:    "vim",
			cmdline: "vim /tmp/claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentFromCommand(tc.comm, tc.cmdline); got != "" {
				t.Errorf("AgentFromCommand(%q, %q) = %q, want \"\"", tc.comm, tc.cmdline, got)
			}
		})
	}

	t.Run("plain shell with no agent script", func(t *testing.T) {
		if got := AgentFromCommand("zsh", "-zsh"); got != "" {
			t.Errorf("AgentFromCommand(zsh, -zsh) = %q, want \"\"", got)
		}
		if got := AgentFromCommand("bash", "bash"); got != "" {
			t.Errorf("AgentFromCommand(bash, bash) = %q, want \"\"", got)
		}
	})

	t.Run("empty / missing cmdline yields undefined", func(t *testing.T) {
		// TS exercises both "" and undefined; both map to "" in Go.
		if got := AgentFromCommand("node", ""); got != "" {
			t.Errorf("AgentFromCommand(node, \"\") = %q, want \"\"", got)
		}
	})
}

// Boundary regression suite for CommMatches: the matcher enforces both a
// left boundary (path separator or start-of-string) and a right boundary
// (end-of-string OR hyphen) so short patterns don't greedily prefix-match
// longer commands. The hyphen exception preserves the intentional prefix
// matches like "claude" → "claude-code".
func TestCommMatchesBoundaryRules(t *testing.T) {
	check := func(t *testing.T, comm, pat string, want bool) {
		t.Helper()
		if got := CommMatches(comm, pat); got != want {
			t.Errorf("CommMatches(%q, %q) = %v, want %v", comm, pat, got, want)
		}
	}

	t.Run("exact basename match (no prefix)", func(t *testing.T) {
		check(t, "claude", "claude", true)
		check(t, "amp", "amp", true)
		check(t, "codex", "codex", true)
		check(t, "opencode", "opencode", true)
	})

	t.Run("path-prefixed exact basename", func(t *testing.T) {
		check(t, "/usr/local/bin/claude", "claude", true)
		check(t, "/opt/codex", "codex", true)
		check(t, "/opt/homebrew/bin/codex", "codex", true)
	})

	t.Run("later path occurrence can match after an earlier boundary miss", func(t *testing.T) {
		check(t, "/Users/kyle/.npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex", "codex", true)
		check(t, "/Users/x/node_modules/@openai/codex/bin/other", "codex", false)
	})

	t.Run("hyphen-suffix is part of the same word — preserves intentional prefix matches", func(t *testing.T) {
		check(t, "claude-code", "claude", true)
		check(t, "amp-cli", "amp", true)
		check(t, "codex-tui", "codex", true)
		check(t, "/usr/bin/claude-code", "claude", true)
	})

	t.Run("substring matches in the middle of a name do not count", func(t *testing.T) {
		check(t, "tail-claude", "claude", false)
		check(t, "my-claude-fork", "claude", false)
	})

	t.Run("non-hyphen suffixes (dots, digits, dashes-elsewhere) do not match", func(t *testing.T) {
		check(t, "claude.fork", "claude", false)
		check(t, "claude2", "claude", false)
		check(t, "claudex", "claude", false)
		check(t, "amplitude", "amp", false)
		check(t, "opencoded", "opencode", false)
	})

	t.Run("returns false on no match", func(t *testing.T) {
		check(t, "nodejs", "codex", false)
		check(t, "", "codex", false)
	})
}

func TestCommMatchesPi(t *testing.T) {
	for _, comm := range []string{"pi", "/usr/local/bin/pi", "pi-rpc"} {
		if !CommMatches(comm, "pi") {
			t.Errorf("CommMatches(%q, pi) = false, want true", comm)
		}
	}
	for _, comm := range []string{"pip", "pipenv", "pipx", "ping", "/usr/bin/pip3", "api"} {
		if CommMatches(comm, "pi") {
			t.Errorf("CommMatches(%q, pi) = true, want false", comm)
		}
	}
}
