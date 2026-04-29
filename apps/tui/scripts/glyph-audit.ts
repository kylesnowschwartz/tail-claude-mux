#!/usr/bin/env bun
/**
 * Codepoint audit for vocab.ts.
 *
 * Reads vocab.ts, extracts every `\u{XXXXX}` codepoint and its trailing
 * `// nf-md-foo / nf-fa-bar / nf-fae-baz` comment, then validates the
 * codepoint against a SNAPSHOT table sourced from the user's installed
 * SymbolsNerdFontMono-Regular.ttf (verified at audit-creation time).
 *
 * Reports any codepoint whose glyph-at-CP doesn't match its claimed name.
 *
 * Run from anywhere: `bun apps/tui/scripts/glyph-audit.ts`
 *
 * Exit code 0 = clean, 1 = mismatch found.
 *
 * To add a new codepoint, look it up at https://www.nerdfonts.com/cheat-sheet
 * (or use fonttools: `from fontTools.ttLib import TTFont` → `getBestCmap`),
 * verify the actual glyph name, and add an entry to GLYPH_SNAPSHOT below.
 *
 * History: this script was added after we discovered three vocab.ts entries
 * pointed to the WRONG glyphs in the patcher's current cmap layout:
 *   F05CB was account_voice, not record
 *   F009C was bell_outline, not bell_alert
 *   E22B  was palette_color, not book_open_o
 * The bugs were invisible until rendered (the comments said one thing, the
 * codepoints said another) and produced a "person figure with sound waves"
 * where the spec called for a small filled disc.
 */

import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// ── Snapshot of verified (codepoint → glyph name) pairs ──
// Source: SymbolsNerdFontMono-Regular.ttf cmap table, verified 2026-04 with
// `fontTools.ttLib.TTFont(...).getBestCmap()`. Add entries when a new glyph
// joins vocab.ts; never edit an existing entry without re-verifying against
// the font and updating vocab.ts to match.
const GLYPH_SNAPSHOT: Record<string, string> = {
  // ── Severity glyphs ──
  "F0D59": "md-bell_alert",
  "F05E1": "md-check_circle_outline",
  "F0666": "md-stop_circle",
  "F0028": "md-alert_circle",

  // ── Structural & branding ──
  "F062C": "md-source_branch",
  "F19CB": "md-folder_question_outline",
  "F0142": "md-chevron_right",
  "F0054": "md-arrow_right",
  "F0143": "md-chevron_up",
  "F0140": "md-chevron_down",

  // ── Activity-zone gutter ──
  "F044A": "md-record",

  // ── Verb stripe ──
  "E28B":  "fae-book_open_o",
  "F0279": "md-format_list_bulleted",
  "F0968": "md-folder_search",
  "EE75":  "fa-pen_nib",
  "F0BE0": "md-wrench_outline",
  "F059F": "md-web",
  "F167A": "md-robot_outline",
  "F0EB":  "fa-lightbulb_o",      // "fa-lightbulb" was renamed to "_o" in nerd-fonts 3.x
  "F00D":  "fa-xmark",            // "fa-cross" was renamed to "fa-xmark" in nerd-fonts 3.x

  // ── Vendored ──
  "100CC0": "<vendored Clawd glyph (fonts/Clawd.ttf — not in SymbolsNerdFontMono)>",
};

// Codepoints that are intentionally NOT in any nerd-font (Unicode block
// characters, control codes, vendored brand glyphs, etc.). Skipped silently.
const SKIP_RANGES: Array<[number, number, string]> = [
  [0x2580, 0x259F, "Unicode block elements (sparkline)"],
  [0x2500, 0x257F, "Box drawing"],
  [0x2800, 0x28FF, "Braille spinners"],
];

interface VocabEntry {
  constName: string;
  hex: string;          // uppercase, no U+ prefix
  claim: string;        // e.g. "nf-md-bell-alert"
  line: number;
}

function parseVocab(src: string): VocabEntry[] {
  const out: VocabEntry[] = [];
  const lines = src.split("\n");
  // Match: export const NAME = "\u{HEX}"; // nf-FAMILY-glyph-name
  // Also accepts the const NAME on its own line followed by a comment.
  const re =
    /^\s*(?:export\s+)?const\s+(\w+)\s*=\s*"\\u\{([0-9A-Fa-f]+)\}"[^/]*\/\/\s*(nf-[a-z]+-[a-z0-9_-]+)/;
  for (let i = 0; i < lines.length; i++) {
    const m = re.exec(lines[i]!);
    if (m) out.push({ constName: m[1]!, hex: m[2]!.toUpperCase(), claim: m[3]!, line: i + 1 });
  }
  return out;
}

function claimToGlyphName(claim: string): string {
  // "nf-md-bell-alert" → "md-bell_alert"
  // "nf-fa-pen-nib"    → "fa-pen_nib"
  // "nf-fae-book-open-o" → "fae-book_open_o"
  const parts = claim.split("-");
  if (parts.length < 3 || parts[0] !== "nf") return claim;
  const family = parts[1]!;
  const rest = parts.slice(2).join("_");
  return `${family}-${rest}`;
}

function inSkipRange(cp: number): string | null {
  for (const [lo, hi, label] of SKIP_RANGES) {
    if (cp >= lo && cp <= hi) return label;
  }
  return null;
}

function main(): number {
  const here = dirname(fileURLToPath(import.meta.url));
  const vocabPath = join(here, "..", "src", "vocab.ts");
  const src = readFileSync(vocabPath, "utf8");
  const entries = parseVocab(src);

  let mismatches = 0;
  let missing = 0;
  console.log(`Auditing ${entries.length} codepoints in ${vocabPath}\n`);

  for (const e of entries) {
    const cp = parseInt(e.hex, 16);
    const skipReason = inSkipRange(cp);
    if (skipReason) {
      // Silent skip — these aren't nerd-fonts glyphs.
      continue;
    }
    const expected = claimToGlyphName(e.claim);
    const actual = GLYPH_SNAPSHOT[e.hex];
    if (actual === undefined) {
      console.log(
        `  ${e.line.toString().padStart(3)}: ${e.constName.padEnd(28)} U+${e.hex.padStart(5, "0")}  (claims ${e.claim}) — NOT IN SNAPSHOT`,
      );
      missing++;
    } else if (actual !== expected) {
      console.log(
        `  ${e.line.toString().padStart(3)}: ${e.constName.padEnd(28)} U+${e.hex.padStart(5, "0")}  claimed=${expected.padEnd(28)} actual=${actual}`,
      );
      mismatches++;
    }
  }

  console.log();
  console.log(`Total entries: ${entries.length}`);
  console.log(`Mismatches:    ${mismatches}`);
  console.log(`Missing from snapshot: ${missing}`);

  if (mismatches > 0) {
    console.error("\nFAIL: at least one codepoint disagrees with its claimed glyph name.");
    console.error("Either fix the codepoint in vocab.ts or update GLYPH_SNAPSHOT after");
    console.error("verifying against fonts/SymbolsNerdFontMono-Regular.ttf.");
    return 1;
  }
  if (missing > 0) {
    console.log("\nNote: some entries aren't in the snapshot. Add them after verification.");
  }
  return 0;
}

process.exit(main());
