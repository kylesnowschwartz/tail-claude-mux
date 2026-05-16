/**
 * Width-aware text helpers + boundary sanitization.
 *
 * Why this file exists: every user-derived string that lands in the sidebar
 * (thread names, cwds, tool inputs) has two failure modes that `.slice()` and
 * `.length` silently produce:
 *
 *   1. CJK / fullwidth / emoji characters occupy 2 terminal cells but count as
 *      1-2 code units, so column-budgeted layouts (the activity zone's
 *      sparkline, the verb stripe) misalign the moment a wide char shows up.
 *
 *   2. Raw C0/C1 control bytes pasted into a user prompt propagate through to
 *      the renderer and either disturb layout or get reinterpreted as escape
 *      sequences by the host terminal.
 *
 * Treat every string crossing from agent payload → sidebar render through
 * `sanitizeForDisplay()` once, at the boundary. Downstream code can then
 * trust the input is printable and use `stringWidth` / `truncateToWidth` for
 * layout instead of `.length` / `.slice()`.
 *
 * No regexes are used for control-char detection — encoding control bytes
 * portably across file-write / source-read tooling is brittle, and the
 * codePointAt / charCodeAt loops below are just as fast at our scale.
 */

const ESC_CODE = 0x1b;
const BEL_CODE = 0x07;
const CSI_OPEN_CODE = 0x5b; // '['
const OSC_OPEN_CODE = 0x5d; // ']'

/**
 * Terminal display width of a single Unicode code point.
 * Returns 2 for fullwidth / wide characters, 0 for zero-width, 1 otherwise.
 * Adapted from honeymux/src/util/text.ts (charWidth). The ranges cover the
 * common cases — CJK, Hangul, fullwidth ASCII, Hiragana/Katakana, modern
 * emoji blocks, plus zero-width combining marks and variation selectors.
 *
 * Limitation: this is code-point-based, not grapheme-cluster-based. ZWJ emoji
 * families (e.g. 👨‍👩‍👧 = three emoji + two ZWJ) over-count vs what a modern
 * terminal renders as a single 2-cell cluster. Matches `string-width`/`wcwidth`
 * conventions and is what column-budgeted layouts elsewhere in tcm already
 * assume — fix grapheme-awareness everywhere or nowhere.
 */
function charWidth(ch: string): number {
  const code = ch.codePointAt(0) ?? 0;
  // Zero-width
  if (code === 0 || (code >= 0x0300 && code <= 0x036f)) return 0; // combining diacritics
  if (code >= 0xfe00 && code <= 0xfe0f) return 0; // variation selectors
  if (code >= 0x200b && code <= 0x200f) return 0; // zero-width spaces/joiners
  if (code === 0xfeff) return 0; // BOM
  // Fullwidth / wide
  if (code >= 0x1100 && code <= 0x115f) return 2; // Hangul Jamo
  if (code >= 0x2e80 && code <= 0x303e) return 2; // CJK Radicals, Kangxi, Ideographic
  if (code >= 0x3040 && code <= 0x33bf) return 2; // Hiragana, Katakana, CJK
  if (code >= 0x3400 && code <= 0x4dbf) return 2; // CJK Extension A
  if (code >= 0x4e00 && code <= 0xa4cf) return 2; // CJK Unified + Yi
  if (code >= 0xac00 && code <= 0xd7af) return 2; // Hangul Syllables
  if (code >= 0xf900 && code <= 0xfaff) return 2; // CJK Compatibility Ideographs
  if (code >= 0xfe30 && code <= 0xfe6f) return 2; // CJK Compatibility Forms
  if (code >= 0xff01 && code <= 0xff60) return 2; // Fullwidth ASCII
  if (code >= 0xffe0 && code <= 0xffe6) return 2; // Fullwidth symbols
  if (code >= 0x20000 && code <= 0x2fa1f) return 2; // CJK extensions B-F + supplement
  if (code >= 0x30000 && code <= 0x323af) return 2; // CJK extension G-I
  if (code >= 0x1f000 && code <= 0x1f02f) return 2; // Mahjong, Dominos
  if (code >= 0x1f0a0 && code <= 0x1f0ff) return 2; // Playing cards
  if (code >= 0x1f100 && code <= 0x1f1ff) return 2; // Enclosed Alphanumerics
  if (code >= 0x1f200 && code <= 0x1f2ff) return 2; // Enclosed Ideographic
  if (code >= 0x1f300 && code <= 0x1fbff) return 2; // Misc Symbols, Emoticons
  return 1;
}

/** Terminal display width (in cells) of a string. */
export function stringWidth(s: string): number {
  let w = 0;
  for (const ch of s) w += charWidth(ch);
  return w;
}

/**
 * Truncate text so its display width never exceeds maxWidth.
 * Appends a single-cell ellipsis when truncation actually occurs.
 * Returns "" when maxWidth <= 0.
 */
export function truncateToWidth(text: string, maxWidth: number): string {
  if (maxWidth <= 0) return "";
  if (stringWidth(text) <= maxWidth) return text;
  const suffix = "…"; // …
  if (maxWidth <= 1) return suffix;
  let width = 0;
  let out = "";
  const budget = maxWidth - 1; // reserve one cell for the ellipsis
  for (const ch of text) {
    const cw = charWidth(ch);
    if (width + cw > budget) break;
    out += ch;
    width += cw;
  }
  return out + suffix;
}

/**
 * Strip C0 (0x00-0x1F) and C1 (0x7F-0x9F) control code points. Apply to every
 * user-derived string before it lands in the renderer.
 */
export function stripNonPrintingControlChars(text: string): string {
  let out = "";
  for (const ch of text) {
    const c = ch.codePointAt(0) ?? 0;
    if ((c >= 0x00 && c <= 0x1f) || (c >= 0x7f && c <= 0x9f)) continue;
    out += ch;
  }
  return out;
}

/**
 * Strip CSI / OSC ANSI escape sequences. Used in concert with
 * stripNonPrintingControlChars to defend against bracket-form sequences whose
 * leading ESC has somehow survived earlier sanitization, and as the primary
 * sanitizer when callers want to preserve other control bytes.
 *
 * CSI: ESC [ ... finalByte (0x40-0x7E)
 * OSC: ESC ] ... BEL (0x07)
 */
export function stripAnsiEscapes(text: string): string {
  let out = "";
  let i = 0;
  while (i < text.length) {
    const c = text.charCodeAt(i);
    if (c === ESC_CODE && i + 1 < text.length) {
      const next = text.charCodeAt(i + 1);
      if (next === CSI_OPEN_CODE) {
        // CSI — consume until a final byte in 0x40-0x7E inclusive.
        let j = i + 2;
        while (j < text.length) {
          const x = text.charCodeAt(j);
          j++;
          if (x >= 0x40 && x <= 0x7e) break;
        }
        i = j;
        continue;
      }
      if (next === OSC_OPEN_CODE) {
        // OSC — consume until BEL.
        let j = i + 2;
        while (j < text.length && text.charCodeAt(j) !== BEL_CODE) j++;
        i = j + 1;
        continue;
      }
    }
    out += text[i]!;
    i++;
  }
  return out;
}

/**
 * Single canonical sanitizer for strings crossing from agent payload to
 * sidebar render. Strips ANSI bracket-sequences first (while the ESC byte is
 * still present to anchor on), then strips remaining C0/C1 control bytes.
 */
export function sanitizeForDisplay(text: string): string {
  return stripNonPrintingControlChars(stripAnsiEscapes(text));
}
