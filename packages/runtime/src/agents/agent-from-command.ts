/**
 * Command-line aware agent identification.
 *
 * The pane scanner's fast path matches an agent by its `comm` (executable
 * basename) alone — fine for a bare `claude` or `pi` binary. But agents are
 * frequently launched through a runtime or wrapper whose `comm` is the
 * *launcher*, not the agent:
 *   - node/bun-wrapped pi:  comm=`node`,  argv=`node …/@earendil-works/pi-coding-agent/dist/cli.js`
 *   - nix-wrapped claude:   comm=`.claude-code-wrapped`, argv0=`/nix/store/…/bin/claude-code`
 *   - npx/shell-wrapped:    comm=`npx`/`sh`, argv carries the real script path
 *
 * `agentFromCommand` resolves those cases from the full command line. It is a
 * narrowed port of herdr's `identify_agent_in_job` machinery
 * (`src/detect/mod.rs`: `normalized_process_name`, `wrapped_agent_name_from_runtime_argv`,
 * `agent_name_from_path_token`, `agent_name_from_known_package_path`), scoped to
 * the two agents tcm receives hooks from — pi and claude-code.
 *
 * Pure: no process state, no I/O (herdr's filesystem `canonicalize` fallback is
 * deliberately omitted so this stays unit-testable). Boundary-aware in the same
 * spirit as `commMatches`: `python -c "…codex…"` and `node -e "…claude…"` are
 * rejected because eval flags carry inline code, not a script path.
 */

/** Canonical agent keys this matcher can return. Matches the keys in
 *  AGENT_COMM_PATTERNS for pi + claude-code. */
export type AgentKey = "pi" | "claude-code";

/** Runtimes/shells whose `comm` is a launcher — the real agent identity lives
 *  in the script argument. `npx`/`bunx` are included beyond herdr's set because
 *  they're common agent launchers on macOS. */
const GENERIC_RUNTIMES = new Set([
  "sh", "bash", "zsh", "fish", "tmux",
  "node", "bun", "deno", "python", "python3",
  "npx", "bunx",
]);

/** Node/bun-style runtimes pass inline code via these flags — when present the
 *  next token is code, not a script, so identification must bail. */
const EVAL_FLAGS = new Set(["-e", "--eval", "-p", "--print", "-c"]);
/** `python -m <module>` runs a module, not a script path. */
const MODULE_FLAGS = new Set(["-m"]);
/** Flags that consume the following token as their value (node subset). */
const VALUE_FLAGS = new Set([
  "-r", "--require", "--loader", "--import", "--experimental-loader",
  "--inspect-port", "-W", "-X", "-S", "-L", "-o",
]);

/** lowercase, trim, strip a trailing runtime/script suffix. Mirrors herdr's
 *  `normalized_agent_lookup_name`. */
function normalizeLookupName(name: string): string {
  let n = name.trim().toLowerCase();
  for (const suffix of [".exe", ".cmd", ".bat", ".ps1", ".js"]) {
    if (n.endsWith(suffix)) { n = n.slice(0, -suffix.length); break; }
  }
  return n;
}

/** Last non-empty path component, splitting on both `/` and `\`. */
function pathBasename(path: string): string {
  const parts = path.split(/[/\\]/).filter((p) => p.length > 0);
  return parts.length ? parts[parts.length - 1]! : path;
}

/** Map a normalized basename to an agent key, or undefined. */
function parseAgentLabel(name: string): AgentKey | undefined {
  const n = normalizeLookupName(name);
  if (n === "pi") return "pi";
  if (n === "claude" || n === "claude-code") return "claude-code";
  return undefined;
}

/** Fingerprint pi by its installed package path. Looks for the consecutive
 *  components `@earendil-works / pi-coding-agent / dist / cli` anywhere in the
 *  path (normalized, so `cli.js` → `cli`). Tighter than a bare substring — the
 *  scoped package name is a strong, low-false-positive signal. */
function agentFromKnownPackagePath(token: string): AgentKey | undefined {
  const components = token
    .split(/[/\\]/)
    .filter((c) => c.length > 0)
    .map(normalizeLookupName);
  const fingerprint = ["@earendil-works", "pi-coding-agent", "dist", "cli"];
  for (let i = 0; i + fingerprint.length <= components.length; i++) {
    let match = true;
    for (let j = 0; j < fingerprint.length; j++) {
      if (components[i + j] !== fingerprint[j]) { match = false; break; }
    }
    if (match) return "pi";
  }
  return undefined;
}

/** Resolve an agent from a single path/argv token: basename match first, then
 *  the known package-path fingerprint. Rejects empty/flag tokens. */
function agentFromPathToken(token: string): AgentKey | undefined {
  const trimmed = token.replace(/^['"]+|['"]+$/g, "");
  if (trimmed.length === 0 || trimmed.startsWith("-")) return undefined;
  return parseAgentLabel(pathBasename(trimmed)) ?? agentFromKnownPackagePath(trimmed);
}

/** Walk runtime argv (after argv0) to the first real script token, honoring
 *  eval/module flags (bail) and value-consuming flags (skip the value).
 *  Mirrors herdr's `script_arg_agent_name`. */
function agentFromScriptArgs(tokens: string[]): AgentKey | undefined {
  for (let i = 1; i < tokens.length; i++) {
    const arg = tokens[i]!;
    if (arg === "--") {
      const next = tokens[i + 1];
      return next ? agentFromPathToken(next) : undefined;
    }
    // Eval/module flags carry inline code or a module name — no script path,
    // so identification must stop here (rejects `python -c "…codex…"`).
    if (EVAL_FLAGS.has(arg) || MODULE_FLAGS.has(arg)) return undefined;
    if (arg.startsWith("-")) {
      if (VALUE_FLAGS.has(arg)) i++; // consume the flag's value token
      continue;
    }
    return agentFromPathToken(arg);
  }
  return undefined;
}

/**
 * Identify the agent (`"pi"` | `"claude-code"`) running under a process, given
 * its `comm` and full command line (argv joined by spaces, as captured by the
 * pane scanner). Returns undefined when no agent is recognized.
 *
 * Intended as the fallback when `commMatches(comm, pattern)` misses — i.e. the
 * process is a runtime/wrapper rather than the bare agent binary.
 */
export function agentFromCommand(comm: string, cmdline: string | undefined): AgentKey | undefined {
  const cmd = (cmdline ?? "").trim();
  const tokens = cmd.length ? cmd.split(/\s+/) : [];

  // 1. Known package path anywhere in the command line — catches node/bun-wrapped
  //    pi whose comm is just the runtime.
  const pkg = agentFromKnownPackagePath(cmd);
  if (pkg) return pkg;

  // 2. comm is a generic runtime/shell — the agent identity is the script arg.
  const commBase = normalizeLookupName(pathBasename(comm));
  if (GENERIC_RUNTIMES.has(commBase) && tokens.length > 0) {
    const wrapped = agentFromScriptArgs(tokens);
    if (wrapped) return wrapped;
  }

  // 3. Fall back to argv0's basename. Handles nix-wrapped aliases where comm is
  //    `.claude-code-wrapped` but argv0 resolves to `…/bin/claude-code`.
  if (tokens.length > 0) {
    const argv0 = agentFromPathToken(tokens[0]!);
    if (argv0) return argv0;
  }

  return undefined;
}
