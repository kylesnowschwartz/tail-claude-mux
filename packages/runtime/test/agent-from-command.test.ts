import { describe, test, expect } from "bun:test";
import { agentFromCommand } from "../src/agents/agent-from-command";

// Ported from herdr's `identify_agent_in_job` test suite (src/detect/mod.rs),
// narrowed to the two agents tcm receives hooks from. Each case is a wrapper
// invocation that the comm-only fast path (commMatches) would miss.
describe("agentFromCommand — wrapper identification", () => {
  test("node-wrapped pi package CLI (comm=node)", () => {
    expect(
      agentFromCommand(
        "node",
        "node /Users/x/.bun/install/global/node_modules/@earendil-works/pi-coding-agent/dist/cli.js",
      ),
    ).toBe("pi");
  });

  test("bun-wrapped pi package CLI (comm=bun)", () => {
    expect(
      agentFromCommand(
        "bun",
        "bun /opt/node_modules/@earendil-works/pi-coding-agent/dist/cli.js --resume",
      ),
    ).toBe("pi");
  });

  test("nix-wrapped claude — comm is .claude-code-wrapped, argv0 resolves to claude-code", () => {
    expect(
      agentFromCommand(".claude-code-wrapped", "/nix/store/example/bin/claude-code"),
    ).toBe("claude-code");
  });

  test("nix-wrapped claude with trailing args", () => {
    expect(
      agentFromCommand(".claude-code-wrapped", "/nix/store/abc/bin/claude-code --resume xyz"),
    ).toBe("claude-code");
  });

  test("shell-wrapped pi (comm=sh, script path basename = pi)", () => {
    expect(agentFromCommand("sh", "/bin/sh /tmp/test-bin/pi")).toBe("pi");
  });

  test("npx-wrapped claude (bare package name)", () => {
    expect(agentFromCommand("npx", "npx @anthropic-ai/claude-code")).toBe("claude-code");
  });

  test("node-wrapped claude binary by basename", () => {
    expect(
      agentFromCommand("node", "node /usr/local/lib/node_modules/.bin/claude"),
    ).toBe("claude-code");
  });

  test("node-wrapped claude with eval-looking but real script after value flag", () => {
    // --require consumes its value token; the next bare token is the script.
    expect(
      agentFromCommand("node", "node --require ./pre.js /opt/bin/claude"),
    ).toBe("claude-code");
  });

  test("script after `--` separator", () => {
    expect(agentFromCommand("node", "node -- /opt/bin/pi")).toBe("pi");
  });
});

describe("agentFromCommand — false-positive rejection", () => {
  test("python -c with codex in inline code is rejected (eval flag, not a script)", () => {
    expect(
      agentFromCommand("python", 'python -c "import x; run_codex()"'),
    ).toBeUndefined();
  });

  test("node -e with claude in inline code is rejected", () => {
    expect(agentFromCommand("node", 'node -e "console.log(\'claude\')"')).toBeUndefined();
  });

  test("python -m module is rejected", () => {
    expect(agentFromCommand("python", "python -m pi")).toBeUndefined();
  });

  test("pip (commMatches false-positive sibling) is not pi", () => {
    expect(agentFromCommand("pip", "/usr/bin/pip install pi")).toBeUndefined();
  });

  test("a directory named meta-claude in an unrelated command does not match", () => {
    expect(
      agentFromCommand("node", "node /Users/x/Code/meta-claude/build.js"),
    ).toBeUndefined();
  });

  test("plain shell with no agent script", () => {
    expect(agentFromCommand("zsh", "-zsh")).toBeUndefined();
    expect(agentFromCommand("bash", "bash")).toBeUndefined();
  });

  test("empty / missing cmdline yields undefined", () => {
    expect(agentFromCommand("node", "")).toBeUndefined();
    expect(agentFromCommand("node", undefined)).toBeUndefined();
  });

  test("a non-runtime comm that is not an agent does not match via script args", () => {
    // vim opening a file named pi must not be identified as the pi agent —
    // vim is not a generic runtime, so script-arg walking never runs.
    expect(agentFromCommand("vim", "vim /tmp/pi")).toBeUndefined();
  });

  test("partial package path (missing dist/cli) is not pi", () => {
    expect(
      agentFromCommand("node", "node /x/node_modules/@earendil-works/pi-coding-agent/package.json"),
    ).toBeUndefined();
  });
});
