/**
 * The tail-claude-mux Nerd Font alphabet.
 *
 * Source of truth for codepoints.
 *
 * All glyphs are single column-cell wide. Material Design Icons (`nf-md-*`)
 * are the default family; exceptions are brand letterforms (π/▲/♦),
 * the vendored Clawd glyph, and brail spinners. (The branch glyph used to
 * be Powerline U+E0A0; it now resolves into the MD family as well.)
 *
 * This module is consumed by render code; never compose glyphs ad-hoc in
 * components — import them from here so the design stays auditable.
 */

// ── Severity glyphs (left gutter) ──
// Five states: working / waiting / ready / stopped / error.
// Working is animated via SEV_WORKING_SPINNER frames (legacy brail spinner,
// kept for visual continuity from the redesign vocabulary).
export const SEV_WORKING_SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"] as const;
export const SEV_WAITING = "\u{F009C}";   // nf-md-bell-alert
export const SEV_READY = "\u{F05E1}";     // nf-md-check-circle-outline
export const SEV_STOPPED = "\u{F0667}";   // nf-md-stop-circle
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
export { STATUSLINE_LAST_WINDOW, STATUSLINE_SHELL } from "@tcm/runtime";

// ── Structural & branding glyphs ──
export const BRAND_CLAWD = ID_CLAUDE_CODE;   // header product mark
export const BRANCH_GLYPH = "\u{F062C}";     // nf-md-source-branch
export const DIR_MISMATCH_GLYPH = "\u{F19CB}"; // nf-md-folder-question-outline
export const ACTIVITY_LEAD = "\u{F0142}";    // nf-md-chevron-right
export const ACTIVITY_HEAD = "\u{F0054}";    // nf-md-arrow-right (heading separator)
export const WRAP_UP = "\u{F0143}";          // nf-md-chevron-up (rolodex top)
export const WRAP_DOWN = "\u{F0140}";        // nf-md-chevron-down (rolodex bottom)
