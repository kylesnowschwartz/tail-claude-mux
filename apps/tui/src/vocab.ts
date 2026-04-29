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
 * Plus brand letterforms (π/▲/♦), the vendored Clawd glyph, brail
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
export const SEV_WAITING = "\u{F0178}";   // nf-md-bell-alert (matches tail-claude-hud)
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

// ── Activity-zone glyphs (sparkline + gutter + verb stripe) ──
// Verb glyphs come from three nerd-fonts families (see header). The palette
// is aligned with tail-claude-hud's tool-category icons so a reader who
// learns one app's vocabulary recognises the other.
//
// The sparkline alphabet is plain Unicode block characters (U+2581–U+2588)
// which are EAW Neutral and need no glyph budget; they're inlined where used.
//
// See docs/simmer/activity-zone/result.md §Glyph palette for rationale.
export const ACTIVITY_GUTTER_FRESH = "\u{F05CB}"; // nf-md-record (filled small disc — "now")

// ── Verb glyphs (col 1, the verb stripe) ──
// Five core verbs are derived client-side by classify.ts. The remaining
// glyphs are wired into VERB_GLYPHS for forward-compatibility with watchers
// that emit web/task/skill/thinking events.
export const ACTIVITY_VERB_READ     = "\u{E22B}";  // nf-fae-book-open-o (was nf-md-eye)
export const ACTIVITY_VERB_LIST     = "\u{F0279}"; // nf-md-format-list-bulleted (kept; hud has no list)
export const ACTIVITY_VERB_SEARCH   = "\u{F0968}"; // nf-md-folder-search (was nf-md-magnify)
export const ACTIVITY_VERB_EDIT     = "\u{EE75}";  // nf-fa-pen-nib (was nf-md-pencil)
export const ACTIVITY_VERB_RUN      = "\u{F0BE0}"; // nf-md-wrench-outline (was nf-md-play)
export const ACTIVITY_VERB_WEB      = "\u{F059F}"; // nf-md-web
export const ACTIVITY_VERB_TASK     = "\u{F167A}"; // nf-md-robot-outline (sub-agent / Task tool)
export const ACTIVITY_VERB_SKILL    = "\u{F0BE0}"; // nf-md-wrench-outline (alias of RUN by design)
export const ACTIVITY_VERB_THINKING = "\u{F0EB}";  // nf-fa-lightbulb (thinking-block content)
// Stripe-internal error glyph (col 1) — distinct from gutter SEV_ERROR (col 0).
// Both can co-exist on an error row: gutter signals severity, stripe signals
// "this row's tool failed" in the verb column.
export const ACTIVITY_VERB_ERROR    = "\u{F00D}";  // nf-fa-cross
