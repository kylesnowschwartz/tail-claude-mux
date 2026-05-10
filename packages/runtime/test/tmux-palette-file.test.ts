import { describe, test, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, readFileSync, rmSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import {
  buildPaletteFileBody,
  writePaletteFile,
  __resetPaletteFileWriterStateForTests,
  type PaletteFileWriterDeps,
} from "../src/server/tmux-palette-file";
import { resolveTheme, BUILTIN_THEMES } from "../src/themes";
import { STATUSLINE_LAST_WINDOW, STATUSLINE_SHELL } from "../src/server/tmux-header-sync";

const MOCHA = resolveTheme("catppuccin-mocha");
const DRACULA = resolveTheme("dracula");

function makeStubDeps(): { calls: string[][]; logs: Array<{ msg: string; data?: Record<string, unknown> }>; deps: PaletteFileWriterDeps } {
  const calls: string[][] = [];
  const logs: Array<{ msg: string; data?: Record<string, unknown> }> = [];
  const deps: PaletteFileWriterDeps = {
    shellTmux: (args) => { calls.push(args); return ""; },
    log: (msg, data) => { logs.push({ msg, data }); },
  };
  return { calls, logs, deps };
}

describe("buildPaletteFileBody", () => {
  test("emits one set-gq line per palette token plus the two glyph constants", () => {
    const body = buildPaletteFileBody(MOCHA, "catppuccin-mocha");
    // 9 palette tokens (PALETTE_TOKENS in tmux-header-sync.ts).
    const palette = body.match(/^set -gq @tcm-thm-/gm) ?? [];
    expect(palette.length).toBe(9);
    expect(body).toContain(`@tcm-thm-base    `);
    expect(body).toContain(`"${MOCHA.palette.base}"`);
    expect(body).toContain(`"${MOCHA.palette.blue}"`);
    expect(body).toContain(`@tcm-shell-glyph        "${STATUSLINE_SHELL}"`);
    expect(body).toContain(`@tcm-last-window-glyph  "${STATUSLINE_LAST_WINDOW}"`);
  });

  test("transparent theme palette translates to tmux 'default'", () => {
    const transparent = resolveTheme("transparent");
    const body = buildPaletteFileBody(transparent, "transparent");
    // base is 'transparent' → 'default'; blue passes through unchanged.
    expect(body).toContain(`@tcm-thm-base     "default"`);
    expect(body).toContain(`"${transparent.palette.blue}"`);
  });

  test("themed body differs across themes (regression for diff cache)", () => {
    // Same-name palette rewrites or theme switches should both produce a new
    // body. Used to be a bug: the diff used to key on themeName alone, missing
    // in-place edits to the same-named theme.
    const m = buildPaletteFileBody(MOCHA, "catppuccin-mocha");
    const d = buildPaletteFileBody(DRACULA, "dracula");
    expect(m).not.toBe(d);
    // Synthesize a same-name-different-palette case.
    const tweaked = { ...MOCHA, palette: { ...MOCHA.palette, base: "#000000" } };
    const tw = buildPaletteFileBody(tweaked, "catppuccin-mocha");
    expect(tw).not.toBe(m);
  });
});

describe("writePaletteFile", () => {
  let tmpDir: string;
  let dest: string;

  beforeEach(() => {
    tmpDir = join(tmpdir(), `palette-test-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(tmpDir, { recursive: true });
    dest = join(tmpDir, "palette-active.tmux.conf");
    __resetPaletteFileWriterStateForTests();
  });

  afterEach(() => {
    try { rmSync(tmpDir, { recursive: true }); } catch {}
  });

  test("writes the file and runs `tmux source-file -q <dest>` to apply live", () => {
    const { calls, deps } = makeStubDeps();
    writePaletteFile(MOCHA, "catppuccin-mocha", deps, dest);

    const body = readFileSync(dest, "utf-8");
    expect(body).toContain("@tcm-thm-base");
    expect(body).toContain(MOCHA.palette.base);

    expect(calls).toHaveLength(1);
    expect(calls[0]).toEqual(["source-file", "-q", dest]);
  });

  test("idempotent: same theme on second call skips write + source-file", () => {
    const { calls, deps } = makeStubDeps();
    writePaletteFile(MOCHA, "catppuccin-mocha", deps, dest);
    expect(calls).toHaveLength(1);

    writePaletteFile(MOCHA, "catppuccin-mocha", deps, dest);
    expect(calls).toHaveLength(1); // no new tmux call
  });

  test("theme change triggers a fresh write + source-file", () => {
    const { calls, deps } = makeStubDeps();
    writePaletteFile(MOCHA, "catppuccin-mocha", deps, dest);
    writePaletteFile(DRACULA, "dracula", deps, dest);
    expect(calls).toHaveLength(2);

    const body = readFileSync(dest, "utf-8");
    expect(body).toContain(BUILTIN_THEMES["dracula"]!.palette.base);
  });

  test("same name + tweaked palette values still re-writes (file-body diff key)", () => {
    // Regression: the diff key used to be themeName, missing in-place theme edits.
    const { calls, deps } = makeStubDeps();
    writePaletteFile(MOCHA, "catppuccin-mocha", deps, dest);
    expect(calls).toHaveLength(1);

    const tweaked = { ...MOCHA, palette: { ...MOCHA.palette, base: "#000000" } };
    writePaletteFile(tweaked, "catppuccin-mocha", deps, dest);
    expect(calls).toHaveLength(2);
    const body = readFileSync(dest, "utf-8");
    expect(body).toContain(`"#000000"`);
  });

  test("creates parent directory if missing", () => {
    const { calls, deps } = makeStubDeps();
    const nestedDest = join(tmpDir, "deep", "nest", "palette-active.tmux.conf");
    writePaletteFile(MOCHA, "catppuccin-mocha", deps, nestedDest);
    expect(readFileSync(nestedDest, "utf-8")).toContain("@tcm-thm-base");
    expect(calls).toHaveLength(1);
  });

  test("shellTmux throw is caught (running outside a tmux server is OK)", () => {
    const calls: string[][] = [];
    const deps: PaletteFileWriterDeps = {
      shellTmux: (args) => { calls.push(args); throw new Error("no tmux server"); },
      log: () => {},
    };
    expect(() => writePaletteFile(MOCHA, "catppuccin-mocha", deps, dest)).not.toThrow();
    // File was still written before the source-file attempt.
    expect(readFileSync(dest, "utf-8")).toContain("@tcm-thm-base");
    expect(calls).toHaveLength(1);
  });
});
