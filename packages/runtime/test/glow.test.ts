import { describe, expect, test } from "bun:test";

import { glowPhase, lerpHex } from "../src/glow";

describe("lerpHex — endpoints", () => {
  test("t=0 returns the first color", () => {
    expect(lerpHex("#000000", "#ffffff", 0)).toBe("#000000");
  });

  test("t=1 returns the second color", () => {
    expect(lerpHex("#000000", "#ffffff", 1)).toBe("#ffffff");
  });

  test("t=0.5 returns the midpoint", () => {
    // (0 + 255) / 2 = 127.5 → rounds to 128 = 0x80
    expect(lerpHex("#000000", "#ffffff", 0.5)).toBe("#808080");
  });

  test("lerp(a, a, 0.5) returns a exactly — no rounding drift on equal endpoints", () => {
    expect(lerpHex("#cdd6f4", "#cdd6f4", 0.5)).toBe("#cdd6f4");
    expect(lerpHex("#f9e2af", "#f9e2af", 0.7)).toBe("#f9e2af");
  });
});

describe("lerpHex — clamping", () => {
  test("clamps t < 0 to 0", () => {
    expect(lerpHex("#000000", "#ffffff", -5)).toBe("#000000");
  });

  test("clamps t > 1 to 1", () => {
    expect(lerpHex("#000000", "#ffffff", 99)).toBe("#ffffff");
  });

  test("non-finite t falls back to 0", () => {
    expect(lerpHex("#000000", "#ffffff", Number.NaN)).toBe("#000000");
    expect(lerpHex("#000000", "#ffffff", Number.POSITIVE_INFINITY)).toBe("#000000");
  });

  test("malformed-input check fires before t-clamping (NaN with one bad endpoint)", () => {
    // Contract: if only one endpoint parses, return the valid one — independent
    // of t. (Validity check runs before clamp01.)
    expect(lerpHex("garbage", "#000000", Number.NaN)).toBe("#000000");
    expect(lerpHex("#ffffff", "broken", Number.POSITIVE_INFINITY)).toBe("#ffffff");
  });
});

describe("lerpHex — per-channel blending", () => {
  test("blends each channel independently", () => {
    // r: 0+100*0.5=50 (0x32), g: 50+50*0.5=50 (0x32), b: 100+0*0.5=100 (0x64)
    expect(lerpHex("#003264", "#643264", 0.5)).toBe("#323264");
  });

  test("works with catppuccin text → yellow (the actual use case)", () => {
    const text = "#cdd6f4";
    const yellow = "#f9e2af";
    // At t=0.5, each channel midway
    const mid = lerpHex(text, yellow, 0.5);
    // r: (0xcd + 0xf9) / 2 = (205 + 249) / 2 = 227 = 0xe3
    // g: (0xd6 + 0xe2) / 2 = (214 + 226) / 2 = 220 = 0xdc
    // b: (0xf4 + 0xaf) / 2 = (244 + 175) / 2 = 209.5 → 210 = 0xd2
    expect(mid).toBe("#e3dcd2");
  });
});

describe("lerpHex — malformed input never throws", () => {
  test("malformed first color → returns second", () => {
    expect(lerpHex("not-a-color", "#ffffff", 0.5)).toBe("#ffffff");
  });

  test("malformed second color → returns first", () => {
    expect(lerpHex("#000000", "garbage", 0.5)).toBe("#000000");
  });

  test("both malformed → returns first verbatim, does not throw", () => {
    expect(lerpHex("x", "y", 0.5)).toBe("x");
  });

  test("short-form hex (#fff) is not accepted — only #rrggbb", () => {
    // Caller paints in #rrggbb everywhere; supporting #rgb would mask bugs.
    expect(lerpHex("#fff", "#000000", 0.5)).toBe("#000000");
  });

  test("uppercase hex is parsed (output normalizes to lowercase)", () => {
    // Mid-blend forces the parser to interpret both endpoints, then format.
    // (At t=0/t=1 the function short-circuits without re-formatting, so use
    // a midpoint to actually exercise the parse → blend → format path.)
    expect(lerpHex("#FFFFFF", "#FFFFFF", 0.5)).toBe("#ffffff");
    expect(lerpHex("#FFFFFF", "#000000", 0.5)).toBe("#808080");
  });
});

describe("glowPhase — sine wave over default 2s period", () => {
  test("nowMs=0 → 0.5 (midpoint, starts calm)", () => {
    expect(glowPhase(0)).toBeCloseTo(0.5, 10);
  });

  test("nowMs=500 → 1.0 (quarter period, peak toward b)", () => {
    expect(glowPhase(500)).toBeCloseTo(1.0, 10);
  });

  test("nowMs=1000 → 0.5 (half period, back at midpoint)", () => {
    expect(glowPhase(1000)).toBeCloseTo(0.5, 10);
  });

  test("nowMs=1500 → 0.0 (three-quarter period, peak toward a)", () => {
    expect(glowPhase(1500)).toBeCloseTo(0.0, 10);
  });

  test("nowMs=2000 → 0.5 (full period, back at start)", () => {
    expect(glowPhase(2000)).toBeCloseTo(0.5, 10);
  });

  test("phase is always in [0,1]", () => {
    for (let t = 0; t < 10_000; t += 17) {
      const p = glowPhase(t);
      expect(p).toBeGreaterThanOrEqual(0);
      expect(p).toBeLessThanOrEqual(1);
    }
  });

  test("pure — same input returns same output", () => {
    expect(glowPhase(500)).toBe(glowPhase(500));
    expect(glowPhase(1234, 3000)).toBe(glowPhase(1234, 3000));
  });
});

describe("glowPhase — period override and bad input", () => {
  test("custom period scales correctly", () => {
    // 4s period → quarter-period peak at 1000ms
    expect(glowPhase(1000, 4000)).toBeCloseTo(1.0, 10);
  });

  test("non-finite period falls back to default", () => {
    expect(glowPhase(0, Number.NaN)).toBeCloseTo(0.5, 10);
    expect(glowPhase(500, Number.POSITIVE_INFINITY)).toBeCloseTo(1.0, 10);
  });

  test("zero or negative period falls back to default", () => {
    expect(glowPhase(500, 0)).toBeCloseTo(1.0, 10);
    expect(glowPhase(500, -100)).toBeCloseTo(1.0, 10);
  });

  test("absurdly long period falls back to default (sanity guard)", () => {
    expect(glowPhase(500, 999_999_999)).toBeCloseTo(1.0, 10);
  });

  test("non-finite nowMs is treated as 0", () => {
    expect(glowPhase(Number.NaN)).toBeCloseTo(0.5, 10);
    expect(glowPhase(Number.POSITIVE_INFINITY)).toBeCloseTo(0.5, 10);
  });
});
