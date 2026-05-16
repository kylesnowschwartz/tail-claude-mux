import { describe, expect, test } from "bun:test";

import {
  sanitizeForDisplay,
  stringWidth,
  stripAnsiEscapes,
  stripNonPrintingControlChars,
  truncateToWidth,
} from "../src/text";

describe("stringWidth", () => {
  test("ASCII is one cell per char", () => {
    expect(stringWidth("")).toBe(0);
    expect(stringWidth("hello")).toBe(5);
    expect(stringWidth("a b c")).toBe(5);
  });

  test("CJK is two cells per char", () => {
    expect(stringWidth("漢字")).toBe(4);
    expect(stringWidth("a漢b")).toBe(4);
    expect(stringWidth("한국어")).toBe(6);
    expect(stringWidth("こんにちは")).toBe(10);
  });

  test("emoji is two cells", () => {
    expect(stringWidth("🚀")).toBe(2);
    expect(stringWidth("a🚀b")).toBe(4);
  });

  test("combining marks are zero-width", () => {
    // "e" + combining acute (U+0301)
    expect(stringWidth("é")).toBe(1);
  });

  test("zero-width joiner does not add cells", () => {
    expect(stringWidth("a​b")).toBe(2);
  });
});

describe("truncateToWidth", () => {
  test("returns input unchanged when within budget", () => {
    expect(truncateToWidth("hello", 10)).toBe("hello");
    expect(truncateToWidth("hello", 5)).toBe("hello");
  });

  test("appends ellipsis when truncating ASCII", () => {
    expect(truncateToWidth("hello world", 8)).toBe("hello w…");
    // 8-cell budget: 7 chars + "…" = 8 cells.
    expect(stringWidth(truncateToWidth("hello world", 8))).toBe(8);
  });

  test("respects CJK width when truncating", () => {
    // "漢字漢字" is 8 cells. With max=5 we must fit "X…" where X is one CJK (2)
    // plus the ellipsis (1) = 3 cells. Two CJK + ellipsis = 5 cells.
    const out = truncateToWidth("漢字漢字", 5);
    expect(stringWidth(out)).toBeLessThanOrEqual(5);
    expect(out.endsWith("…")).toBe(true);
  });

  test("returns just ellipsis when maxWidth is 1", () => {
    expect(truncateToWidth("anything", 1)).toBe("…");
  });

  test("returns empty when maxWidth is 0 or negative", () => {
    expect(truncateToWidth("anything", 0)).toBe("");
    expect(truncateToWidth("anything", -3)).toBe("");
  });

  test("does not split a wide char mid-codepoint", () => {
    // "漢漢" = 4 cells. Budget 3: ellipsis takes 1, leaving 2 — exactly one 漢.
    const out = truncateToWidth("漢漢", 3);
    expect(out).toBe("漢…");
    expect(stringWidth(out)).toBe(3);
  });
});

describe("stripNonPrintingControlChars", () => {
  test("removes C0 controls", () => {
    expect(stripNonPrintingControlChars("a\x00b\x01c")).toBe("abc");
    expect(stripNonPrintingControlChars("line\rreturn")).toBe("linereturn");
    expect(stripNonPrintingControlChars("tab\there")).toBe("tabhere");
  });

  test("removes ESC and BEL", () => {
    expect(stripNonPrintingControlChars("\x1b[31mred\x1b[0m")).toBe("[31mred[0m");
    expect(stripNonPrintingControlChars("ping\x07")).toBe("ping");
  });

  test("removes DEL and C1 range", () => {
    expect(stripNonPrintingControlChars("a\x7fb")).toBe("ab");
    expect(stripNonPrintingControlChars("a\x9bb")).toBe("ab");
  });

  test("leaves printable text untouched", () => {
    expect(stripNonPrintingControlChars("hello 漢字 🚀")).toBe("hello 漢字 🚀");
    expect(stripNonPrintingControlChars("")).toBe("");
  });
});

describe("stripAnsiEscapes", () => {
  test("removes CSI sequences", () => {
    expect(stripAnsiEscapes("\x1b[31mred\x1b[0m")).toBe("red");
    expect(stripAnsiEscapes("a\x1b[1;33;44mb\x1b[mc")).toBe("abc");
  });

  test("removes OSC sequences terminated by BEL", () => {
    expect(stripAnsiEscapes("\x1b]0;title\x07rest")).toBe("rest");
  });

  test("leaves text without escapes untouched", () => {
    expect(stripAnsiEscapes("hello world")).toBe("hello world");
  });
});

describe("sanitizeForDisplay", () => {
  test("strips both ANSI and lingering controls", () => {
    expect(sanitizeForDisplay("\x1b[31mred\x1b[0m\x00")).toBe("red");
  });

  test("preserves CJK and emoji", () => {
    expect(sanitizeForDisplay("漢字 🚀 hello")).toBe("漢字 🚀 hello");
  });

  test("is idempotent", () => {
    const s = "\x1b[1mfoo\x00\rbar";
    expect(sanitizeForDisplay(sanitizeForDisplay(s))).toBe(sanitizeForDisplay(s));
  });

  test("realistic pasted-log-line case", () => {
    // What a user might paste into a Claude prompt from a colored CLI tool.
    const pasted = "\x1b[32m✓\x1b[0m build succeeded in \x1b[1m1.2s\x1b[0m";
    expect(sanitizeForDisplay(pasted)).toBe("✓ build succeeded in 1.2s");
  });
});
