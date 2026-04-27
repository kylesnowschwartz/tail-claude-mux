/**
 * The tail-claude-mux Nerd Font alphabet.
 *
 * Source of truth: docs/design/03-vocabulary.md §10 codepoint cheat-sheet.
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
// kept for visual continuity per docs/design/03-vocabulary.md §2).
export const SEV_WORKING_SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"] as const;
export const SEV_WAITING = "\u{F009C}";   // nf-md-bell-alert
export const SEV_READY = "\u{F05E1}";     // nf-md-check-circle-outline
export const SEV_STOPPED = "\u{F0667}";   // nf-md-stop-circle
export const SEV_ERROR = "\u{F0028}";     // nf-md-alert-circle

/** Determinate-progress variant for `working` (8 fill levels).
 *  Used when `metadata.progress.percent` is set; replaces the brail spinner.
 *  See docs/design/03-vocabulary.md §2 "Determinate-progress variant". */
export const PROGRESS_GLYPHS = [
  "\u{F0A9E}", "\u{F0A9F}", "\u{F0AA0}", "\u{F0AA1}",
  "\u{F0AA2}", "\u{F0AA3}", "\u{F0AA4}", "\u{F0AA5}",
] as const;

/** Pick the determinate-progress glyph for a given percent (0..1). */
export function progressGlyph(percent: number): string {
  const clamped = Math.max(0, Math.min(1, percent));
  const idx = Math.min(PROGRESS_GLYPHS.length - 1, Math.floor(clamped * PROGRESS_GLYPHS.length));
  return PROGRESS_GLYPHS[idx]!;
}

// ── Identity glyphs (right gutter) ──
// One per known agent type plus a generic fallback. The same table is
// shared with the tmux statusline (see packages/runtime/src/server/
// tmux-header-sync.ts AGENT_GLYPHS) — keep them aligned.
export const ID_CLAUDE_CODE = "\u{100CC0}"; // Clawd, vendored at fonts/Clawd.ttf
export const ID_PI = "\u{03C0}";             // π
export const ID_CODEX = "\u{25B2}";          // ▲
export const ID_AMP = "\u{2666}";            // ♦
export const ID_GENERIC = "\u{F167A}";       // nf-md-robot-outline

// ── Statusline-only glyphs (tmux header) ──
// These never appear in the panel — the panel uses agent identity glyphs
// or severity glyphs in those positions. They're declared here so the
// vocabulary doc has a single source of truth across surfaces.
// See `integrations/tmux-plugin/scripts/header.tmux` for usage.
export const STATUSLINE_SHELL = "\u{EA85}";    // nf-cod-terminal (boxed >_)
export const STATUSLINE_LAST_WINDOW = "\u{F17B3}"; // nf-md-arrow_u_left — last-visited-window marker (curl-back)

/** Resolve an identity glyph for a known agent type, falling back to generic. */
export function identityGlyph(agent: string): string {
  switch (agent) {
    case "claude-code": return ID_CLAUDE_CODE;
    case "pi":          return ID_PI;
    case "codex":       return ID_CODEX;
    case "amp":         return ID_AMP;
    default:            return ID_GENERIC;
  }
}

/** Two-letter agent code used in activity-zone source columns.
 *  See docs/design/03-vocabulary.md §7 "Source format". */
export function agentCode(agent: string): string {
  switch (agent) {
    case "claude-code": return "cc";
    case "pi":          return "pi";
    case "codex":       return "cd";
    case "amp":         return "ap";
    default:            return agent.slice(0, 2);
  }
}

// ── Structural & branding glyphs ──
export const BRAND_CLAWD = ID_CLAUDE_CODE;   // header product mark
export const BRANCH_GLYPH = "\u{F062C}";     // nf-md-source-branch
export const FOLDER_GLYPH = "\u{F0770}";     // nf-md-folder-outline
export const ACTIVITY_LEAD = "\u{F0142}";    // nf-md-chevron-right
export const ACTIVITY_HEAD = "\u{F0054}";    // nf-md-arrow-right (heading separator)
export const WRAP_UP = "\u{F0143}";          // nf-md-chevron-up (rolodex top)
export const WRAP_DOWN = "\u{F0140}";        // nf-md-chevron-down (rolodex bottom)

/** Box-drawing for the focused-card border.
 *  OpenTUI's `border` prop owns the actual rendering; these are exported
 *  for any custom-built borders or mockup tooling. */
export const BORDER_TL = "\u{256D}";
export const BORDER_TR = "\u{256E}";
export const BORDER_BL = "\u{2570}";
export const BORDER_BR = "\u{256F}";
export const BORDER_H = "\u{2500}";
export const BORDER_V = "\u{2502}";

// ── Five-state agent labels ──
// Locked vocabulary used across the design. Each maps to one severity
// glyph and one severity colour (resolved per palette in tiers.ts).
export type SeverityLabel = "working" | "waiting" | "ready" | "stopped" | "error";

/** Resolve the severity glyph for a label.
 *  `working` requires a frame index because it animates. */
export function severityGlyph(label: SeverityLabel, spinnerFrame: number = 0): string {
  switch (label) {
    case "working": return SEV_WORKING_SPINNER[spinnerFrame % SEV_WORKING_SPINNER.length]!;
    case "waiting": return SEV_WAITING;
    case "ready":   return SEV_READY;
    case "stopped": return SEV_STOPPED;
    case "error":   return SEV_ERROR;
  }
}
