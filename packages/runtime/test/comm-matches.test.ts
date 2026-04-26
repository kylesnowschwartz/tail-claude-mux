import { describe, test, expect } from "bun:test";
import { commMatches } from "../src/server/index";

// Boundary regression suite for commMatches. The matcher used to enforce only
// a left boundary (path separator or start-of-string), which was fine for
// 4+ character patterns but shipped a bug for the new "pi" pattern: any
// command name starting with "pi" — pip, ping, pipx, pipenv, /usr/bin/pip —
// matched. After the fix the matcher also requires a right boundary
// (end-of-string OR hyphen). The hyphen exception preserves the intentional
// prefix matches like "claude" → "claude-code".
describe("commMatches — boundary rules", () => {
  test("exact basename match (no prefix)", () => {
    expect(commMatches("pi", "pi")).toBe(true);
    expect(commMatches("claude", "claude")).toBe(true);
    expect(commMatches("amp", "amp")).toBe(true);
    expect(commMatches("codex", "codex")).toBe(true);
    expect(commMatches("opencode", "opencode")).toBe(true);
  });

  test("path-prefixed exact basename", () => {
    expect(commMatches("/usr/bin/pi", "pi")).toBe(true);
    expect(commMatches("/usr/local/bin/claude", "claude")).toBe(true);
    expect(commMatches("/opt/codex", "codex")).toBe(true);
  });

  test("hyphen-suffix is part of the same word — preserves intentional prefix matches", () => {
    expect(commMatches("claude-code", "claude")).toBe(true);
    expect(commMatches("amp-cli", "amp")).toBe(true);
    expect(commMatches("pi-mono", "pi")).toBe(true);
    expect(commMatches("/usr/bin/claude-code", "claude")).toBe(true);
  });

  test("M3 regression: short patterns must not greedily prefix-match longer commands", () => {
    expect(commMatches("pip", "pi")).toBe(false);
    expect(commMatches("pipx", "pi")).toBe(false);
    expect(commMatches("ping", "pi")).toBe(false);
    expect(commMatches("pipenv", "pi")).toBe(false);
    expect(commMatches("/usr/bin/pip", "pi")).toBe(false);
    expect(commMatches("/usr/local/bin/pipenv", "pi")).toBe(false);
  });

  test("substring matches in the middle of a name do not count", () => {
    expect(commMatches("tail-claude", "claude")).toBe(false);
    expect(commMatches("my-claude-fork", "claude")).toBe(false);
    expect(commMatches("xyz-pi", "pi")).toBe(false);
  });

  test("non-hyphen suffixes (dots, digits, dashes-elsewhere) do not match", () => {
    expect(commMatches("claude.fork", "claude")).toBe(false);
    expect(commMatches("claude2", "claude")).toBe(false);
    expect(commMatches("claudex", "claude")).toBe(false);
    expect(commMatches("amplitude", "amp")).toBe(false);
    expect(commMatches("opencoded", "opencode")).toBe(false);
  });

  test("returns false on no match", () => {
    expect(commMatches("nodejs", "pi")).toBe(false);
    expect(commMatches("", "pi")).toBe(false);
  });
});
