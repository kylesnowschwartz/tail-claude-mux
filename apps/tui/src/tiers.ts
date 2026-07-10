/**
 * Four-tier text hierarchy helpers.
 *
 *   Tier 1 (Primary)   = bold + text colour     — dynamic, urgent
 *   Tier 2 (Secondary) = text colour            — stable context
 *   Tier 3 (Dim)       = faint + text colour    — supporting detail
 *   Tier 4 (Muted)     = overlay0 (no faint)    — static chrome
 *
 * Severity colours and identity glyphs DO NOT slide — they bypass the tier
 * system. See vocab §4 "Severity colours bypass tiers".
 *
 * Pane focus no longer dims the tiers: the panel is read far more often
 * than it is focused, so the header FOCUS chip carries the focus signal
 * and the text always renders at full strength.
 */

import { TextAttributes } from "@opentui/core";
import type { ThemePalette } from "@tcm/runtime";

const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;

export type Tier = "primary" | "secondary" | "dim" | "muted";

/** Style tuple ready to spread into `<span style={...}>`. */
export interface TierStyle {
  fg: string;
  attributes?: number;
}

/** Resolve the style for a tier given the current palette. */
export function tier(t: Tier, palette: ThemePalette): TierStyle {
  switch (t) {
    case "primary":   return { fg: palette.text, attributes: BOLD };
    case "secondary": return { fg: palette.text };
    case "dim":       return { fg: palette.text, attributes: DIM };
    case "muted":     return { fg: palette.overlay0 };
  }
}
