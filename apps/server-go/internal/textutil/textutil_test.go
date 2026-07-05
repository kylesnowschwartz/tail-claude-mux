// Port of packages/runtime/test/text.test.ts. Subtest names mirror the TS
// test names one-to-one so drift between the two suites is easy to spot.
package textutil

import (
	"strings"
	"testing"
)

func TestStringWidth(t *testing.T) {
	t.Run("ASCII is one cell per char", func(t *testing.T) {
		if got := StringWidth(""); got != 0 {
			t.Errorf("StringWidth(%q) = %d, want 0", "", got)
		}
		if got := StringWidth("hello"); got != 5 {
			t.Errorf("StringWidth(%q) = %d, want 5", "hello", got)
		}
		if got := StringWidth("a b c"); got != 5 {
			t.Errorf("StringWidth(%q) = %d, want 5", "a b c", got)
		}
	})

	t.Run("CJK is two cells per char", func(t *testing.T) {
		if got := StringWidth("漢字"); got != 4 {
			t.Errorf("StringWidth(%q) = %d, want 4", "漢字", got)
		}
		if got := StringWidth("a漢b"); got != 4 {
			t.Errorf("StringWidth(%q) = %d, want 4", "a漢b", got)
		}
		if got := StringWidth("한국어"); got != 6 {
			t.Errorf("StringWidth(%q) = %d, want 6", "한국어", got)
		}
		if got := StringWidth("こんにちは"); got != 10 {
			t.Errorf("StringWidth(%q) = %d, want 10", "こんにちは", got)
		}
	})

	t.Run("emoji is two cells", func(t *testing.T) {
		if got := StringWidth("🚀"); got != 2 {
			t.Errorf("StringWidth(%q) = %d, want 2", "🚀", got)
		}
		if got := StringWidth("a🚀b"); got != 4 {
			t.Errorf("StringWidth(%q) = %d, want 4", "a🚀b", got)
		}
	})

	t.Run("combining marks are zero-width", func(t *testing.T) {
		// "e" + combining acute (U+0301)
		if got := StringWidth("é"); got != 1 {
			t.Errorf("StringWidth(e+U+0301) = %d, want 1", got)
		}
	})

	t.Run("zero-width joiner does not add cells", func(t *testing.T) {
		// "a" + U+200B (zero-width space) + "b", as in the TS source string.
		if got := StringWidth("a​b"); got != 2 {
			t.Errorf("StringWidth(a+U+200B+b) = %d, want 2", got)
		}
	})
}

func TestTruncateToWidth(t *testing.T) {
	t.Run("returns input unchanged when within budget", func(t *testing.T) {
		if got := TruncateToWidth("hello", 10); got != "hello" {
			t.Errorf("TruncateToWidth(%q, 10) = %q, want %q", "hello", got, "hello")
		}
		if got := TruncateToWidth("hello", 5); got != "hello" {
			t.Errorf("TruncateToWidth(%q, 5) = %q, want %q", "hello", got, "hello")
		}
	})

	t.Run("appends ellipsis when truncating ASCII", func(t *testing.T) {
		if got := TruncateToWidth("hello world", 8); got != "hello w…" {
			t.Errorf("TruncateToWidth(%q, 8) = %q, want %q", "hello world", got, "hello w…")
		}
		// 8-cell budget: 7 chars + "…" = 8 cells.
		if got := StringWidth(TruncateToWidth("hello world", 8)); got != 8 {
			t.Errorf("StringWidth(TruncateToWidth(%q, 8)) = %d, want 8", "hello world", got)
		}
	})

	t.Run("respects CJK width when truncating", func(t *testing.T) {
		// "漢字漢字" is 8 cells. With max=5 we must fit "X…" where X is one CJK (2)
		// plus the ellipsis (1) = 3 cells. Two CJK + ellipsis = 5 cells.
		out := TruncateToWidth("漢字漢字", 5)
		if w := StringWidth(out); w > 5 {
			t.Errorf("StringWidth(%q) = %d, want <= 5", out, w)
		}
		if !strings.HasSuffix(out, "…") {
			t.Errorf("TruncateToWidth(%q, 5) = %q, want ellipsis suffix", "漢字漢字", out)
		}
	})

	t.Run("returns just ellipsis when maxWidth is 1", func(t *testing.T) {
		if got := TruncateToWidth("anything", 1); got != "…" {
			t.Errorf("TruncateToWidth(%q, 1) = %q, want %q", "anything", got, "…")
		}
	})

	t.Run("returns empty when maxWidth is 0 or negative", func(t *testing.T) {
		if got := TruncateToWidth("anything", 0); got != "" {
			t.Errorf("TruncateToWidth(%q, 0) = %q, want empty", "anything", got)
		}
		if got := TruncateToWidth("anything", -3); got != "" {
			t.Errorf("TruncateToWidth(%q, -3) = %q, want empty", "anything", got)
		}
	})

	t.Run("does not split a wide char mid-codepoint", func(t *testing.T) {
		// "漢漢" = 4 cells. Budget 3: ellipsis takes 1, leaving 2 — exactly one 漢.
		out := TruncateToWidth("漢漢", 3)
		if out != "漢…" {
			t.Errorf("TruncateToWidth(%q, 3) = %q, want %q", "漢漢", out, "漢…")
		}
		if w := StringWidth(out); w != 3 {
			t.Errorf("StringWidth(%q) = %d, want 3", out, w)
		}
	})
}

func TestStripNonPrintingControlChars(t *testing.T) {
	t.Run("removes C0 controls", func(t *testing.T) {
		if got := StripNonPrintingControlChars("a\x00b\x01c"); got != "abc" {
			t.Errorf("got %q, want %q", got, "abc")
		}
		if got := StripNonPrintingControlChars("line\rreturn"); got != "linereturn" {
			t.Errorf("got %q, want %q", got, "linereturn")
		}
		if got := StripNonPrintingControlChars("tab\there"); got != "tabhere" {
			t.Errorf("got %q, want %q", got, "tabhere")
		}
	})

	t.Run("removes ESC and BEL", func(t *testing.T) {
		if got := StripNonPrintingControlChars("\x1b[31mred\x1b[0m"); got != "[31mred[0m" {
			t.Errorf("got %q, want %q", got, "[31mred[0m")
		}
		if got := StripNonPrintingControlChars("ping\x07"); got != "ping" {
			t.Errorf("got %q, want %q", got, "ping")
		}
	})

	t.Run("removes DEL and C1 range", func(t *testing.T) {
		if got := StripNonPrintingControlChars("a\x7fb"); got != "ab" {
			t.Errorf("got %q, want %q", got, "ab")
		}
		// TS "\x9b" is the code point U+009B (CSI, C1 range); in Go that is
		// the two-byte UTF-8 encoding of U+009B, not a raw 0x9b byte.
		if got := StripNonPrintingControlChars("a\u009bb"); got != "ab" {
			t.Errorf("got %q, want %q", got, "ab")
		}
	})

	t.Run("leaves printable text untouched", func(t *testing.T) {
		if got := StripNonPrintingControlChars("hello 漢字 🚀"); got != "hello 漢字 🚀" {
			t.Errorf("got %q, want %q", got, "hello 漢字 🚀")
		}
		if got := StripNonPrintingControlChars(""); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestStripAnsiEscapes(t *testing.T) {
	t.Run("removes CSI sequences", func(t *testing.T) {
		if got := StripAnsiEscapes("\x1b[31mred\x1b[0m"); got != "red" {
			t.Errorf("got %q, want %q", got, "red")
		}
		if got := StripAnsiEscapes("a\x1b[1;33;44mb\x1b[mc"); got != "abc" {
			t.Errorf("got %q, want %q", got, "abc")
		}
	})

	t.Run("removes OSC sequences terminated by BEL", func(t *testing.T) {
		if got := StripAnsiEscapes("\x1b]0;title\x07rest"); got != "rest" {
			t.Errorf("got %q, want %q", got, "rest")
		}
	})

	t.Run("leaves text without escapes untouched", func(t *testing.T) {
		if got := StripAnsiEscapes("hello world"); got != "hello world" {
			t.Errorf("got %q, want %q", got, "hello world")
		}
	})
}

func TestSanitizeForDisplay(t *testing.T) {
	t.Run("strips both ANSI and lingering controls", func(t *testing.T) {
		if got := SanitizeForDisplay("\x1b[31mred\x1b[0m\x00"); got != "red" {
			t.Errorf("got %q, want %q", got, "red")
		}
	})

	t.Run("preserves CJK and emoji", func(t *testing.T) {
		if got := SanitizeForDisplay("漢字 🚀 hello"); got != "漢字 🚀 hello" {
			t.Errorf("got %q, want %q", got, "漢字 🚀 hello")
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		s := "\x1b[1mfoo\x00\rbar"
		once := SanitizeForDisplay(s)
		if twice := SanitizeForDisplay(once); twice != once {
			t.Errorf("SanitizeForDisplay not idempotent: once %q, twice %q", once, twice)
		}
	})

	t.Run("realistic pasted-log-line case", func(t *testing.T) {
		// What a user might paste into a Claude prompt from a colored CLI tool.
		pasted := "\x1b[32m✓\x1b[0m build succeeded in \x1b[1m1.2s\x1b[0m"
		want := "✓ build succeeded in 1.2s"
		if got := SanitizeForDisplay(pasted); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
