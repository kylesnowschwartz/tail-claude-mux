// Palette file writer (catppuccin pattern).
//
// The bun server is the single writer of ~/.config/tcm/palette-active.tmux.conf,
// a tmux-source-file-able config containing `set -gq @tcm-thm-* "#xxx"` lines
// and the two statusline glyph constants. tcm.tmux sources this file at TPM
// init (before sourcing header.tmux), so every cold boot / kill-server
// restart / prefix+r reload paints the status line with the correct palette
// on first repaint — no flicker, no daemon-cache divergence.
//
// On runtime theme changes (set-theme command, external active-theme.json
// rewrites) the bun server re-writes this file AND issues `tmux source-file`
// to apply live without waiting for the next config reload.
//
// Design rationale: hooks + palette + format strings used to be three
// separately-managed pieces of state (two in tmux, one in the bun server's
// diff cache). After `tmux kill-server` the tmux side reset to empty but the
// daemon's diff cache didn't, leaving the new tmux server with no palette.
// Moving palette emission to a file that tcm.tmux sources at TPM init makes
// the bun server irrelevant to the cold-start palette path: tmux owns the
// declarative read; the daemon owns ephemeral writes during runtime theme
// changes only.

import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import type { Theme } from "../themes";
import { STATUSLINE_LAST_WINDOW, STATUSLINE_SHELL } from "./tmux-header-sync";

/** Mirrors PALETTE_TOKENS in tmux-header-sync.ts. The active palette file
 *  must contain exactly these tokens — the vendored fallback at
 *  integrations/tmux-plugin/themes/default-palette.tmux.conf agrees. */
const PALETTE_TOKENS = [
  "base", "text", "blue", "surface0", "surface2",
  "overlay0", "yellow", "red", "green",
] as const satisfies readonly (keyof Theme["palette"])[];

/** Default destination path. Co-located with the rest of tcm's user config. */
export const PALETTE_FILE_PATH: string = join(
  homedir(), ".config", "tcm", "palette-active.tmux.conf",
);

/** Translate an tcm palette value to a tmux-renderable colour.
 *  The "transparent" theme stores the literal string "transparent" for base
 *  surfaces; tmux understands "default" but not "transparent". Mirrors
 *  toTmuxColour() in tmux-header-sync.ts — keep the two in lockstep. */
function toTmuxColour(value: string): string {
  return value === "transparent" ? "default" : value;
}

/** Build the file body — same shape as the vendored fallback so a diff is
 *  human-readable. Each option uses `-gq` (global, quiet) so a re-source
 *  silently overwrites without erroring on already-set options. */
export function buildPaletteFileBody(theme: Theme, themeName: string | undefined): string {
  const header = themeName
    ? `# tcm tmux palette — active (${themeName}). Auto-generated; do not edit.`
    : "# tcm tmux palette — active. Auto-generated; do not edit.";
  const lines: string[] = [
    header,
    "# Single writer: packages/runtime/src/server/tmux-palette-file.ts.",
    "# Read by: tcm.tmux at TPM init; bun server re-runs `tmux source-file` on theme change.",
    "",
  ];
  for (const token of PALETTE_TOKENS) {
    const value = toTmuxColour(theme.palette[token]);
    // Pad token name to keep columns aligned with the vendored default for diffability.
    lines.push(`set -gq @tcm-thm-${token.padEnd(8)} "${value}"`);
  }
  lines.push("");
  lines.push(`set -gq @tcm-shell-glyph        "${STATUSLINE_SHELL}"`);
  lines.push(`set -gq @tcm-last-window-glyph  "${STATUSLINE_LAST_WINDOW}"`);
  lines.push("");
  return lines.join("\n");
}

export interface PaletteFileWriterDeps {
  /** Run a tmux command. Caller-provided so tests can stub. Throw on failure
   *  is fine — the writer catches it and logs. */
  shellTmux: (args: string[]) => string;
  /** Structured logger; same shape as the server's `log()`. */
  log: (msg: string, data?: Record<string, unknown>) => void;
}

/** State kept across calls so an unchanged theme doesn't trigger a redundant
 *  file write or `tmux source-file` (cheap subprocess, but pointless I/O). */
let lastWrittenBody: string | null = null;

export function __resetPaletteFileWriterStateForTests(): void {
  lastWrittenBody = null;
}

/** Write the palette file and apply it live via `tmux source-file -q`. Idempotent
 *  on body content (same theme → same body → no-op). The `-q` flag swallows
 *  the no-such-tmux-server error so we can safely call this from a bun
 *  process not running inside a tmux session (e.g. external triggers). */
export function writePaletteFile(
  theme: Theme,
  themeName: string | undefined,
  deps: PaletteFileWriterDeps,
  destPath: string = PALETTE_FILE_PATH,
): void {
  const body = buildPaletteFileBody(theme, themeName);
  if (body === lastWrittenBody) {
    deps.log("palette-file unchanged", { themeName });
    return;
  }
  try {
    const dir = dirname(destPath);
    if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
    writeFileSync(destPath, body);
    lastWrittenBody = body;
    deps.log("palette-file written", { path: destPath, themeName, bytes: body.length });
  } catch (err) {
    deps.log("palette-file write failed", { error: String(err), path: destPath });
    return;
  }
  // Apply live. If we're not inside a tmux server (or the server doesn't have
  // this conf yet sourced by anyone), -q swallows the error.
  try {
    deps.shellTmux(["source-file", "-q", destPath]);
  } catch (err) {
    deps.log("palette-file source-file failed", { error: String(err) });
  }
}
