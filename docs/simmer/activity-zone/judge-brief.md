# Judge brief — activity-zone simmer

This brief is shared across all panelists and all iterations. Read it once at
the start of phase 1.

## Artifact

- **Type:** single-file (markdown design spec)
- **Path:** `docs/simmer/activity-zone/iteration-N-candidate.md` (substitute the
  iteration the orchestrator gave you)
- **Domain:** activity-zone redesign for the tcm tmux-sidebar TUI

## Criteria (rubric)

### hud-fidelity *(PRIMARY)*

A 10/10 design passes **both** terminal tests:

- **Spellcheck test** — a marker appears *on the line of work itself*, not in a
  popup, dialog, side pane, or separate command.
- **Sparkline test** — the signal renders with `▁▂▃▄▅▆▇█` (or a similar fixed
  glyph alphabet) in ≤8 cells *inside* an existing line; if sparklines stack,
  they share a labelled baseline.

A 10/10 design uses ≤3 primitives per row from the palette (sparkline, gutter
cell, status-line field, dim/bold/inverse run, badge glyph, color-band column,
box-drawing frame, prompt-line indicator, small-multiples). Excessive chrome
(rules, padding, decorative glyphs without function) is penalised. Italic on
monospace is penalised. Re-stating information already shown elsewhere in the
panel is penalised. Embodies *see-through, not at*.

8–9/10: passes both tests, ≤3 primitives per row, but one or two micro-typography
or chrome decisions could be tighter.
6–7/10: passes one test, fails the other; or 4 primitives somewhere.
4–5/10: design relies on prose-reading; minimal HUD primitives.
1–3/10: pure copilot reflex, dialog/dashboard rather than HUD.

### four-questions

A 10/10 design lets a **glance** (not a read) answer all four watcher
questions:

1. **Liveness** — *is something happening at all?*
2. **Pattern** — *cycling, converging, stuck?*
3. **Exception** — *did anything go wrong?*
4. **Latest** — *what's the freshest concrete action?*

8–9/10: 4-of-4, with one question slightly weaker than the others.
6–7/10: 3-of-4 perceptible by glance; the missing one requires reading.
4–5/10: 2-of-4 (typically liveness + latest); the other two require reading.
1–3/10: 1-of-4 (latest only) — current shipped state.

### terminal-feasibility

A 10/10 design is implementable in OpenTUI with the existing `vocab.ts` (or
extends it by ≤6 new nerd-font glyphs, with codepoint and rationale per glyph),
works at sidebar widths down to ~30 cols, and degrades cleanly across the four
states:

- **Empty** — no recent activity
- **Single-source** — one agent in a session (most common)
- **Multi-source** — two or more agents interleaving
- **Error-heavy** — multiple failures in the visible window

8–9/10: implementable with minor open questions about edge cases.
5–7/10: implementable but requires 7+ new glyphs, or hand-waves a state, or
leans on an OpenTUI feature not exercised elsewhere in the codebase.
3–4/10: would need a renderer feature we don't have (image cells, custom
fonts, etc.) or breaks under one of the four states.
0–2/10: unbuildable in OpenTUI without a major capability addition.

## What the generator can change

**This is single-file mode.** The generator's job is to **refine the design
spec markdown file** (`docs/simmer/activity-zone/iteration-N-candidate.md`).

The generator **cannot**:
- edit `apps/tui/src/index.tsx`, `vocab.ts`, `tiers.ts`, or any source code
- modify the rolodex, footer keybinds, theme palette, mux protocol, log
  producer
- add new repo files outside `docs/simmer/activity-zone/`
- introduce design constraints not satisfiable by OpenTUI + nerd-fonts

The generator **can**:
- add or remove proposed primitives (sparklines, gutters, glyphs, badges)
- propose new vocab.ts entries (codepoint + rationale, ≤6 total)
- restructure the spec, add/remove sections, sketch alternatives
- add ASCII mockups for new states (empty / multi-source / error-heavy / narrow)
- explicitly resolve open questions and remove them from the "Open" list

## Background

- tcm is a tmux-sidebar TUI for monitoring multi-agent sessions.
- The activity zone shows `metadata.logs` for the focused session.
- Each entry: `{ message: string; ts: number; source?: string; tone?: ... }`.
- Sources: `pi xxxx` / `cc xxxx` (7 chars, `agentCode + " " + 4-char threadId`)
  or system tags like `[bell]`. Many entries have no source.
- Cap: 5–7 entries depending on terminal height.
- Existing primitives (`vocab.ts`): severity glyphs (working spinner, waiting,
  ready, stopped, error), identity glyph (Clawd), structural glyphs (branch,
  dir-mismatch, chevrons, arrow-right).
- 4-tier text hierarchy (`tiers.ts`): primary (bold), secondary, dim, muted —
  slides one step dimmer when the panel pane is unfocused.
- Renderer: OpenTUI + solid-js, flex via yoga. `<text truncate>` unreliable at
  edges; pre-truncate in JS is the proven path.
- Severity colours and identity glyphs bypass tiers (always full intensity).
- The shipped implementation (commit `872a621`): heading dropped, source column
  dropped, source as inline Tier-4 prefix on change, no continuation indent,
  descriptions full width, italic dropped, weight-only emphasis.

## Search space

Variants must:
- compose primitives from the HUD-first palette (above)
- introduce ≤6 new nerd-font glyphs (codepoint + rationale per glyph)
- work at sidebar widths down to ~30 cols
- handle the four states cleanly

Layout decisions are **open to revision**:
- heading (dropped in current; could come back if it earns its row)
- top/bottom separator rules
- time / recency column
- source position (inline-prefix, eyebrow, column, watermark, gone)
- per-row gutters and badges
- summary rows (sparkline, count, etc.)

Out of scope (do not propose changes here):
- rolodex (focused card, session listings)
- footer keybind row
- theme palette
- mux protocol or log producer
- performance
