# Activity Zone Redesign — Design Spec

## Context

The "activity zone" is a fixed-height structural band beneath the rolodex in the
tcm tmux sidebar. It surfaces `metadata.logs` for the focused session — a stream
of derived events emitted by the runtime's agent watchers (Claude Code, pi,
codex, amp).

The user looks at it to know *"what is the agent doing?"* — without it, they
must context-switch to the agent's pane to read tool output, breaking flow.

## Constraints

**Spatial.** Sidebar pane, typical width 30–60 cols. Cap of 5–7 entries
depending on terminal height. Lives below the rolodex (which ends with a `▼`
wrap chevron) and above the footer keybind row.

**Source data.** Each entry is

```ts
{ message: string; ts: number; source?: string; tone?: "neutral" | "info" | "success" | "warn" | "error" }
```

Sources are either `pi xxxx` / `cc xxxx` (7 chars, `agentCode + " " + 4-char threadId suffix`)
or system tags like `[bell]`. Many entries have no source. The runtime
debounces emissions and emits one entry per *changed* signal (toolDescription,
threadName, status transition).

**Existing primitives — `vocab.ts`:**

- Severity glyphs: brail spinner (working), `nf-md-bell-alert` (waiting),
  `nf-md-check-circle-outline` (ready), `nf-md-stop-circle` (stopped),
  `nf-md-alert-circle` (error)
- Identity: vendored Clawd glyph for Claude-Code threads
- Structural: `nf-md-source-branch`, `nf-md-folder-question-outline` (dir
  mismatch), `nf-md-chevron-right` / `nf-md-arrow-right`,
  `nf-md-chevron-up` / `nf-md-chevron-down` (rolodex wrap rules)
- The vocab is auditable: components import from `vocab.ts`, never compose
  glyphs ad-hoc. New glyphs go through `vocab.ts`.

**Existing tier system — `tiers.ts`:**

- Tier 1 (primary) = bold + text colour — dynamic, urgent
- Tier 2 (secondary) = text colour — stable context
- Tier 3 (dim) = faint + text colour — supporting detail
- Tier 4 (muted) = overlay0 (no faint) — static chrome

When the panel pane is unfocused, every tier slides one step dimmer
(`text → subtext0`, `overlay0 → surface2`). Severity colours and identity
glyphs bypass the tier system (always full intensity).

**Renderer.** OpenTUI + solid-js. Layout via flex (yoga). Box-drawing chars
fine. Wrap mode default `"word"`; `<text truncate>` exists but unreliable —
pre-truncating in JS is the proven path.

## Current implementation (shipped; WIP commit `872a621`)

```
                                       ← air row
pi db92 · Reading build.ts             ← prefix row, source on change
Reading tsconfig.json                  ← continuation, no prefix, full width
Reading package.json
Reading scenarios.ts
Reading tiers.ts
─────────────────                      ← footer rule (separate component)
→ cycle ⏎ go d hide ? help            ← keybinds (separate component)
```

Layout: `pad(1) | desc(*) | pad(1)`. Description gets the full width.

Source label appears inline, Tier 4 muted, with ` · ` (middle dot) separator
between source and description. Renders only on rows where source differs from
the row above, *or* on the first visible row (anchor).

Continuation rows have **no indent** — they start at the left margin. The
ragged left at source-change rows is intentional: it visually tags the change.

Description weight: Tier 1 (bold) for freshest, Tier 3 (dim) for older. Italic
dropped.

Description is pre-truncated with `…` to avoid wrap orphans.

Empty state: `(no recent activity)` in Tier 4 muted, with breathing-room above
and below.

## HUD-first evaluation of current state

The user reads the rows as prose. Eye scans verb→object pairs.

The four watcher questions:

| Question | Current state | Verdict |
|---|---|---|
| Liveness — *is something happening?* | Unrepresented. No rate signal. | Fail |
| Pattern — *cycling, converging, stuck?* | Rows show events, not shape. | Fail |
| Exception — *did anything go wrong?* | Tone-coloured but no peripheral hit. | Fail |
| Latest — *what's the freshest action?* | Bold-weight freshest row. | Pass |

**1-of-4.** The one we answer is the copilot's question. The three we miss are
the HUD's questions.

**Primitives audit:** rows use `weight` (bold/dim) and (occasionally) `colour-band`
(tone for system events). Missing: sparkline, gutter cell, badge,
small-multiples-via-shared-instrument.

**Spellcheck test** (does a marker appear on the line of work?): Side-pane,
not inline-with-work. Within its constraints, signals are on the lines they
describe (good); no `!` gutter on failed-test rows (could be).

**Sparkline test** (≤8 fixed glyphs inside an existing line; shared baseline):
Fails. No sparkline. Liveness/rate is invisible.

## Proposed direction (sketch — open to revision)

```
 ▁▂▂▃▅▇▇▆  7/min                       ← rate sparkline, ≤8 cells, 60s window
                                        ← air row
 pi db92                                ← source eyebrow, only on change
●  build.ts                            ← gutter `●` on freshest; verb glyph col 1
   tsconfig.json                       ← small-multiples: same instrument per row
   package.json
   scenarios.ts
   tiers.ts                            ✗ ← end-of-row badge if outcome=failed
```

Three primitives layered:

- **Sparkline (top row).** Rate over last 60s — answers liveness + pattern by
  peripheral glance. ≤8 cells, fixed `▁▂▃▄▅▆▇█` alphabet, shared baseline if
  ever stacked.
- **Gutter cell `●`** on freshest row. Salience without bold-noise. Single-cell
  marker in column 0.
- **Verb glyph in column 1.** ` ` read / ` ` list / ` ` search / ` ` edit /
  ` ` run. Small-multiples instrument: same shape in same column on every
  row, so the eye sees verb-rate at a glance ("five reads, then a search"
  reads as one shape).
- **Badge at row-end.** `✓` / `✗` only when outcome is present. Exception by
  glance.

Source label demoted from inline-prefix to **eyebrow line** above its run (Tier 4
muted, no separator). Cost: extra row per source change. Benefit: source no
longer crowds the description column on prefix rows.

## Open design questions

1. **Sparkline window and unit.** 60-second rate? Per-minute count? Bursts vs
   idle phases? What's the resolution and how does the y-axis scale (auto or
   fixed)?

2. **Sparkline labelling.** "7/min" suffix is one option. Could also be a max
   tick mark, a sample-size label, or no text at all.

3. **Gutter on freshest only, or gradient?** Single `●` on freshest is HUD-clean.
   A gradient (`●◐○○○`) could encode recency rank but adds noise.

4. **Verb glyph dictionary size.** Sketch shows 5 (read/list/search/edit/run).
   Some agent activities ("ran bun test", "awaiting input", "errored") don't
   have natural glyphs. Fallback options: a generic ▶ glyph + show text;
   no glyph for non-tool events; or expand the dictionary judiciously
   (`waiting`, `interrupted` are status transitions, not tool calls — maybe
   they get severity glyphs instead of verb glyphs).

5. **Eyebrow vs inline source.** Trade-off:
   - Eyebrow: cleaner row, source out of the description column. Costs a row
     per source change.
   - Inline (current): no row cost. Crowds description on change rows.
   - Multi-source bursts (e.g. `pi 15c8` and `cc 1859` interleaving every
     1–2 entries) make eyebrows expensive.

6. **Heading absorption.** The sparkline row could double as the heading region
   — the band's *as a whole* identity is "the focused session's activity," so
   the focused-session colour applied to the sparkline could telegraph whose
   activity, with no explicit name needed.

7. **Empty state.** What does `(no recent activity)` become with a sparkline?
   A flat `▁▁▁▁▁▁▁▁` + count `0/min`? Just the phrase with breathing room?
   Sparkline-suppressed?

8. **Outcome badges.** `✓` / `✗` at row-end. What about `⚠` for warnings? `…`
   for in-progress? Where do they go relative to truncated descriptions?

9. **Glyph palette additions.** Verb glyphs need vocab.ts entries. Which
   nerd-font codepoints? `nf-md-eye` or `nf-md-book-open` for read?
   `nf-md-format-list-bulleted` for list? `nf-md-magnify` for search?
   `nf-md-pencil` for edit? `nf-md-play-circle` for run?

10. **Unfocus state.** Tiers slide dimmer when the pane loses focus. Sparkline
    glyphs are not text — do they need their own slide? Verb glyphs?
    Severity-bypass rule applies to severity colours; what about verb glyphs?

## What's out of scope

- Rolodex (focused card, session listings, branch row)
- Footer keybind row
- Theme palette
- Mux protocol or log producer (the runtime decides what entries to emit)
- Performance optimisation (typical volume is ~tens of entries/minute)
