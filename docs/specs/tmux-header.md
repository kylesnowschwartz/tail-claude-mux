# Spec: tcm tmux header

**Status:** v1.1 — severity-aware glyph colour, per-agent-type identity
**Origin ticket:** TMUX-HEADER-001
**Last updated:** 2026-05 (Stage 5 of side-panel redesign)

This spec is the lasting reference for the tcm tmux status line. It defines the option contract, the agent glyph table, and the read/write protocol between the tcm server and tmux. Implementation lives in `packages/runtime/src/server/tmux-header-sync.ts` and `integrations/tmux-plugin/scripts/header.tmux`.

---

## 1. Goals and non-goals

### Goals
- Replace third-party tmux themes with a status line whose colours and iconography track the active tcm theme (`packages/runtime/src/themes.ts`).
- Surface an at-a-glance per-window glyph for tmux windows that contain a live agent process (Claude Code, Pi, Codex, …), to aid tab navigation.
- **Severity-aware glyph colour.** The per-window glyph is painted in the colour of the dominant agent's severity (working / waiting / ready / stopped / error), so the tab strip doubles as a hands-off status board. Mirrors the panel's left-gutter severity colours; see `docs/design/03-vocabulary.md` §6. *(Lifted to v1 in v1.1.)*
- Keep the integration zero-cost on the tmux status repaint hot path (no `#(...)` shell expansions for agent state).

### Non-goals (v1.1)
- Per-agent-type colour. The glyph identifies *which* agent; severity colour answers *what state*. They share the same cell.
- Per-session palette divergence. v1 writes `@tcm-thm-*` at the global scope.
- Tooltip / hover-reveal of agent metadata.
- Multi-agent rendering in a single cell (e.g. "two glyphs"). Precedence picks one identity *and* its severity.

---

## 2. Architecture

```
                     +---------------------------+
                     |  tcm server      |
                     |                           |
                     |  broadcastStateImmediate  |
                     |     |                     |
                     |     v                     |
                     |  syncTmuxHeaderOptions    |
                     |     |                     |
                     +-----+---------------------+
                           |
                           |  tmux set-option -w -t <wid> @tcm-agent <glyph>
                           |  tmux set-option -w -t <wid> @tcm-agent-fg <hex>
                           |  tmux set-option -w -t <wid> @tcm-agent-type <agent>
                           |  tmux set-option -g           @tcm-thm-<token> <hex>
                           v
                     +---------------------------+
                     |  tmux server              |
                     |                           |
                     |  window-status-format     |
                     |  reads @tcm-agent*         |
                     |  status-style reads       |
                     |  @tcm-thm-*                |
                     +---------------------------+
```

The server is the single writer; tmux is a passive reader. Status-line repaint never shells out for agent state.

---

## 3. Option contract

### 3.1 Per-window options (writer: server, scope: `set-option -w`)

| Option | Type | Meaning |
|---|---|---|
| `@tcm-agent` | string (single-cell glyph) | Glyph for the dominant agent type in this window. Unset when no live agent is present. |
| `@tcm-agent-fg` | hex string (`#rrggbb`) or `default` | Foreground colour for the glyph. Resolved per-window from the dominant agent's severity: `working`→`palette.blue`, `waiting`→`palette.yellow`, `ready`→`palette.green`, `stopped`→`palette.surface2`, `error`→`palette.red`. The mapping is locked in `severityColour()` in `tmux-header-sync.ts` and mirrors the panel's left-gutter resolver. |
| `@tcm-agent-type` | string | Agent name (`claude-code`, `pi`, `codex`, `amp`, …) of the dominant agent. For introspection / future variants. |

Lifetime:
- Set when at least one agent in the window has `liveness === "alive"`.
- Unset (`set-option -wu`) the broadcast cycle after the last alive agent exits, OR when the window itself is destroyed (tmux handles the latter).

### 3.2 Per-server (global) options (writer: server, scope: `set-option -g`)

| Option | Maps to `Theme.palette.*` |
|---|---|
| `@tcm-thm-base` | `base` |
| `@tcm-thm-text` | `text` |
| `@tcm-thm-blue` | `blue` |
| `@tcm-thm-surface0` | `surface0` |
| `@tcm-thm-surface2` | `surface2` |
| `@tcm-thm-overlay0` | `overlay0` |
| `@tcm-thm-yellow` | `yellow` |
| `@tcm-thm-red` | `red` |
| `@tcm-thm-green` | `green` |

Lifetime: re-written when the server detects a theme change. Otherwise stable.

**Transparency translation.** Some builtin themes (e.g. `transparent`) store the literal string `"transparent"` for `palette.base`/`mantle`/`crust` to indicate "use the terminal's own background." tmux understands the keyword `default`, not `transparent`. Before writing palette tokens, `tmux-header-sync.ts` runs each value through a `toTmuxColour()` helper that maps `"transparent" → "default"`; all other values pass through unchanged. The format-string fallback chain (`#{?@tcm-thm-base,#{@tcm-thm-base},default}`) at §6 still works because `default` is set, not the empty string.

### 3.3 User options (writer: user, scope: `set -g` in tmux conf)

| Option | Default | Meaning |
|---|---|---|
| `@tcm-header` | unset (= `off`) | Set to `on` to opt in. The server reads this once at startup and gates `syncTmuxHeaderOptions`. The tmux conf also reads it to decide whether to source `header.tmux`. |

---

## 4. AGENT_GLYPHS table

| Agent name | Glyph | Codepoint | Notes |
|---|---|---|---|
| `claude-code` |  / `★` | U+100CC0 *(Clawd)* / U+2605 *(fallback)* | Detect-and-fall-back: emits Clawd when the font is installed, else BLACK STAR. See §4.1. |
| `pi` | `π` | U+03C0 GREEK SMALL LETTER PI | |
| `codex` | `▲` | U+25B2 BLACK UP-POINTING TRIANGLE | |
| `amp` | `♦` | U+2666 BLACK DIAMOND SUIT | |
| `generic` |  | U+F167A nf-md-robot-outline | Fallback when no specific entry exists. Aligns with the panel's right-gutter `ID_GENERIC`. |

### 4.1. Clawd auto-detect

`isClawdInstalled()` in `tmux-header-sync.ts` does one `existsSync` against the OS-standard user-fonts path at module load:

| Platform | Path |
|---|---|
| macOS | `~/Library/Fonts/Clawd.ttf` |
| Linux | `~/.local/share/fonts/Clawd.ttf` |

`buildAgentGlyphs({ clawdInstalled })` produces the table — Clawd codepoint when true, `★` when false. The probe is one-shot at module load; restart the server after running the installer. The font itself is vendored at `fonts/Clawd.ttf`; `just install-clawd` (or `scripts/install-clawd-font.sh` directly) is the idempotent installer.

Mascot likeness is a trademark of Anthropic; personal-use vendoring is fine, public redistribution outside this fork needs Anthropic's nod.

**Constraints on glyph values:**
- Single column-cell wide. Multi-cell glyphs (most emoji) drift status-line column accounting.
- Available in MesloLGS Nerd Font (or whatever the user's terminal renders). The conf documents the assumption.
- ASCII-safe is preferred; widely-supported Unicode (U+25xx geometric shapes, U+26xx miscellaneous symbols, basic Greek) is next; Nerd Font private-use codepoints last (they bind us to a font).
- **Avoid less-common codepoints** like U+2726 (BLACK FOUR POINTED STAR) — they render as tofu boxes in monospace stacks that don't carry them, including some Nerd Font variants.

**Swap path.** Edit `AGENT_GLYPHS` in `tmux-header-sync.ts` and restart the server. No tmux conf change required.

### Precedence (multi-agent windows)

```
const AGENT_PRIORITY = ["claude-code", "pi", "codex", "amp"];
```

`pickAgentForWindow(agents)` returns the first match from `AGENT_PRIORITY` present in `agents`, falling back to `agents[0]` and finally to `"generic"`. Mirrors the existing `AGENT_TITLE_PATTERNS` ordering in `server/index.ts`.

---

## 5. Function signatures

### `tmux-header-sync.ts`

```ts
import type { SessionData } from "../contracts";
import type { Theme } from "../themes";

export const AGENT_GLYPHS: Record<string, string>;
export const AGENT_PRIORITY: readonly string[];

export function pickAgentForWindow(agents: string[]): string;

export function syncTmuxHeaderOptions(args: {
  sessions: SessionData[];
  theme: Theme;
  enabled: boolean;
}): void;
```

### Behaviour contract

- **Idempotent.** Identical inputs after the first call produce zero shell invocations.
- **Non-throwing.** Internal errors are caught and logged via `log("tmux-header", ...)`; the function never throws.
- **Bounded shell cost.** At most: 1 `list-panes` call per invocation, plus 1 chained `tmux` invocation containing all `set-option`/`set-option -wu` writes.
- **No global side effects when disabled.** `enabled === false` short-circuits before any tmux call.

---

## 6. Read protocol — `header.tmux`

Sourced from `tcm.tmux` when `@tcm-header == on`. Sets:

| tmux variable | Format |
|---|---|
| `status-position` | `top` |
| `status-justify` | `left` |
| `status-style` | `fg=default,bg=#{?@tcm-thm-base,#{@tcm-thm-base},default}` |
| `window-status-style` | `fg=default` |
| `window-status-current-style` | `fg=#{?@tcm-thm-blue,#{@tcm-thm-blue},default},bg=#{?@tcm-thm-surface0,#{@tcm-thm-surface0},default},bold` |
| `window-status-activity-style` | `default` (cleared) — see legacy-reset note |
| `window-status-bell-style` | `default` (cleared) — see legacy-reset note |
| `window-status-last-style` | `default` (cleared) — see legacy-reset note |
| `window-status-format` | `<space>{glyph-slot}<space>#I<space>#W{zoom?}{last-flag?}<space>` — see vocabulary below |
| `window-status-current-format` | structurally identical to `window-status-format`. Differs only in the post-glyph reset: inactive falls back to `#[default]` (segment style = no bg); active falls back to the pill style (`bg=surface0,fg=blue,bold`). |
| `window-status-separator` | single space — paired with the trailing pad above gives 2 cells between tabs |
| `status-left` | session-name pill in `theme.blue`. Ends with `#[default]` and **no trailing space** — windows carry their own leading pad, so adding one here would render with `bg=default` and produce a stray cell when the terminal default differs from the bar's bg. |
| `status-right` | preserved oh-my-tmux semantic content (prefix, pairing, sync) |

**Legacy-reset.** Three tmux-default / oh-my-tmux indicators paint over the tab strip when set: `window-status-activity-style` adds an underscore for windows with the activity flag, `window-status-bell-style` adds blink+bold for windows that triggered a bell, and `window-status-last-style` paints the previously-visited tab cyan. tcm's vocabulary already surfaces the same signals (activity zone in the panel; severity-coloured glyph in the tab strip; yellow last-window arrow in §6.2), so `header.tmux` explicitly resets each to `"default"` to prevent double-rendering. Without the reset the activity underscore reads as a "janky interrupted underline" because it's clipped by the active-tab pill bg.

**Inactive-tab readability.** Inactive windows render with `fg=default` rather than a fixed `@tcm-thm-overlay0` colour. The tcm theme palette is calibrated for dark terminal backgrounds; users running light terminal palettes (`the-themer` switches both) would see overlay grays as illegible. Letting the terminal palette dictate inactive-tab fg, and using `bold + theme.blue` for the active tab, keeps differentiation regardless of light/dark.

**Active-window pill background.** Active tabs render on `theme.surface0` (a subtle bg slightly lighter than `theme.base`); inactive tabs use `bg=default` (terminal background). This adds bg-based differentiation on top of the existing fg+bold, so the active tab is identifiable even when its severity colour also resolves to `theme.blue`. The pill is a single-segment background — no rounded edges, no dividers — to stay zero-cost on the status repaint hot path.

**Active-window vs. severity-colour collision.** When the active window's agent is `working`, both the pill style and `@tcm-agent-fg` resolve to `theme.blue`, so fg alone can't differentiate "this is the active tab" from "this glyph means working." Resolution: the active style carries `bold` and a `bg=surface0` pill; the inline `#[fg=@tcm-agent-fg]` overrides the *fg* only, so an active working glyph renders bold-blue *on the pill* while inactive working glyphs render plain blue on `bg=default`. When severity is anything other than `working`, fg colour *and* weight *and* bg differentiate. No special-case in the format string — tmux's existing attribute inheritance handles it. See `docs/design/03-vocabulary.md` §6 "Active-window vs. severity colour collision".

### 6.1 Glyph slot vocabulary

Every tab opens with a single "what's running here" glyph slot. Two cases:

| Condition | Glyph | Codepoint | Colour |
|---|---|---|---|
| `@tcm-agent` is set (live agent) | `#{@tcm-agent}` | varies (see §4 AGENT_GLYPHS) | `#{@tcm-agent-fg}` (severity-aware) |
| `@tcm-agent` is unset (shell only) | nf-cod-terminal | U+EA85 | `theme.overlay0` |

The shell-only case mirrors tokyo-night-tmux's leading boxed-terminal glyph. It anchors every tab visually (so empty windows still have a stable left edge) and signals "nothing demanding attention here" via the muted `overlay0` colour. When an agent appears, its severity-coloured identity glyph swaps in; when it exits, the shell glyph returns. Single slot, single meaning.

### 6.2 Last-window-flag indicator

The most-recently-visited window (the target of tmux's `prefix l` / `last-window` binding) carries a yellow trailing glyph: nf-md U+F054C (undo, curl-back arrow) in `theme.yellow`. Other tabs render only the standard trailing space. The marked tab is therefore 2 cells wider than its peers — acceptable because exactly one window holds the flag at a time.

The `#{?@tcm-thm-base,#{@tcm-thm-base},default}` chain ensures the status line is readable on first paint before the server has written palette options.

---

## 7. Failure modes and recovery

See `artifacts/03-blueprint.md` Section 3 for the authoritative table. Key entries:

- Server not running inside tmux → sync no-ops, header stays unstyled.
- `@tcm-header` off → no writes; tmux falls back to whatever else is loaded.
- Glyph wider than one cell → column drift. Constrained at the spec level.
- Window closed mid-broadcast → write fails harmlessly (`shellStatus()` throws on nonzero exit, the sync's outer try/catch swallows it without advancing the cache, so the next successful broadcast retries).
- Empty `tmux list-panes` (transient flake or genuinely empty) → sync returns early **without clearing caches**. Cached `lastWindows`/`lastPalette` are preserved so a subsequent successful scan can compute correct cleanup diffs and the bar does not lose its palette colours after a tmux server restart that wiped `@tcm-thm-*` options.

### 7.1 Disable behaviour (sticky-off)

Setting `@tcm-header` from `on` back to `off` does **not** retroactively tear down options written during the previous active period:

- The server reads the gate **once at startup**, so a runtime flip is invisible until restart.
- After restart with the gate off, the server short-circuits before any tmux probe — it does not issue cleanup writes for `@tcm-agent*` per-window options or revert the status-line settings that `header.tmux` applied.

To fully revert: comment out or remove the `set -g @tcm-header on` line, restart the tmux server, and (optionally) source the user's prior status-line config. v1 does not implement an automated tear-down path; treat the gate as restart-required and forget-on-restart.

---

## 8. Test surface

Module-spec tests live in `packages/runtime/test/tmux-header-sync.test.ts` (new). Coverage targets:
- Glyph lookup and precedence (S1–S3, E2 from blueprint test plan)
- Idempotence (S4)
- Diff-driven cleanup (E1, E5)
- Disabled gate (S6)
- Theme change propagates palette (S5)

Live verification on a real tmux server is recorded in the blueprint as L1–L3 and runs as part of the Stage 5 testing handoff.

---

## 9. Future work (not in v1.1)

- Per-status glyph swap: extend `@tcm-agent` lookup to also vary the *glyph* by status (e.g. severity-glyph overlay) when `@tcm-header-status-aware == on`. Today only the colour varies; the glyph is identity-only.
- Per-agent-type colour: `theme.status[status]` or a separate `@tcm-agent-type-fg` palette.
- Tooltip via `@tcm-agent-tooltip` and tmux `#{T:...}` formats.
- Per-session palette overrides (different theme per tmux session).
- Custom Nerd Font glyphs delivered via the user's SVG-derived icon font — only the `AGENT_GLYPHS` table changes.
