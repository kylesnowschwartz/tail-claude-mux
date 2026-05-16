/**
 * Calm attention-state glow: lerp two hex colors on a slow sine wave.
 *
 * Used by the TUI to "breathe" the row foreground on `status === "waiting"`
 * agents — the only place we tolerate row chrome animating, because waiting
 * is a transient attention state that self-clears.
 *
 * Two ingredients:
 *   - lerpHex(a, b, t): linear sRGB blend between two `#rrggbb` strings
 *   - glowPhase(nowMs):  returns t in [0,1] following `(sin(ms/1000 · π)+1)/2`
 *                        — full period 2s, mid-value at t=0 so animation
 *                        starts gently rather than flashing on at full
 *                        intensity.
 *
 * sRGB-space (not perceptual) blending is intentional: the endpoints are both
 * bright pastels (catppuccin text ↔ yellow ≈ #cdd6f4 ↔ #f9e2af), and a perceptual
 * blend would only matter at high-contrast pairs we don't use here.
 */

const HEX_PREFIX = "#";
const HEX_LEN = 7;
const MAX_PHASE_PERIOD_MS = 60_000;
const DEFAULT_PERIOD_MS = 2000;

function parseHex(c: string): readonly [number, number, number] | null {
  if (typeof c !== "string") return null;
  if (c.length !== HEX_LEN) return null;
  if (c[0] !== HEX_PREFIX) return null;
  const r = parseInt(c.slice(1, 3), 16);
  const g = parseInt(c.slice(3, 5), 16);
  const b = parseInt(c.slice(5, 7), 16);
  if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) return null;
  return [r, g, b];
}

function clamp01(t: number): number {
  if (!Number.isFinite(t)) return 0;
  if (t < 0) return 0;
  if (t > 1) return 1;
  return t;
}

function toHexByte(n: number): string {
  const clamped = Math.max(0, Math.min(255, Math.round(n)));
  return clamped.toString(16).padStart(2, "0");
}

/**
 * Blend two `#rrggbb` strings.
 *
 * - t=0 returns `a`, t=1 returns `b`, t=0.5 is the midpoint.
 * - t outside [0,1] is clamped (non-finite → 0).
 * - Either input being malformed → returns `a` if `a` looks valid,
 *   else `b` — never throws. Callers paint colors every frame; one bad
 *   theme value should never break the render.
 */
export function lerpHex(a: string, b: string, t: number): string {
  const A = parseHex(a);
  const B = parseHex(b);
  if (!A && !B) return a;
  if (!A) return b;
  if (!B) return a;
  const k = clamp01(t);
  const r = A[0] + (B[0] - A[0]) * k;
  const g = A[1] + (B[1] - A[1]) * k;
  const bl = A[2] + (B[2] - A[2]) * k;
  return `#${toHexByte(r)}${toHexByte(g)}${toHexByte(bl)}`;
}

/**
 * Returns the glow lerp coefficient in [0,1] for a given wall-clock ms.
 *
 *   t_seconds = nowMs / 1000
 *   coeff     = (sin(t_seconds · π) + 1) / 2     // for default 2s period
 *
 * With the default 2000ms period:
 *   nowMs = 0    → 0.5  (midpoint — starts calm)
 *   nowMs = 500  → 1.0  (toward `b`)
 *   nowMs = 1000 → 0.5
 *   nowMs = 1500 → 0.0  (toward `a`)
 *   nowMs = 2000 → 0.5
 *
 * Non-finite or non-positive periods fall back to the default. Periods over
 * a minute are also clamped — we're animating an attention state, not
 * driving an analog clock.
 */
export function glowPhase(nowMs: number, periodMs: number = DEFAULT_PERIOD_MS): number {
  const p = Number.isFinite(periodMs) && periodMs > 0 && periodMs <= MAX_PHASE_PERIOD_MS
    ? periodMs
    : DEFAULT_PERIOD_MS;
  const ms = Number.isFinite(nowMs) ? nowMs : 0;
  const phase = (ms / p) * 2 * Math.PI;
  return (Math.sin(phase) + 1) / 2;
}
