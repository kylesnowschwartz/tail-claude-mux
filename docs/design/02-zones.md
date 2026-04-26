# 02 · Panel zone layout

This doc resolves the *structural* question — what zones the side panel is
divided into, what each zone owns, and how they size — before we pick a
glyph vocabulary. Vocabulary follows structure.

The four foundational decisions from the audit (locked 2026-04-26):

1. **Glyph alphabet:** clean-slate. New vocabulary derived from one
   principle, not a patch over the existing reuse.
2. **Status grammar:** two gutters per row — left for severity, right for
   identity.
3. **Activity:** retired from inside the focused-session card; promoted to
   its own panel zone.
4. **Header counters:** dropped. The rolodex is the summary.

**Updated 2026-04-26 after Codex review (B3, B5, ports removal, count format).**
The activity zone is permanently reserved (no auto-show); collapsed-session
severity is conditional (blank when nominal); ports are removed as a feature;
count format is numeric-only. See `04-mockups/02-canonical.md` for the
rendered locked design and `REVIEW-NOTES-codex.md` for the review log.
---

## 1. The four zones

```
┌─────────────────────────────────────┐ ← terminal top
│ HEADER   sticky · 1 cell            │   "opensessions   N sessions"
├─────────────────────────────────────┤   (rule)
│                                     │
│ ROLODEX  flex · grows / shrinks     │   sessions list, focused pinned
│                                     │
│   …before-focus sessions…           │
│                                     │
│   ┌─ FOCUSED ─────────────────┐     │
│   │ session + its agent rows  │     │   ← only chrome that earns it
│   └───────────────────────────┘     │
│                                     │
│   …after-focus sessions…            │
│                                     │
├─────────────────────────────────────┤   (rule)
│ ACTIVITY   sticky · 3–8 cells       │   live narrative for focus context
├─────────────────────────────────────┤   (rule)
│ FOOTER     sticky · 1 cell          │   keybinding hints
└─────────────────────────────────────┘ ← terminal bottom
```

### Sizing rules

| Zone     | Sizing               | Behaviour on resize |
|----------|----------------------|---------------------|
| Header   | sticky, 1 cell       | never grows         |
| Rolodex  | `flexGrow=1`         | absorbs all spare height; the focused card stays pinned at vertical centre |
| Activity | sticky, 3–7 cells    | **Permanently reserved.** Always visible (1 separator + 1 heading + 1 entry slot = 3 cells minimum). Grows up to 7 cells (1+1+5 entries) when the buffer is non-empty; collapses to 5 cells (1+1+3 entries) on terminals with height < 30. Never hides, never animates. |
| Footer   | sticky, 1 cell       | never grows         |

The rules between zones are 1-cell separators (`─`), low-value (`surface1`
or `overlay0`). They are **earned chrome** under the §7 stance: they tell
the eye where one zone ends and the next begins, which serves HUD position
grammar.

---

## 2. HEADER zone

```
opensessions   3 sessions
```

- Single line.
- Left: the literal `opensessions` in `subtext0` (pane unfocused) or `text`
  (pane focused). It is the only place the product name appears, so it
  doesn't waste data-ink.
- Centre/right: a session-count readout. No `⚡`, `✗`, `●` counters; the
  rolodex shows running / errored / unseen state per row.

**What's gone vs. today:** `Sessions` literal label, running counter, error
counter, unseen counter, transient flash message.

**Where the flash message goes:** the activity zone (§4). A flash like
`focus-agent-pane sent` is itself activity — that's where it belongs.

**Open question (default if unanswered):** should there be a tiny one-cell
*system pulse* on the right edge of the header — a single glyph that turns
amber when *anything* across all sessions needs the user's attention? It's
a HUD-style "master caution" idea. Default: no, because the rolodex is
itself the pulse. Reopens if Phase-3 mockups feel too quiet.

---

## 3. ROLODEX zone

The rolodex is a **linear tape**: sessions stay in their natural order,
the focused card is pinned at the vertical centre of the zone, and the
viewport slides over the tape as the focus index changes. Navigation
(`j`/`k`) wraps modularly — a single press at either end of the list
snaps the viewport to the opposite end — but **the visible layout never
rotates.** Sessions appear in stable, predictable positions relative to
each other; only the viewport's location on the tape moves.

An earlier draft modelled this as a wheel (clockwise rotation around the
focused pin, with the surrounding sessions split into halves). That was
retired after live-QA disorientation; see
`04-mockups/02-canonical.md` locked decision #6.

The chevron rules above and below the focused card are kept as
*structural separators* (always visible, not gated on any list-wrap
point). What changes inside each card:

### 3.1 Session row (collapsed)

Two-gutter grammar:

```
[sev]  name                        [ident]
```

- **Severity gutter (1 cell, left).** Shows the rolled-up severity glyph
  for the worst agent state in that session, but **only when that state
  is `working`, `waiting`, or `error`.** Blank when all agents are
  `ready`/`stopped` or there are no agents — the rolodex stays calm
  except where attention is genuinely needed. Coloured by EFIS
  convention. (See `03-vocabulary.md` §2 "Severity row eligibility".)
- **Content (flex, middle).** Session name. Branch moves *into* the
  focused card; the collapsed row carries only the name.
- **Identity gutter (1 cell, right).** For collapsed session rows the
  identity gutter shows a small numeric agent-count (`3`, `2`, blank if
  0). Locked as numeric-only — `2π` was reverted; see
  `03-vocabulary.md` §3 "Numeric agent count formatting".

### 3.2 Focused session card

When focused, the card opens into a per-agent rollup. Each agent row
inherits the same two-gutter grammar:

```
┌──────────────────────────────────────┐
│ [sev]  main                  [count]│   ← session line (top)
│         main                       │   ← branch row, Tier 4 muted leader
│ [sev]    cc  1859                  │   ← agent rows
│ [sev]    pi  15c8                π  │
│ [sev]    cc                        │
└──────────────────────────────────────┘
```

- The session line at the top is the same row that appears collapsed.
- The card optionally adds **one branch row** beneath, prefixed by the
  Powerline branch glyph `\u{E0A0}` (Tier 4 muted). The branch row only
  appears when the session has a branch — sessions outside a git repo
  drop the row entirely.
- **Ports row removed.** The `ports` feature is being deleted in this
  redesign — see `03-vocabulary.md` §8 retired list, item "Ports
  detection (feature)". Implementation deletes the server-side polling
  and the contract field.
- Each agent row's right-gutter is the *agent-type identity* glyph (Clawd
  `\u{100CC0}` for claude-code, `π / ▲ / ♦` for pi/codex/amp, nf-md
  robot-outline `\u{F167A}` for generic). Full table: `03-vocabulary.md`
  §3.
- **Activity is not in this card.** It's in the activity zone (§4),
  which is permanently reserved at the bottom of the panel.
- **Working directory, log tail, metadata text** are not in this card
  either. They appear in the activity zone.

### 3.3 Chrome around the focused card

Earned: the rounded border tells the eye where the rolodex pin is and
gives the focused-region weight under HUD discipline. Kept.

Unearned: any border around collapsed cards. Removed. Hierarchy is carried
by indent, weight, and the focused border alone.

### 3.4 Cell-budget sketch (30-cell sidebar)

```
0    5    10   15   20   25   30
│    │    │    │    │    │    │
[s] [content                ][i]
[s][.][content              ][i]   ← agent row, +1 indent
```

- 1 cell severity gutter at column 0–0
- 1 cell padding at column 1
- 1 cell agent-row indent at column 2 (only inside focused card)
- content: columns 3 → 28 (~25 cells)
- 1 cell padding at column 28
- 1 cell identity gutter at column 29

For a narrower 24-cell sidebar the content drops to ~19 cells, still
adequate for `cc 1859` + a short suffix.

---

## 4. ACTIVITY zone

This zone is the live narrative of *what the session you're looking at
is doing*. The rolodex stays still and identity-focused; events scroll
through this band.

### Visibility (locked)

**Permanently reserved.** The activity zone is always present in the
layout. It does not appear, fade, or animate. Quiet sessions show an
empty zone (just heading + blank entry slots); busy sessions show the
same zone with content. There is no "auto-show" because there is
nothing to show — it's already there.

Heights:
- Minimum: 3 cells (1 separator + 1 heading + 1 entry slot).
- Default visible-entry cap: 5 entries (zone height = 7 cells when
  full).
- On narrow terminals (height < 30): visible-entry cap = 3 (zone = 5
  cells).
- Above the cap: older entries scroll off the top. Buffer holds 200
  in memory (unchanged from existing `metadata.logs` behaviour).

### Layout

```
──────────────────────────────────────
 opensessions 䍡                          ← heading: focused-session name + arrow-right
 cc 1859  editing tmux-header-sync.ts   ← freshest entry: Tier 2 italic
 cc 1859  ran  bun test  (passed)       ← history: Tier 3 italic
 pi 15c8  awaiting input                ← history: Tier 3 italic
 cc       stopped 4m ago                ← history: Tier 3 italic
──────────────────────────────────────
```

Full glyph and column rules: `03-vocabulary.md` §7.

### Reflow rule (B3 resolution)

Because the zone is permanently reserved, the rolodex always operates
with a known available height: `terminal_height - header(1) - zone_sep(1)
- activity_zone_height - footer(1) - footer_sep(1)`. The rolodex flexes
into that available space; the focused card stays pinned at vertical
centre. **Nothing in the layout moves when activity content arrives** —
only the entry rows inside the (already-rendered) zone change.

This is the cleanest answer to Codex's reflow question: there is no
reflow. The space was always there.

### Why this zone exists (rationale)

- Removes the per-agent row 2 from the focused card, which is the
  source of the wrap-row catastrophe ("Editing tmux-header-/sync.ts").
- Gives activity its own typography (italic) so it can never collide
  with identity rows.
- Lets activity carry timestamps and outcomes ("ran bun test
  (passed)") that don't fit on a one-line agent row.
- Mirrors the cockpit MFD convention: identity stays in the page;
  events scroll in a dedicated band.

---

## 5. FOOTER zone

Keep current behaviour. Compact key-hint line, dimmed when pane unfocused.
A future iteration can mode-switch this (e.g. show different keys when
agent panel has focus vs. session panel) — current implementation already
does this; no redesign needed.

---

## 6. Cross-surface vocabulary alignment (panel ↔ statusline ↔ header)

The redesign locks in **identity glyphs are shared** between panel
right-gutter and tmux statusline. With Nerd Fonts now a hard requirement,
claude-code resolves to Clawd (`\u{100CC0}`) in both places — the `★`
fallback is treated as a degraded mode, not a supported state. Same
principle for `π / ▲ / ♦` and the new generic glyph (nf-md robot-outline,
`\u{F167A}`).

**Clawd appears in three positions** across surfaces — panel header (brand
mark), tmux statusline (window-presence), panel right gutter (per-agent
identity). Same glyph, different positions, different meanings. This is
the HUD-grammar payoff that makes the redesign cohere across surfaces.

Severity glyphs were *panel-only* in v1 of the spec; the statusline
currently paints all glyphs in `theme.blue`. The redesign **lifts the
presence-only non-goal** — statusline glyphs render in severity colour
using the same colour rules as the panel. Implementation lives in the
same release as the panel work (separate PR is fine). Concrete
implementer checklist: `03-vocabulary.md` §6 "Implementer checklist for
`tmux-header-sync.ts`".

The detailed glyph alphabet is `03-vocabulary.md`'s job. This doc only
fixes that **the alphabet is shared across surfaces** and **Clawd is the
product brand mark across all three positions**.

---

## 7. What information moved where

A diff against the current panel:

| Information | Today | After |
|---|---|---|
| Total session count | Header counter | Header readout |
| Running count | Header `⚡N` | Implicit (rolodex severity glyphs) |
| Errored count | Header `✗N` | Implicit (rolodex severity glyphs) |
| Unseen count | Header `●N` | Per-row in rolodex (no header tally) |
| Transient flash | Header right-side | Activity zone, top entry |
| Per-session severity | Right-edge floating glyph | Left severity gutter |
| Per-agent severity | Right-edge floating glyph | Left severity gutter |
| Per-agent identity | Plain text name only | Right identity gutter (glyph) + plain text name |
| Branch | Row 2 of session card | Dim row inside focused card only |
| Port hint | Row 2 of session card (collapsed) | **Feature removed** — see `03-vocabulary.md` §8 |
| Per-agent activity | Row 2 of `AgentListItem` | Activity zone |
| Thread name | Row 2 of `AgentListItem` | Dropped from default UI; surfaces when activity zone shows that agent's events |
| Thread id | After agent name | Inline after agent name (kept; small dim suffix) |
| Working dir | Focused card extension | Activity zone or detail mode |
| Metadata logs | Focused card extension | Activity zone |
| Dismiss control | Always-visible `✕` | Hidden until agent row is the j/k focus |

---

## 8. What this means for the next deliverable

`03-vocabulary.md` now has a much smaller surface to specify, because the
zones tell us what we need glyphs for:

1. **Severity** (5 states): shared between session-rollup and per-agent
2. **Identity** (one per agent type + a generic): shared between panel
   right-gutter and statusline
3. **Activity-leader** (one glyph for the activity zone's row prefix)
4. **Structural** (rule character, focused-card border, wrap-line marker)

That's it. No glyph for unseen (use teal name highlight, color-only —
see `03-vocabulary.md` §4 "Unseen state"). The branch row inside the
focused card uses a Tier 4 muted Powerline-branch leader (`\u{E0A0}`).
Ports were retired entirely (feature removal); see `03-vocabulary.md`
§8.
