# 04-mockups/02 · Canonical (the locked design)

This is the canonical mockup. Everything here is decided. The next
deliverable is `04-mockups/03-live-opentui/` — a `--mock` flag plumbed
into `apps/tui/src/index.tsx` rendering this exact design at this exact
dataset.

> **Updated 2026-04-26 after Codex review** (`REVIEW-NOTES-codex.md`).
> The activity zone is now permanently reserved (no auto-show / fade);
> count format is numeric-only; ports are removed entirely as a
> feature; collapsed-session severity is conditional (blank when
> nominal). See locked-decisions table below.
>
> **Updated 2026-04-28 after live-QA pivot.** The rolodex is now a
> *linear tape*: sessions stay in their natural order, the focused card
> stays vertically centred, and the viewport slides over them. The
> earlier wheel/rotation model (clockwise split into halves around the
> focused pin) was confusing in practice — users lost track of where
> they were in the list. Navigation (`j`/`k`) still wraps modularly so
> a single keystroke at the bottom takes you to the top. The chevron
> separators above and below the focused card stay; their semantics
> simplify from "wrap point" to "structural separator between focused
> card and surrounding tape." See locked decision #6.

---

## Locked decisions

| # | Question                              | Locked answer                                                                                                                |
|---|---------------------------------------|------------------------------------------------------------------------------------------------------------------------------|
| 1 | Detail-level interaction              | Activity zone is **permanently reserved**, always visible. New events populate the freshest entry; older entries scroll up. |
| 2 | Branch row inside focused card        | Single optional row, only when the session has a branch. Powerline-branch leader (Tier 4 muted).                             |
| 3 | Same-type count format                | **Numeric-only** — `2`, `3`, `9+`. The earlier `2π` form was reverted (Codex B1/Q3).                                          |
| 4 | Activity zone heading                 | **Session-name label** — `opensessions ` (focused-session name + nf-md arrow-right separator).                              |
| 5 | Rolodex wrap rules                    | nf-md chevron-up / chevron-down anchored mid-rule, always visible (`──  ──`, `──  ──`).                                     |
| 6 | Rolodex layout & navigation           | **Linear tape.** Sessions in natural order; focused card pinned at vertical centre; viewport slides over the tape. `j`/`k` wrap modularly (a single press at either end snaps to the opposite end). The earlier wheel/rotation model is retired. |
| ↑ | What about ports as a feature?        | **Removed entirely.** During implementation, delete the lsof polling loop, the `ports: number[]` field on `SessionData`, the rendering code, the width-sync accounting, and the doc references. |
| ↑ | Unseen state visual                   | **Colour-only** — name shifts to `teal` (replaces Tier 2/Tier 1 colour). No glyph. See `03-vocabulary.md` §4.                |
| ↑ | Italic                                 | Sanctioned only inside the activity zone description column (Tier 3 + italic; freshest entry steps up to Tier 2 + italic). Anywhere else, italic is a violation. |

---

## Activity zone behaviour

The activity zone is structural — like the header, it is part of the
panel's permanent layout. When the focused session has no recent
activity, the zone shows its heading and empty entry slots. When events
arrive, they populate the entry slots:

- **Newest entry** (top of the zone): Tier 2 + italic — slightly
  brighter than history. There is exactly one freshest entry at any
  time.
- **History entries**: Tier 3 + italic — readable but quiet.
- **Older than the visible cap (default 5)**: scrolls up off the top.
- **Buffer**: 200 entries per session in memory; `a` key (placeholder —
  finalised in `05-spec.md`) opens a full-history view.

There is no fade-in, fade-out, pop-in, pop-out, or chrome animation.
The only attribute change is the previous-freshest entry stepping down
from Tier 2 italic to Tier 3 italic when a newer entry arrives.

### Event sources that produce activity entries

| Source                                | Example                                            |
|---------------------------------------|----------------------------------------------------|
| Tool call started                     | `pi 15c8  ask_user`                                |
| Tool call finished (with outcome)     | `cc 1859  ran bun test (passed)`                   |
| Agent state transition                | `cc 1859  errored` / `pi 10bc  awaiting input`     |
| New thread name from a watcher        | `cc abcd  refactor watchers`                       |
| Skill / permission prompt             | `cc 1859  Base directory for this skill: /Users/…` |
| System events                         | `[info]  …` / `[warn]  …` / `[error]  …`           |

When focus moves to a different session, the zone heading and entries
swap to that session's buffer. Instantaneous; no animation.

---

## Canonical state — quiet (focused pane, no recent activity)

```
┌──────────────────────────────────────┐
│   opensessions   5 sessions          │   ← HEADER (Clawd brand, Tier 1)
│  ──────────────────────────────────  │   ← zone separator
│                                      │
│   ai-engineering-template       󱙺   │   ← session 1, 1 generic agent, no severity (ready)
│   pi-mono                        2   │   ← session 2, count=2, no severity (all ready)
│                                      │
│   ──────────────  ───────────────   │   ← rolodex top wrap-rule (nf-md chevron-up)
│                                      │
│  ╭────────────────────────────────╮  │   ← FOCUSED card border
│  │ ▎opensessions               4 │  │   ← session row (working spinner, count 4)
│  │ ▎ pi  15c8                 π  │  │   ← agent: pi #15c8 working
│  │ ▎ pi  10bc               󰗡 π  │  │   ← agent: pi #10bc ready
│  │ ▎ claude-code            󰗡   │  │   ← agent: claude-code ready
│  │ ▎ claude-code 1859       󰗡   │  │   ← agent: claude-code #1859 ready
│  ╰────────────────────────────────╯  │
│                                      │
│   ──────────────  ───────────────   │   ← rolodex bottom wrap-rule (nf-md chevron-down)
│                                      │
│   claude-code-system…                │   ← session 4, no agents, no badge
│   the-themer                  󰗡 󱙺  │   ← session 5, 1 generic agent ready
│                                      │
│  ──────────────────────────────────  │   ← zone separator
│   opensessions                       │   ← ACTIVITY heading (focused-session name)
│   (no recent activity)               │   ← empty zone, Tier 4 muted placeholder
│                                      │
│  ──────────────────────────────────  │   ← zone separator
│   j/k nav  ↵ switch  q quit  a hist  │   ← FOOTER (added: `a` shows activity history)
└──────────────────────────────────────┘
```

Notes:
- **Severity gutter is empty** for the two collapsed top sessions
  (`ai-engineering-template`, `pi-mono`) and `the-themer` because all
  their agents are ready/stopped. This is the B5 resolution: nominal
  collapsed sessions don't carry a severity glyph; only attention-needing
  states (working / waiting / error) do.
- **No branch row** under the focused-card session line yet — branch
  appears as one optional row when shown (next state).
- **Activity zone is visible but empty** — `(no recent activity)` is
  rendered in Tier 4 muted as a placeholder. Some implementations may
  prefer a literal blank; mockup picks the friendlier hint.

---

## Canonical state — live (focused pane, events arriving)

The user has been navigating; the focused session has a stream of
events:

```
┌──────────────────────────────────────┐
│   opensessions   5 sessions          │
│  ──────────────────────────────────  │
│                                      │
│   ai-engineering-template       󱙺   │
│   pi-mono                        2   │
│                                      │
│   ──────────────  ───────────────   │
│                                      │
│  ╭────────────────────────────────╮  │
│  │ ▎opensessions               4 │  │
│  │   main                          │  │   ← branch row (Tier 4 muted, Powerline branch)
│  │ ▎ pi  15c8                 π  │  │
│  │ ▎ pi  10bc               󰗡 π  │  │
│  │ ▎ claude-code            󰗡   │  │
│  │ ▎ claude-code 1859       󰗡   │  │
│  ╰────────────────────────────────╯  │
│                                      │
│   ──────────────  ───────────────   │
│                                      │
│   claude-code-system…                │
│   the-themer                  󰗡 󱙺  │
│                                      │
│  ──────────────────────────────────  │
│   opensessions                       │
│   pi 15c8  ask_user                  │   ← FRESHEST: Tier 2 italic
│   cc 1859  Base directory for        │   ← history: Tier 3 italic, multi-line
│             this skill: /Users/      │
│   cc 1859  ran  bun test (passed)    │   ← history; outcome `(passed)` in green
│   pi 10bc  awaiting input            │   ← history
│  ──────────────────────────────────  │
│   j/k nav  ↵ switch  q quit  a hist  │
└──────────────────────────────────────┘
```

Notes:
- The freshest entry (`pi 15c8 ask_user`) renders **Tier 2 italic** —
  the *only* visual signal that distinguishes "just happened" from
  "history." All older entries are Tier 3 italic.
- Multi-line entry: `cc 1859 Base directory for this skill: /Users/`
  wraps to two lines. Continuation indents under the description
  column (column 13+); the source column stays empty on the
  continuation. This is permitted in the activity zone — multi-line
  is by design.
- The branch row inside the focused card is shown here because
  `opensessions` has a branch (`main`).
- **Layout is identical to the quiet state above** — no zone moved, no
  card resized, no chrome animated. Only the zone's *content* changed.

---

## Canonical state — errored (focused pane, errored agent in non-focused session)

The cursor is on `pi-mono`. `the-themer` has an errored agent. Activity
zone shows the `pi-mono` session's narrative.

```
┌──────────────────────────────────────┐
│   opensessions   5 sessions          │
│  ──────────────────────────────────  │
│                                      │
│   ai-engineering-template       󱙺   │
│                                      │
│   ──────────────  ───────────────   │
│                                      │
│  ╭────────────────────────────────╮  │
│  │ ▎pi-mono                     2 │  │   ← focused on pi-mono
│  │   main                          │  │   ← branch row (Tier 4 muted)
│  │ ▎ pi  20cd               󰗡 π  │  │
│  │ ▎ pi  20de               󰗡 π  │  │
│  ╰────────────────────────────────╯  │
│                                      │
│   ──────────────  ───────────────   │
│                                      │
│   opensessions                  4   │   ← collapsed, working spinner left
│   claude-code-system…                │
│ û the-themer                    󱙺   │   ← ERRORED — left severity (red alert-circle), right identity (robot-outline, Tier 3 dim)
│                                      │
│  ──────────────────────────────────  │
│   pi-mono                            │   ← activity zone, focused-session is pi-mono
│   pi 20cd  saw new file              │   ← freshest, Tier 2 italic
│   pi 20de  ran  pytest (passed)      │
│  ──────────────────────────────────  │
│   j/k nav  ↵ switch  q quit  a hist  │
└──────────────────────────────────────┘
```

Notes:
- `the-themer`'s **left severity gutter** carries the red alert-circle
  (`û`, `\u{F0028}`). The eye is hooked by colour first, position
  second.
- `the-themer`'s **right identity gutter** carries the agent-type glyph
  (`󱙺` robot-outline) in **Tier 3 Dim**. The identity rule never
  changes per state — left = severity, right = identity, full stop.
  The errored row reads "this session is errored AND has a generic
  agent" via two unambiguously-positioned signals.
- The errored severity propagates to the rolodex's left gutter for
  collapsed sessions per the §B5 rule (errored is one of the three
  states that gets a glyph; ready/stopped don't).
- The activity zone is now showing `pi-mono`'s narrative because that's
  the focused session. `the-themer`'s errored event is still visible
  at the rolodex level; opening it (j/k focus) would swap the activity
  zone to its buffer.

---

## Canonical state — pane unfocused (the panel is not the active pane)

When the user has clicked into a different tmux pane and the
opensessions panel is *unfocused*, every text tier slides one step
dimmer (per `03-vocabulary.md` §4). Severity colours and identity
glyphs stay at full strength — they're not tier-controlled.

```
┌──────────────────────────────────────┐
│   opensessions   5 sessions          │   ← Clawd: Tier 2 (was Tier 1 bold)
│  ──────────────────────────────────  │   ← separator: surface1 (unchanged)
│                                      │
│   ai-engineering-template       󱙺   │   ← name: subtext0 (was text)
│   pi-mono                        2   │   ← name: subtext0; digit: subtext0 + faint
│                                      │
│   ──────────────  ───────────────   │   ← chevrons: unchanged surface1
│                                      │
│  ╭────────────────────────────────╮  │   ← border: surface2 (was blue)
│  │ ▎opensessions               4 │  │   ← bar: overlay0 (was blue); name: subtext0+bold
│  │   main                          │  │
│  │ ▎ pi  15c8                 π  │  │   ← spinner: still blue (severity bypass)
│  │ ▎ pi  10bc               󰗡 π  │  │   ← 󰗡 still green; identity glyphs Tier 3 dim
│  │ ▎ claude-code            󰗡   │  │
│  │ ▎ claude-code 1859       󰗡   │  │
│  ╰────────────────────────────────╯  │
│                                      │
│   ──────────────  ───────────────   │
│                                      │
│   claude-code-system…                │   ← name: subtext0
│   the-themer                  󰗡 󱙺  │
│                                      │
│  ──────────────────────────────────  │
│   opensessions                       │   ← heading: subtext0 (was text)
│   pi 15c8  ask_user                  │   ← freshest: subtext0 italic (was text)
│   cc 1859  ran  bun test (passed)    │   ← history: subtext0 + faint italic
│  ──────────────────────────────────  │
│   j/k nav  ↵ switch  q quit  a hist  │   ← keys: surface2 (was subtext0)
└──────────────────────────────────────┘
```

Notes:
- The whole panel reads "asleep" but legible. Severity colours
  (working spinner `blue`, ready `󰗡 green`) stay at full strength so
  the user can see at a glance that something needs attention even if
  they're not in the panel.
- Identity glyphs (`π`, `󱙺`, etc.) are already Tier 3 Dim when the
  panel is focused; in unfocused state they slide to Tier 3-mirror
  (subtext0 + faint), staying recognisable.
- The focused-card border drops from `blue` to `surface2`, signalling
  "this panel does not own the cursor." The current-session left bar
  (`▎`) drops from `blue` to `overlay0` similarly.

---

## Cross-references

- Vocabulary table: `03-vocabulary.md` §2 (severity), §3 (identity),
  §5 (structural), §10 (codepoint cheat-sheet)
- Zone layout: `02-zones.md`
- Style stance: `00-grounding.md` §7
- Statusline strips: `01-proposal.md` §"Statusline strips"
- Codex review: `REVIEW-NOTES-codex.md`

---

## What's needed to promote to live OpenTUI

`04-mockups/03-live-opentui/` will need:

1. **A `--mock <scenario>` flag** in `apps/tui/src/index.tsx` that
   bypasses `ensureServer()` and feeds the renderer a pre-canned
   `SessionData[]`. Scenarios to include:
   - `quiet` — quiet state above
   - `live` — same dataset with activity zone populated
   - `errored` — pi-mono focused, the-themer errored
   - `unfocused` — pane-unfocused mirror state
   - `wide` — terminal width 50+ to verify proportions
   - `narrow` — terminal width 24, the squeeze case
2. **The new vocabulary actually wired into render code** — replace
   the existing `SPINNERS / TONE_ICONS / UNSEEN_ICON / agentBadge`
   etc. with the codepoints in `03-vocabulary.md` §10.
3. **Tier system primitives** — a small helper that returns a `text`
   span with the correct `fg + attributes` for each of the 4 tiers,
   factoring in pane focus state. Italic is a sanctioned Tier-3
   modifier inside the activity zone description column only.
4. **Two-gutter grammar** — the row layout helper that reserves
   column 0 (severity) and the rightmost column (identity) and lets
   content flex in the middle. Severity gutter respects the B5 rule:
   blank when nominal.
5. **Always-visible activity zone** — fixed-height layout, no
   animation. Pulls from the existing `metadata.logs` buffer, scoped
   to the focused session.
6. **Rolodex wrap chevrons** — replace the `─ × 200` rule with a
   chevron-bearing rule using `\u{F0143}` (up) and `\u{F0140}` (down).
7. **Unseen colour-only marker** — name colour shifts to `teal` per
   `03-vocabulary.md` §4 unseen rules.
8. **Ports feature deletion** (in this same wave or a parallel PR):
   delete the lsof polling, the `SessionData.ports` field, the
   rendering code, the width-sync accounting, and the docs references.

---

## What this canonical doc does NOT yet specify

Deliberately deferred to the live OpenTUI iteration:

- **Per-tool-category activity icons** (pen-nib for edit, wrench for
  bash, magnifying glass for grep). Locked decision was "single
  constant chevron leader for v1; per-category later."
- **Configurable detail levels per session.** Locked: not in v1.
- **Exact `a` keybind for activity history view.** Currently shown as
  placeholder `a hist` in the footer; finalised in `05-spec.md`.
- **Light-mode palette verification.** Catppuccin Latte and Day are in
  the theme list but the new design hasn't been A/B'd against them
  visually.
- **Statusline implementation details.** Covered separately in
  `01-proposal.md` §"Statusline strips" and `03-vocabulary.md` §6
  including the implementer checklist for `tmux-header-sync.ts`.
