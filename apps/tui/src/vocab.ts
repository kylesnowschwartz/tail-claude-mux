/**
 * The tail-claude-mux Nerd Font alphabet.
 *
 * Source of truth for codepoints.
 *
 * All glyphs are single column-cell wide. The palette intentionally mixes
 * three nerd-fonts families chosen for semantic fit, not visual purity:
 *
 *   nf-md  (Material Design Icons, U+F0000–U+F1FFF) — default modern set
 *   nf-fa  (Font Awesome 4, U+F000–U+F2E0)         — used where MD lacks a
 *                                                     better fit (e.g.
 *                                                     pen-nib for Edit,
 *                                                     lightbulb for Thinking,
 *                                                     cross for Error).
 *   nf-fae (Font Awesome Extension, U+E200–U+E2A9)  — used for book-open Read.
 *
 * Plus brand letterforms (π/♦, hexagon for codex), the vendored Clawd
 * glyph, brail
 * spinners, and Powerline branch (legacy fallback).
 *
 * This module is consumed by render code; never compose glyphs ad-hoc in
 * components — import them from here so the design stays auditable.
 */

// ── Severity glyphs (left gutter) ──
// Five states: working / waiting / ready / stopped / error.
// Working is animated via SEV_WORKING_SPINNER frames (legacy brail spinner,
// kept for visual continuity from the redesign vocabulary).
export const SEV_WORKING_SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"] as const;
export const SEV_WAITING = "\u{F0D59}";   // nf-md-bell-alert (filled bell + alert flick)
export const SEV_READY = "\u{F05E1}";     // nf-md-check-circle-outline
export const SEV_STOPPED = "\u{F0666}";   // nf-md-stop-circle (was U+F0667 stop_circle_outline)
export const SEV_ERROR = "\u{F0028}";     // nf-md-alert-circle

// ── Identity glyphs (right gutter) ──
const ID_CLAUDE_CODE = "\u{100CC0}"; // Clawd, vendored at fonts/Clawd.ttf

// ── Statusline-only glyphs (tmux header) ──
// Re-exported from `@tcm/runtime` (where they're declared next to the
// per-window AGENT_GLYPHS table). The runtime emits them as server-global
// tmux user-options that `integrations/tmux-plugin/scripts/header.tmux`
// reads via `#{@tcm-last-window-glyph}` / `#{@tcm-shell-glyph}`. These never
// appear in the panel — they live here so vocab.ts stays the single
// reader-facing entry-point for every glyph in the codebase.
export { STATUSLINE_LAST_WINDOW, STATUSLINE_SHELL, AGENT_GLYPHS } from "@tcm/runtime";

// ── Structural & branding glyphs ──
export const BRAND_CLAWD = ID_CLAUDE_CODE;   // header product mark
export const BRANCH_GLYPH = "\u{F062C}";     // nf-md-source-branch


// ── Liaison block ──
export const LIAISON_GLYPH = "\u{F1308}";   // nf-md-account-tie-voice
export const LIAISON_WORKER_GLYPH = "\u{F060D}"; // nf-md-subdirectory_arrow_right: a row an open liaison started
export const LIAISON_FINISHED_GLYPH = SEV_READY;  // nf-md-check-circle-outline: a worker row the liaison reports finished and still open
export const LIAISON_WORKERS_MARK = "↳";         // before the count of live conversations the liaison started
/** Item glyph per priority: 1 the user's action now, 2 a report or answer due, 3 a reminder. */
export const LIAISON_ITEM_GLYPHS = { 1: "■", 2: "▲", 3: "●" } as const;

// ── "Needs you" block ──
export const NEEDS_GLYPH = "\u{F1295}";     // nf-md-tray-alert
export const REASON_GLYPHS = {
  permission: "\u{F099D}",                  // nf-md-shield-lock
  question: "\u{F1738}",                    // nf-md-chat-question
  input: "\u{F030C}",                       // nf-md-keyboard
  error: SEV_ERROR,                         // nf-md-alert-circle
  finished: SEV_READY,                      // nf-md-check-circle-outline
} as const;
