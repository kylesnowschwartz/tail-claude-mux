import { describe, test, expect } from "bun:test";
import { classifyTitleStatus } from "../src/agents/title-status";

// Mirrors herdr's claude.toml osc_title rules: leading braille → working,
// leading sparkle (✳ U+2733) → idle/ended.
describe("classifyTitleStatus", () => {
  test("leading braille spinner glyph → working", () => {
    expect(classifyTitleStatus("⠀ doing work")).toBe("working");
    expect(classifyTitleStatus("⠋ Reading config.ts")).toBe("working");
    expect(classifyTitleStatus("⣿ tail of the braille range")).toBe("working");
  });

  test("leading sparkle ✳ → ended", () => {
    expect(classifyTitleStatus("✳ ~/Code/project")).toBe("ended");
    expect(classifyTitleStatus("✳ idle at prompt")).toBe("ended");
  });

  test("plain title → null (no signal)", () => {
    expect(classifyTitleStatus("~/Code/meta-claude")).toBeNull();
    expect(classifyTitleStatus("zsh")).toBeNull();
    expect(classifyTitleStatus("claude — main")).toBeNull();
  });

  test("empty title → null", () => {
    expect(classifyTitleStatus("")).toBeNull();
  });

  test("glyph not at the leading position → null", () => {
    // A braille char mid-string is not Claude's state marker.
    expect(classifyTitleStatus("project ⠋")).toBeNull();
  });

  test("near-range glyphs that are not braille/sparkle → null", () => {
    // U+27FF is just below the braille block; U+2734 is just past the sparkle.
    expect(classifyTitleStatus("⟿ x")).toBeNull();
    expect(classifyTitleStatus("✴ x")).toBeNull();
  });
});
