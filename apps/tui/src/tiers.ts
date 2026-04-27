/**
 * Four-tier text hierarchy helpers.
 *
 *   Tier 1 (Primary)   = bold + text colour     — dynamic, urgent
 *   Tier 2 (Secondary) = text colour            — stable context
 *   Tier 3 (Dim)       = faint + text colour    — supporting detail
 *   Tier 4 (Muted)     = overlay0 (no faint)    — static chrome
 *
 * When the panel pane is unfocused, every tier slides one step dimmer:
 *   text → subtext0, overlay0 → surface2.
 *
 * Severity colours and identity glyphs DO NOT slide — they bypass the tier
 * system. Pane focus only affects *text*. See vocab §4 "Severity colours
 * bypass tiers".
 */

import { TextAttributes } from "@opentui/core";
import type { ThemePalette } from "@tcm/runtime";

const BOLD = TextAttributes.BOLD;
const DIM = TextAttributes.DIM;
const ITALIC = TextAttributes.ITALIC;

export type Tier = "primary" | "secondary" | "dim" | "muted";

/** Style tuple ready to spread into `<span style={...}>`. */
export interface TierStyle {
  fg: string;
  attributes?: number;
}

/** Resolve the style for a tier given the current palette and pane focus. */
export function tier(t: Tier, palette: ThemePalette, paneFocused: boolean): TierStyle {
  if (paneFocused) {
    switch (t) {
      case "primary":   return { fg: palette.text, attributes: BOLD };
      case "secondary": return { fg: palette.text };
      case "dim":       return { fg: palette.text, attributes: DIM };
      case "muted":     return { fg: palette.overlay0 };
    }
  } else {
    // Unfocused mirror: text → subtext0, overlay0 → surface2.
    switch (t) {
      case "primary":   return { fg: palette.subtext0, attributes: BOLD };
      case "secondary": return { fg: palette.subtext0 };
      case "dim":       return { fg: palette.subtext0, attributes: DIM };
      case "muted":     return { fg: palette.surface2 };
    }
  }
}

/**
 * Style for the activity-zone description column.
 *
 * Italic is sanctioned only here. Fresh entries step UP to Tier 2 + italic
 * to distinguish "just happened" from history. Older entries fall back to
 * Tier 3 + italic. See vocab §4 "Italic as a sanctioned modifier" and §7
 * "Entry shape".
 */
export function activityDescription(palette: ThemePalette, paneFocused: boolean, fresh: boolean): TierStyle {
  const base = tier(fresh ? "secondary" : "dim", palette, paneFocused);
  return {
    fg: base.fg,
    attributes: (base.attributes ?? 0) | ITALIC,
  };
}

