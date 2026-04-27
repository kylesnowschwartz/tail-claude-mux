# 03 · Vocabulary — glyphs, colours, columns

This doc fixes the actual glyph alphabet, colour rules, text-tier rules,
and column rules that fill the zones from `02-zones.md`.

The audit's open questions on glyph dedup, status gutter, activity
content, and header counters are all resolved upstream. This doc
translates those decisions into a concrete table an implementer uses.

The vocabulary is shared between the side panel and the tmux statusline
where applicable (see §6). Nerd Fonts are a hard requirement.

---

## 1. Principles (recap, then operationalised)

From `00-grounding.md` §3 and §7:

- **One family is the default; exceptions earn their place.** Material
  Design Icons (`nf-md-*`) are the anchor. Four exceptions: Powerline
  branch glyph, brand letterforms (`π / ▲ / ♦`), vendored glyphs (Clawd
  `\u{100CC0}`), animated brail spinners.
- **One cell, one meaning, one column.** No glyph reused across roles.
- **Shape encodes taxonomy; colour encodes severity.** Don't double-load.
- **Brand glyphs sit in three positions across surfaces.** Same glyph,
  different positions, different meanings; the user learns it once.

---

## 2. Severity glyphs (left gutter)

```
[sev]  content                 [ident]
 ^^^
 1 cell at column 0 of every row that has a status to report.
```

| State    | Glyph | Codepoint | Family | Colour token | Notes |
|----------|-------|-----------|--------|--------------|-------|
| working  | (animated brail spinner) | existing `SPINNERS[]` | exception (animation) | `blue`     | Animation already implemented \u2014 keep `index.tsx:45` |
| waiting  |  bell-alert | `\u{F0179}` | nf-md | `yellow`   | "Asks for your attention" \u2014 same metaphor `tail-claude-hud` uses for permission prompts |
| ready    |  check-circle-outline | `\u{F05E1}` | nf-md | `green`    | "Done & idle" \u2014 includes the `done` substate in the five-label scheme |
| stopped  |  circle-small | `\u{F1428}` | nf-md | `surface2` | Quiet absence; reads as "this slot is reserved but inactive" |
| error    |  alert-circle | `\u{F0028}` | nf-md | `red`      | The MD family's canonical urgent-warning glyph |

### Determinate-progress variant for `working`

When `metadata.progress.percent` is set, the spinner is replaced by a
**circle-slice** glyph indicating fill level. Same column, same colour
(`blue`), but the user gets a real readout instead of indeterminate
motion. Lifted directly from `tail-claude-hud`.

| Range  | Glyph | Codepoint |
|--------|-------|-----------|
| 0–18%   |  | `\u{F0A9E}` |
| 19–31%  |  | `\u{F0A9F}` |
| 32–43%  |  | `\u{F0AA0}` |
| 44–56%  |  | `\u{F0AA1}` |
| 57–68%  |  | `\u{F0AA2}` |
| 69–81%  |  | `\u{F0AA3}` |
| 82–93%  |  | `\u{F0AA4}` |
| 94–100% |  | `\u{F0AA5}` |

### Severity row eligibility

| Row type             | Severity gutter? |
|----------------------|------------------|
| Session row (collapsed) | **Conditional** — shows worst agent state when that state is `working`, `waiting`, or `error`. **Blank** when all agents are `ready`/`stopped` or there are no agents. |
| Session row (focused)   | Same rule as collapsed |
| Agent row               | Always — every agent's state shows |
| Branch row              | No — gutter is blank |
| Activity zone entry     | No — activity has its own leader; severity carried by entry colour |
| Header / footer         | No |

**B5 resolution.** Collapsed-session severity is *information-bearing*
only when the state demands user attention. Nominal sessions (all
agents ready/stopped) keep the left gutter empty so the rolodex reads
calm — the eye is drawn only to the gutters that actually need looking
at. This is what makes the absence of the running-counter (`⚡1`) in
the header acceptable: severity gutters in the rolodex carry the same
information for sessions that need it, and don't shout when nothing
needs shouting.

---

## 3. Identity glyphs (right gutter)

```
[sev]  content                 [ident]
                                ^^^^^
                                1 cell at the rightmost column.
```

| Agent type    | Glyph | Codepoint | Family | Notes |
|---------------|-------|-----------|--------|-------|
| `claude-code` |  Clawd  | `\u{100CC0}` | vendored | Per spec §4.1 — falls back to `★` (`\u2605`) only if the Clawd font isn't installed; with Nerd Fonts as a hard requirement we can promote Clawd to the default and treat the fallback as a degraded mode, not a supported one |
| `pi`          | π     | `\u03C0`     | letterform | Iconic, kept |
| `codex`       | ▲     | `\u25B2`     | letterform | OpenAI mark, kept |
| `amp`         | ♦     | `\u2666`     | letterform | Amp's diamond suit, kept |
| `generic`     |  robot-outline | `\u{F167A}` | nf-md | **NEW** — replaces retired `●`. Same glyph `tail-claude-hud` uses for sub-agents in its `Task` category |

### Identity row eligibility

| Row type             | Identity gutter? |
|----------------------|------------------|
| Session row (collapsed) | Sometimes — numeric agent count if 2+, agent-type glyph if exactly 1, blank if 0 |
| Session row (focused)   | Numeric agent count if 2+, glyph if 1, blank if 0 |
| Agent row               | Yes — that agent's type |
| Branch row              | No |
| Activity zone entry     | No |
| Header                  | No (header has its own product mark, see §6) |
| Footer                  | No |

### Numeric agent count formatting

When a session has 2+ agents, the right gutter widens to **2 cells** and
shows the digit:

```
[s]  main                    [ ][3]
                              ↑  ↑ digit (Tier 3 Dim)
                              gutter widens 1 cell
```

`9+` is the cap. >9-agent sessions are pathological.

**Locked count format (B1 / Q3 resolution):** the gutter shows the bare
digit only — not `2π` or `3䍳`. The original same-type-glyph compaction
(`2π`) was reverted because the width-sensitive branching it introduced
didn't justify the marginal density gain. Type information is already
available on the per-agent rows inside the focused card and on the tmux
statusline.

### Cross-surface: identity glyphs are SHARED with the tmux statusline

The same table populates `AGENT_GLYPHS` in
`packages/runtime/src/server/tmux-header-sync.ts`. Today's table already
matches except for `generic` (currently `●`, becomes  `\u{F167A}`).
Spec §4 already documents the table as the single source of truth — the
v1.x bump just changes one row and removes the spec's existing
"ASCII-safe is preferred" rule.

---

## 4. Text hierarchy — four tiers

Replaces the current ad-hoc use of `text / subtext0 / subtext1 /
overlay0 / overlay1 / surface2` (six tiers) with a disciplined four.
Lifted from `tail-claude-hud/internal/render/widget/colors.go`.

### Tier definitions

| Tier | Style | Visual recipe | Used for |
|------|-------|---------------|----------|
| **Tier 1: Primary**   | bold + default | `text` colour, `BOLD` attribute | Dynamic, urgent: focused-session name, errored agent name, working spinner |
| **Tier 2: Secondary** | default        | `text` colour, no attributes    | Stable context: agent type names, branch text, ready/stopped agent names |
| **Tier 3: Dim**       | faint          | `text` colour, `DIM` (Faint) attribute | Supporting detail: thread IDs, elapsed times, "stopped 4m ago" |
| **Tier 4: Muted**     | colour-shifted | `overlay0` colour, no attributes | Static metadata: separators, row leaders, port labels, dates |

**Why Tier 3 ≠ Tier 4.** Tier 3 uses the *Faint attribute* applied to the
text colour — the result varies by terminal and looks "dimmed but same
colour." Tier 4 uses an explicit colour shift (`overlay0`) — the result
is a different *hue* from the text. The two are visually distinct even
though both look "dim" at a glance, and using both lets the eye separate
"important-but-quiet" (Tier 3) from "static chrome" (Tier 4).

### Pane-unfocused override

When the side panel is not the focused pane, every tier slides **one step
dimmer**:

| Pane focused | Pane unfocused |
|--------------|----------------|
| Tier 1: `text` + bold | `subtext0` + bold |
| Tier 2: `text`        | `subtext0` |
| Tier 3: `text` + faint | `subtext0` + faint |
| Tier 4: `overlay0`    | `surface2` |

This is the only place `subtext0` and `surface2` show up as text colours
\u2014 they exist purely as the unfocused mirror of the focused tiers, so the
panel reads "asleep" without becoming illegible.

### Severity colours bypass tiers

Severity glyphs (§2) and agent identity glyphs (§3) are coloured by their
own rules, *not* by tier. A red error glyph stays red regardless of pane
focus state — the EFIS principle "severity reads through dimming" holds.
Only *text* slides dimmer when the pane unfocused.

### Italic as a sanctioned modifier

**Activity-zone description text uses italic in addition to its tier**
(Tier 3 + italic). This is the *only* sanctioned use of italic in the
design. It serves to mark the description column as narrative (not
structural) and to distinguish it from agent-name text in the same
tier. Anywhere else, italic is a violation.

**Fresh-vs-history distinction inside the activity zone:**
the most-recently-arrived entry uses **Tier 2 + italic** (brighter than
history); older entries fall back to Tier 3 + italic (the default).
When a newer entry arrives, the previous freshest entry steps down to
Tier 3. This is the only attribute change inside the zone — no fade
animation, no chrome motion.

### Unseen state — colour-only marker on the name

Unseen state (a session has new activity since the user last looked at
it) replaces the retired `●` glyph with a **colour shift on the session
or agent name itself**:

| Where | Pane focused | Pane unfocused |
|-------|--------------|----------------|
| Session-row name (rolodex) | `teal` (replaces Tier 2) | `teal` + `DIM` (mirror) |
| Agent-row name (focused card) | `teal` (replaces Tier 2) | `teal` + `DIM` |
| Current session + unseen | `teal` + bold (replaces Tier 1) | `teal` + bold + `DIM` |

**Precedence rules:**

1. Severity colour applies to the **left severity gutter**, never to the
   name itself. An errored *and* unseen agent has a red glyph on the
   left and a teal name in the middle. Both signals coexist.
2. Pane-unfocused dimming applies *after* unseen colouring — unseen
   teal goes one step dim when the pane isn't focused.
3. Bold (Tier 1, current-session-or-focused-row) applies *on top of*
   unseen teal — the cumulative attribute set is `teal + bold`.
4. Once the user views the session/agent (focused for ≥ 1s), unseen
   clears and the name returns to its tier rule.

---

## 5. Structural & branding glyphs

| Use | Glyph | Codepoint | Family | Colour |
|---|---|---|---|---|
| Header product mark |  Clawd | `\u{100CC0}` | vendored | Tier 1 (bold) when pane focused; Tier 2 unfocused |
| Branch row leader |  source-branch | `\u{F062C}` | nf-md | Tier 4 (muted) |
| Working-dir row leader |  folder-outline | `\u{F0770}` | nf-md | Tier 4 (muted) |
| Activity zone leader |  chevron-right | `\u{F0142}` | nf-md | Tier 4 (muted) |
| Activity zone heading separator |  arrow-right | `\u{F0054}` | nf-md | Tier 4 (muted) — used between session name and the entries (`tcm 䍡`) |
| Rolodex top separator |  chevron-up | `\u{F0143}` | nf-md | `surface1` (Tier 4 colour) — anchored mid-rule, marks the boundary above the focused card; always visible regardless of viewport position on the tape |
| Rolodex bottom separator |  chevron-down | `\u{F0140}` | nf-md | `surface1` — anchored mid-rule, marks the boundary below the focused card; always visible |
| Zone separator (rule between zones) | `─` | `\u{2500}` | box-drawing | `surface1` |
| Focused-card border (corners) | `╭ ╮ ╯ ╰` | `\u{256D} \u{256E} \u{256F} \u{2570}` | box-drawing | `blue` (focused) / `surface2` (unfocused) |
| Focused-card border (edges) | `─ │` | `\u{2500} \u{2502}` | box-drawing | same as corners |
| Current-session left bar | `▎` | `\u{258E}` | block | `blue` (focused) / `overlay0` (unfocused) |

### Header layout (with Clawd)

```
 tcm   3 sessions
^^                ^^^^^^^^
│                  Tier 4 muted readout
└─ Clawd, Tier 1 bold (focused) / Tier 2 (unfocused)
```

Single line. The Clawd glyph is left-most; product name follows in
Tier 1; session count in Tier 4 muted. No counters, no labels, no flash.
(Flash messages move to the activity zone — see §7.)

---

## 6. Cross-surface vocabulary alignment (panel ↔ statusline ↔ header)

The Clawd glyph appears in **three positions** across the product:

| Surface | Position | Meaning | Colour |
|---------|----------|---------|--------|
| Panel header | Leftmost cell | Product brand mark | `text`, bold |
| Tmux statusline | Per-window cell | "claude-code is alive in this window" | severity colour — lifts `docs/specs/tmux-header.md` §1's "presence-only" non-goal; replaces the current hard-coded `theme.blue`. Promoted to v1 of this redesign (same release as the panel work, separate PR is fine). |
| Panel right gutter | Per-agent row | "this row's agent is claude-code" | Tier 3 Dim — recedes vs. severity gutter on the left |

This is the HUD-grammar payoff: same glyph, three positions, three
unambiguous meanings. The user learns Clawd once.

The other identity glyphs (`π / ▲ / ♦ / `) follow the same pattern:
they appear in the panel right gutter and the statusline. They do NOT
appear in the header — the header is reserved for the product brand mark
(Clawd).

### Active-window vs. severity colour collision (statusline)

Spec §6 defines `window-status-current-style: fg=blue,bold` for the
active tmux window. When agent state is `working` (also `blue`), the
active window's text and glyph become the same colour and the glyph
loses its category job.

Resolution rule: **active-window text uses bold; severity glyph uses
colour without bold.** An active window with a working claude-code
renders as `[bold blue]project-name[/]  [blue] [/]` — distinguishable
by weight, not colour. When the agent is in any other state
(yellow/green/red/grey), both colour *and* weight differentiate.

### Implementer checklist for `tmux-header-sync.ts`

When the panel-side vocabulary lands, the following must propagate to
`packages/runtime/src/server/tmux-header-sync.ts` in the same release
(separate PR is fine; same release is required so the two surfaces
stay aligned):

- [x] **`AGENT_GLYPHS` table:** change `generic: "●"` to
  `generic: "\u{F167A}"` (nf-md robot-outline). All other entries
  already match the panel.
- [x] **Severity-aware glyph colour:** `computeWindowStates()` resolves
  `@os-agent-fg` per-window from the dominant agent's severity
  (working=blue, waiting=yellow, ready=green, stopped=surface2,
  error=red) via the local `severityLabel()` + `severityColour()` in
  `tmux-header-sync.ts`. The panel's resolver in `apps/tui/src/index.tsx`
  remains a separate copy for now — the doc-anchor at the top of those
  helpers tracks the divergence; extract to `runtime/themes.ts` if a
  third surface lands.
- [x] **Active-window collision:** confirmed in
  `integrations/tmux-plugin/scripts/header.tmux`. The format uses
  inline `#[fg=#{@os-agent-fg}]` (colour-only override); `bold` lives
  on the segment-level `window-status-current-style`. tmux inherits
  `bold` through the inline span, so an active working glyph renders
  bold-blue while inactive working glyphs render plain blue — weight
  differentiates the active tab when severity colour collides with
  `theme.blue`. No format-string change required.
- [x] **Spec update:** `docs/specs/tmux-header.md` is now v1.1. §1 lifts
  the "presence-only" non-goal; §3.1 documents the severity→palette
  mapping; §4 updates the `generic` row; §6 documents the active-window
  collision rule; §9 retires the now-shipped "status-aware glyph"
  future-work bullet.
- [x] **Test coverage:** `packages/runtime/test/tmux-header-sync.test.ts`
  adds 18 new cases covering `severityLabel`, `severityColour`, the
  per-status fg writes, multi-agent precedence (severity follows the
  picked agent), and a status-flip diff that re-emits `@os-agent-fg`
  without changing the glyph identity.

These can land as their own PR after the panel work; the panel does not
block on them. But the design is incomplete until both surfaces share
the vocabulary.

---

## 7. Activity zone vocabulary

### Visibility (locked)

The activity zone is **permanently reserved** in the panel layout. It is
always visible; it never appears or disappears in response to events.
Quiet sessions show an empty (or near-empty) zone; busy sessions show
the same zone with content. **No fade animations, no chrome motion, no
pop-in/pop-out.**

- Minimum height: **3 cells** (1 separator + 1 heading + 1 entry slot).
- Default visible-entry cap: **5 entries** (zone height = 1 + 1 + 5 = 7
  cells when full). On terminal heights below 30 cells, the cap drops
  to 3 entries (zone = 5 cells).
- Above the cap, older entries scroll up off the top. The buffer
  itself holds 200 entries in memory (unchanged from the current
  `metadata.logs` cap).

### Heading

```
 tcm 䍡
^^^^^^^^^^^^^^ ^
  session name  arrow-right (\u{F0054}), Tier 4 muted
  Tier 2
```

The session-name heading tells the user *whose narrative they're
reading*. It's the focused session's name (Tier 2) followed by an
nf-md arrow-right glyph (Tier 4) as a soft "→" separator.

When the user moves focus to a different session, the heading and
entries swap to that session's buffer. No transition animation; the
swap is instantaneous.

### Leader (single, constant)

```
 cc 1859  editing tmux-header-sync.ts
^
1 cell, nf-md chevron-right (\u{F0142}), Tier 4 Muted.
```

Per the locked decision: per-category icons are **deferred to a later
phase**. The activity zone uses one constant leader; the category
information lives in the *text* of the entry.

When per-category icons are eventually adopted, they replace the
chevron-right with the category-specific glyph (pen-nib for edits,
wrench for bash, etc., per `tail-claude-hud`'s `Icons` struct). That
upgrade is data-model-light because watcher events already carry the
needed category metadata.

### Entry shape (single-line ticker)

```
 cc 1859  editing tmux-header-sync.ts        ← freshest entry: Tier 2 italic
 cc 1859  ran  bun test  (passed)            ← history: Tier 3 italic
 pi 15c8  awaiting input                     ← history: Tier 3 italic
 [info]   port 3000 detected                 ← history: Tier 3 italic
 cc       stopped 4m ago                     ← history: Tier 3 italic
```

| Column   | Width      | Tier (freshest / history) | Role |
|----------|------------|---------------------------|------|
| 0        | 1 cell     | Tier 4 / Tier 4           | leader  |
| 1        | 1 cell     | —                          | padding |
| 2 → 11   | 10 cells   | Tier 2 / Tier 2           | source — agent code + threadId, OR `[info]`/`[warn]`/`[error]` for system messages |
| 12       | 1 cell     | —                          | padding |
| 13 → end | flex       | **Tier 2 italic** / Tier 3 italic | event description; outcome suffix `(passed)`/`(failed)` in `green`/`red` |

Only the *description* column changes tier between fresh and history;
the leader and source columns stay constant. This is the only
attribute change inside the zone (per the freshness rule in §4).

### Source format

- Per-agent events: `<2-letter agent code> <4-char thread suffix>`
  - `cc 1859` (claude-code), `pi 15c8`, `cd a3b1` (codex), `ap 89c2` (amp)
- System events: `[info]` / `[warn]` / `[error]` in tone colours

### Producers (locked)

Until this redesign, `metadata.logs` was a typed-and-rendered buffer
that nothing actually pushed to in production. The activity zone makes
the buffer load-bearing, so producers are now spec’d.

| Watcher                                  | Event                                | Emits                                                       |
|------------------------------------------|--------------------------------------|-------------------------------------------------------------|
| `claude-code-hooks.ts` / `pi-hooks.ts`   | tool call started                    | `{ source: "<code> <id>", message: "<tool name>", tone: "info" }` |
| same                                     | tool call finished (with outcome)    | `{ source: "<code> <id>", message: "ran  <cmd> (passed)", tone: "success" }` (or `(failed)` + `"error"`) |
| same                                     | agent state transition               | `{ source: "<code> <id>", message: "<state>", tone: "info"\|"error" }` |
| same                                     | new thread name                      | `{ source: "<code> <id>", message: "<thread name>", tone: "neutral" }` |
| any (server-side)                        | system event                         | `{ source: "[info]"\|"[warn]"\|"[error]", message: "...", tone: <matching> }` |

Producers POST to the server’s existing `/log` HTTP endpoint
(`packages/runtime/src/server/index.ts`); the server appends to
`metadata.logs` and broadcasts state. No new wire protocol.

### Persistence (locked)

- Each session keeps a rolling buffer of events (cap: 200 in memory).
- Buffer survives focus changes — coming back to a session shows what
  happened while focus was elsewhere.
- Buffer is reset only on session close.
- Implementation: server-side state, broadcast as part of
  `SessionData.metadata.logs`. The redesign moves *rendering* of that
  buffer from inside the focused card to the dedicated zone.

---

## 8. The retired list

| Element | Was used for | Replaced by |
|---|---|---|
| `·` (stopped status) | Status gutter | nf-md circle-small  `\u{F1428}` |
| `·` (activity-row leader) | `AgentListItem` row 2 prefix | nf-md chevron-right  `\u{F0142}` (Activity zone, different surface, no role collision) |
| `·` (log tone-icon, neutral) | Log entries inside focused card | Removed; logs move to activity zone, leader is chevron-right |
| `·` (port separator) | Port list inline | **Feature removed** — see Ports below |
| `●` (unseen marker) | Right of agent name | Teal name colour shift (color-only marker) |
| `●` (live-agent count badge) | Right of session name | Numeric digit in identity gutter (§3) |
| `●` (`generic` agent type) | Statusline glyph | nf-md robot-outline  `\u{F167A}` |
| `⎇` (branch glyph) | Branch row | Powerline branch  `\u{E0A0}` |
| `⚡` (running counter) | Header right side | Removed; severity gutters in rolodex make this implicit |
| `Sessions` literal label | Header | Removed; product name `tcm` carries the same role |
| `◆` (waiting) | Status gutter | nf-md bell-alert  `\u{F0179}` |
| `◇` (ready) | Status gutter | nf-md check-circle-outline  `\u{F05E1}` |
| `✗` (error) | Status gutter | nf-md alert-circle  `\u{F0028}` |
| `subtext1` (text colour token) | Various dim text | Removed from text-tier system |
| `overlay1` (text colour token) | Various dim text | Removed from text-tier system |
| **Ports detection (feature)** | Localhost port hint, clickable port list, branch+ports row | **Feature removed entirely.** During implementation, delete: the lsof polling loop in `packages/runtime/src/server/index.ts`, the `ports: number[]` field on `SessionData` in `packages/runtime/src/shared.ts`, all port rendering in `apps/tui/src/index.tsx`, and the port-hint width accounting in `packages/runtime/src/server/sidebar-width-sync.ts`. Update `docs/explanation/architecture.md` and `docs/reference/features-and-keybindings.md` to drop the feature mention. |
| `2π` (count + same-type glyph) | Right gutter for uniform multi-agent sessions | **Reverted to numeric-only `2`.** The complexity of width-sensitive glyph branching was not worth the marginal density gain (Codex review feedback B1/Q3). |
| `★` (claude-code fallback) | Statusline glyph when Clawd font missing | Demoted to a degraded-mode signal — Nerd Fonts now a hard requirement, font-missing is no longer "supported" |
| `✕` (dismiss control, always visible) | Left of agent name | Hidden until the agent row is the j/k focus; appears in left gutter momentarily |

---

## 9. Cell-budget summary

For a 30-cell sidebar:

```
0    5    10   15   20   25   30
│    │    │    │    │    │    │
[s]  name                     [i]      ← session row (collapsed)
[s]  cc  1859                 [i]      ← agent row inside focused card
      main                              ← branch row, no gutters (only when shown)
 cc 1859  editing tmux-header.ts       ← activity entry, no gutters
```

Severity gutter = 1 cell, identity gutter = 1 cell. Total chrome cost = 2
cells out of 30 (6.7%).

For a 24-cell sidebar (narrowest reasonable), 2-cell gutter cost becomes
8.3%. Still well below where information density would suffer.

The Nerd Font icons are spec'd as **single column-cell wide** in the
tmux-header spec §4; this carries forward.

---

## 10. Codepoint cheat-sheet (implementer reference)

For when you're writing the OpenTUI render code and need to drop these
in inline:

```ts
// Severity (left gutter)
const SEV_WORKING_SPINNER = SPINNERS[idx % SPINNERS.length];   // existing
const SEV_WAITING         = "\u{F0179}";  // nf-md-bell-alert
const SEV_READY           = "\u{F05E1}";  // nf-md-check-circle-outline
const SEV_STOPPED         = "\u{F1428}";  // nf-md-circle-small
const SEV_ERROR           = "\u{F0028}";  // nf-md-alert-circle

// Determinate-progress variant for working (8 fill levels)
const PROGRESS_GLYPHS = [
  "\u{F0A9E}", "\u{F0A9F}", "\u{F0AA0}", "\u{F0AA1}",
  "\u{F0AA2}", "\u{F0AA3}", "\u{F0AA4}", "\u{F0AA5}",
];

// Identity (right gutter)
const ID_CLAUDE_CODE = "\u{100CC0}";  // Clawd, vendored at fonts/Clawd.ttf
const ID_PI          = "\u{03C0}";    // π
const ID_CODEX       = "\u{25B2}";    // ▲
const ID_AMP         = "\u{2666}";    // ♦
const ID_GENERIC     = "\u{F167A}";   // nf-md-robot-outline

// Structural & branding
const BRAND_CLAWD    = "\u{100CC0}";  // same as ID_CLAUDE_CODE
const BRANCH_GLYPH   = "\u{E0A0}";    // Powerline branch
const FOLDER_GLYPH   = "\u{F0770}";   // nf-md-folder-outline
const ACTIVITY_LEAD  = "\u{F0142}";   // nf-md-chevron-right
const ACTIVITY_HEAD  = "\u{F0054}";   // nf-md-arrow-right (heading separator)
const WRAP_UP        = "\u{F0143}";   // nf-md-chevron-up (rolodex top)
const WRAP_DOWN      = "\u{F0140}";   // nf-md-chevron-down (rolodex bottom)
```

---

## 11. What this enables for `04-mockups/`

With the alphabet locked, mockups have a fixed grammar to draw inside.
Every mockup must:

- Place severity glyphs at column 0 only.
- Place identity glyphs at the rightmost column only.
- Use only the four text tiers (Primary / Secondary / Dim / Muted).
- Use only the glyphs in §2, §3, §5, §7. Anything else is a violation.
- The Clawd glyph appears in the header and (for claude-code agents) in
  the right gutter / tmux statusline. Nowhere else.

Deviations from grammar are now mechanical to spot in mockup review.
