// Package agentmatch ports the pane scanner's agent-identification pair:
// commMatches (server/index.ts) and agentFromCommand
// (agents/agent-from-command.ts).
//
// The fast path matches an agent by its comm (executable basename) alone.
// AgentFromCommand is the fallback for wrapped launches — node/bun-wrapped
// pi, nix-wrapped claude, npx/shell wrappers — where comm is the runtime,
// not the agent. It is a narrowed port of herdr's identify_agent_in_job
// machinery scoped to the two agents tcm receives hooks from: pi and
// claude-code. Boundary-aware: eval flags (`node -e "…claude…"`) carry
// inline code, not a script path, and are rejected.
package agentmatch

import "strings"

// Agent keys AgentFromCommand can return.
const (
	AgentPi         = "pi"
	AgentClaudeCode = "claude-code"
)

// CommMatches matches a comm string against a pattern as a whole word:
// "claude" matches "claude", "/usr/bin/claude", "claude-code" but NOT
// "tail-claude", "pip" (vs "pi"), or "claude.fork". The pattern must start
// at the beginning of comm or after a path separator, and end at the end of
// comm or before a hyphen (preserving "claude" → "claude-code").
func CommMatches(comm, pat string) bool {
	idx := strings.Index(comm, pat)
	if idx < 0 {
		return false
	}
	if idx > 0 && comm[idx-1] != '/' {
		return false
	}
	if tail := idx + len(pat); tail < len(comm) && comm[tail] != '-' {
		return false
	}
	return true
}

// genericRuntimes are runtimes/shells whose comm is a launcher — the real
// agent identity lives in the script argument.
var genericRuntimes = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "tmux": true,
	"node": true, "bun": true, "deno": true, "python": true, "python3": true,
	"npx": true, "bunx": true,
}

// evalFlags pass inline code — the next token is code, not a script, so
// identification must bail.
var evalFlags = map[string]bool{"-e": true, "--eval": true, "-p": true, "--print": true, "-c": true}

// moduleFlags run a module, not a script path (`python -m <module>`).
var moduleFlags = map[string]bool{"-m": true}

// valueFlags consume the following token as their value (node subset).
var valueFlags = map[string]bool{
	"-r": true, "--require": true, "--loader": true, "--import": true,
	"--experimental-loader": true, "--inspect-port": true,
	"-W": true, "-X": true, "-S": true, "-L": true, "-o": true,
}

// piPackageFingerprint identifies pi by consecutive normalized path
// components of its installed package.
var piPackageFingerprint = []string{"@earendil-works", "pi-coding-agent", "dist", "cli"}

// normalizeLookupName lowercases, trims, and strips one trailing
// runtime/script suffix.
func normalizeLookupName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".ps1", ".js"} {
		if strings.HasSuffix(n, suffix) {
			return n[:len(n)-len(suffix)]
		}
	}
	return n
}

// pathBasename is the last non-empty component, splitting on / and \.
func pathBasename(path string) string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

// parseAgentLabel maps a normalized basename to an agent key, or "".
func parseAgentLabel(name string) string {
	switch normalizeLookupName(name) {
	case "pi":
		return AgentPi
	case "claude", "claude-code":
		return AgentClaudeCode
	default:
		return ""
	}
}

// agentFromKnownPackagePath fingerprints pi by its scoped package path —
// tighter than a bare substring, a strong low-false-positive signal.
func agentFromKnownPackagePath(token string) string {
	raw := strings.FieldsFunc(token, func(r rune) bool { return r == '/' || r == '\\' })
	components := make([]string, len(raw))
	for i, c := range raw {
		components[i] = normalizeLookupName(c)
	}
	for i := 0; i+len(piPackageFingerprint) <= len(components); i++ {
		match := true
		for j, want := range piPackageFingerprint {
			if components[i+j] != want {
				match = false
				break
			}
		}
		if match {
			return AgentPi
		}
	}
	return ""
}

// agentFromPathToken resolves an agent from a single path/argv token:
// basename match first, then the known package-path fingerprint. Rejects
// empty/flag tokens.
func agentFromPathToken(token string) string {
	trimmed := strings.Trim(token, `'"`)
	if trimmed == "" || strings.HasPrefix(trimmed, "-") {
		return ""
	}
	if a := parseAgentLabel(pathBasename(trimmed)); a != "" {
		return a
	}
	return agentFromKnownPackagePath(trimmed)
}

// agentFromScriptArgs walks runtime argv (after argv0) to the first real
// script token, honoring eval/module flags (bail) and value-consuming flags
// (skip the value).
func agentFromScriptArgs(tokens []string) string {
	for i := 1; i < len(tokens); i++ {
		arg := tokens[i]
		if arg == "--" {
			if i+1 < len(tokens) {
				return agentFromPathToken(tokens[i+1])
			}
			return ""
		}
		if evalFlags[arg] || moduleFlags[arg] {
			return ""
		}
		if strings.HasPrefix(arg, "-") {
			if valueFlags[arg] {
				i++ // consume the flag's value token
			}
			continue
		}
		return agentFromPathToken(arg)
	}
	return ""
}

// AgentFromCommand identifies the agent ("pi" | "claude-code") running
// under a process, given its comm and full command line. Returns "" when no
// agent is recognized. Intended as the fallback when CommMatches misses —
// the process is a runtime/wrapper rather than the bare agent binary.
func AgentFromCommand(comm, cmdline string) string {
	cmd := strings.TrimSpace(cmdline)
	var tokens []string
	if cmd != "" {
		tokens = strings.Fields(cmd)
	}

	// 1. Known package path anywhere in the command line — catches
	//    node/bun-wrapped pi whose comm is just the runtime.
	if a := agentFromKnownPackagePath(cmd); a != "" {
		return a
	}

	// 2. comm is a generic runtime/shell — the agent identity is the script arg.
	if genericRuntimes[normalizeLookupName(pathBasename(comm))] && len(tokens) > 0 {
		if a := agentFromScriptArgs(tokens); a != "" {
			return a
		}
	}

	// 3. Fall back to argv0's basename. Handles nix-wrapped aliases where
	//    comm is ".claude-code-wrapped" but argv0 is ".../bin/claude-code".
	if len(tokens) > 0 {
		if a := agentFromPathToken(tokens[0]); a != "" {
			return a
		}
	}

	return ""
}
