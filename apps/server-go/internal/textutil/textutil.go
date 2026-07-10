// Package textutil ports packages/runtime/src/text.ts — width-aware text
// helpers plus boundary sanitization. Every user-derived string that lands
// in the sidebar (thread names, tool inputs, pane titles) passes through
// SanitizeForDisplay once at the boundary; layout code then budgets columns
// with StringWidth/TruncateToWidth instead of len().
//
// Width is code-point-based, not grapheme-cluster-based, matching the TS
// implementation (and string-width/wcwidth conventions): ZWJ emoji families
// over-count vs what a modern terminal renders. Fix grapheme-awareness
// everywhere or nowhere.
package textutil

import "strings"

const (
	escCode     = 0x1b
	belCode     = 0x07
	csiOpenCode = '['
	oscOpenCode = ']'
	// SessionNameMaxWidth caps agent session/thread display names.
	SessionNameMaxWidth = 80
)

// charWidth is the terminal display width of a single code point: 2 for
// fullwidth/wide, 0 for zero-width, 1 otherwise. Ranges mirror text.ts.
func charWidth(r rune) int {
	switch {
	// Zero-width
	case r == 0 || (r >= 0x0300 && r <= 0x036f): // combining diacritics
		return 0
	case r >= 0xfe00 && r <= 0xfe0f: // variation selectors
		return 0
	case r >= 0x200b && r <= 0x200f: // zero-width spaces/joiners
		return 0
	case r == 0xfeff: // BOM
		return 0
	// Fullwidth / wide
	case r >= 0x1100 && r <= 0x115f: // Hangul Jamo
		return 2
	case r >= 0x2e80 && r <= 0x303e: // CJK Radicals, Kangxi, Ideographic
		return 2
	case r >= 0x3040 && r <= 0x33bf: // Hiragana, Katakana, CJK
		return 2
	case r >= 0x3400 && r <= 0x4dbf: // CJK Extension A
		return 2
	case r >= 0x4e00 && r <= 0xa4cf: // CJK Unified + Yi
		return 2
	case r >= 0xac00 && r <= 0xd7af: // Hangul Syllables
		return 2
	case r >= 0xf900 && r <= 0xfaff: // CJK Compatibility Ideographs
		return 2
	case r >= 0xfe30 && r <= 0xfe6f: // CJK Compatibility Forms
		return 2
	case r >= 0xff01 && r <= 0xff60: // Fullwidth ASCII
		return 2
	case r >= 0xffe0 && r <= 0xffe6: // Fullwidth symbols
		return 2
	case r >= 0x20000 && r <= 0x2fa1f: // CJK extensions B-F + supplement
		return 2
	case r >= 0x30000 && r <= 0x323af: // CJK extension G-I
		return 2
	case r >= 0x1f000 && r <= 0x1f02f: // Mahjong, Dominos
		return 2
	case r >= 0x1f0a0 && r <= 0x1f0ff: // Playing cards
		return 2
	case r >= 0x1f100 && r <= 0x1f1ff: // Enclosed Alphanumerics
		return 2
	case r >= 0x1f200 && r <= 0x1f2ff: // Enclosed Ideographic
		return 2
	case r >= 0x1f300 && r <= 0x1fbff: // Misc Symbols, Emoticons
		return 2
	default:
		return 1
	}
}

// StringWidth is the terminal display width (in cells) of a string.
func StringWidth(s string) int {
	w := 0
	for _, r := range s {
		w += charWidth(r)
	}
	return w
}

// TruncateToWidth truncates text so its display width never exceeds
// maxWidth, appending a single-cell ellipsis when truncation occurs.
// Returns "" when maxWidth <= 0.
func TruncateToWidth(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if StringWidth(text) <= maxWidth {
		return text
	}
	const suffix = "…"
	if maxWidth <= 1 {
		return suffix
	}
	budget := maxWidth - 1 // reserve one cell for the ellipsis
	width := 0
	var out strings.Builder
	for _, r := range text {
		cw := charWidth(r)
		if width+cw > budget {
			break
		}
		out.WriteRune(r)
		width += cw
	}
	return out.String() + suffix
}

// StripNonPrintingControlChars strips C0 (0x00-0x1F) and C1 (0x7F-0x9F)
// control code points.
func StripNonPrintingControlChars(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		if (r >= 0x00 && r <= 0x1f) || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// StripAnsiEscapes strips CSI (ESC [ ... 0x40-0x7E) and OSC (ESC ] ... BEL)
// sequences. Run before StripNonPrintingControlChars so the ESC byte is
// still present to anchor on.
func StripAnsiEscapes(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	i := 0
	for i < len(text) {
		if text[i] == escCode && i+1 < len(text) {
			switch text[i+1] {
			case csiOpenCode:
				j := i + 2
				for j < len(text) {
					x := text[j]
					j++
					if x >= 0x40 && x <= 0x7e {
						break
					}
				}
				i = j
				continue
			case oscOpenCode:
				j := i + 2
				for j < len(text) && text[j] != belCode {
					j++
				}
				i = j + 1
				continue
			}
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

// SanitizeForDisplay is the single canonical sanitizer for strings crossing
// from agent payload to sidebar render: ANSI bracket-sequences first, then
// remaining C0/C1 control bytes.
func SanitizeForDisplay(text string) string {
	return StripNonPrintingControlChars(StripAnsiEscapes(text))
}

// SanitizeSessionName applies the shared sanitizer and width cap used for
// agent session/thread display names.
func SanitizeSessionName(text string) string {
	return TruncateToWidth(SanitizeForDisplay(text), SessionNameMaxWidth)
}
