# Reviewer's guide

Welcome. This folder contains a complete design pass for the
opensessions side panel and tmux statusline redesign. Implementation
hasn't started yet — we deliberately paused for a second pair of eyes.

This guide is for you. It is not a doc to read end-to-end; it tells you
where to look depending on how much time you have, what kinds of
feedback are most useful, and what's already locked vs. still open.

---

## 60-second elevator pitch

The opensessions side panel and statusline are being redesigned in a
single pass. The current panel has solid bones (rolodex with focused
pin, 5-state agent vocabulary, theme-aware palette) but suffers from
glyph reuse (`·` has 4 roles, `●` has 3), wrap-row catastrophes (long
permission prompts truncate invisibly), and ad-hoc text colour usage
(6 weights, no rules). The redesign:

- Adopts **Material Design Icons (`nf-md-*`)** as the default Nerd Font
  family. Nerd Fonts become a hard requirement (not a fallback layer).
- Restructures the panel into **four zones**: header, rolodex,
  activity, footer. Activity moves out of the focused card and into a
  permanently reserved band at the bottom — always visible, no
  animations; events populate the zone's content as they arrive.
- Locks a **two-gutter grammar** for every row: severity glyph at
  column 0, identity glyph at the last column. Position is meaning.
- Collapses the text colour usage to **four tiers** (Primary bold /
  Secondary default / Dim faint / Muted colour-shift) — replaces the
  current 6-token sprawl.
- Promotes the **Clawd glyph** (vendored in `fonts/Clawd.ttf`) into
  three positions: header (brand mark), tmux statusline (window-
  presence), panel right gutter (per-agent identity).

The canonical mockup at `04-mockups/02-canonical.md` shows the locked
design at four rendered states (quiet / live / errored / pane-unfocused)
using the real 5-session dataset captured from a live screenshot.

---

## Status (live)

Implementation has started — Stages 1 and 2 are landed (vocab + tiers
modules; severity glyph swap; mock flag with canonical scenarios;
header counter retirement; branch glyph + numeric count + B5
conditional severity; chevron wrap rules). The design docs in this
folder remain the spec; one decision was revised after live QA:

- **2026-04-28 — rolodex layout pivot.** The wheel/rotation model was
  retired in favour of a *linear tape* (sessions in natural order;
  focused card pinned vertically centred; viewport slides over the
  tape; `j`/`k` wraps modularly). The wheel disoriented users in
  practice. See `04-mockups/02-canonical.md` locked decision #6 and the
  dated update note at the top of that file.

Other locked decisions are unchanged.

---

## How long do you have?

### 5 minutes

Read just two things, in order:

1. **`04-mockups/02-canonical.md`** § "Canonical state — quiet" and
   § "Canonical state — live" — these are the punchline. Two ASCII
   panels showing the new design.
2. **`README.md`** § "Resolved stances" — three short paragraphs that
   summarise the locked direction.

After that, give us a gut-feel response. Tasteful or not? Anything
glaringly wrong? Don't get into details unless you have more time —
the gut response on the visuals is the most valuable.

### 20 minutes

Add to the above:

3. **`04-mockups/00-baseline.md`** § "ASCII transcription" and
   § "Friction inventory" — the *before* state with seven concrete
   problems. Useful to verify our problem statement matches reality.
4. **`03-vocabulary.md`** § 2 (severity), § 3 (identity), § 4 (text
   tiers), § 5 (structural) — the actual glyph table and tier rules.
   This is where most of the design currency lives.
5. **`02-zones.md`** § 1 (zones) and § 4 (activity zone) — the new
   structural commitment.

At this depth you can push back on specific glyph choices, the auto-
show timing, the identity-gutter format (`2π` vs. `2`), etc.

### 60 minutes

Add the foundational doc:

6. **`00-grounding.md`** in full — disciplines, references, and the
   resolved style stance. This is the *why* layer. Useful if you want
   to challenge a specific design choice as inconsistent with the
   stated principles.
7. **`01-audit.md`** § 2 (glyph & colour collision matrix) and § 3
   (information hierarchy) — the analytical foundation that drove the
   redesign's priorities.

At this depth you're qualified to challenge the entire stance, not
just the pixels. Useful if you think we're over- or under-engineering
a particular surface.

---

## High-stakes decisions to push back on

These are the choices most worth a second opinion. They were each made
through ask_user prompts during the design pass, but a fresh reviewer
might catch things the original decision-maker missed.

| Decision | Where it lives | Push back if… |
|---|---|---|
| **Activity zone permanently reserved (always visible)** | `04-mockups/02-canonical.md` §"Activity zone behaviour"; `02-zones.md` §4 | …you think the always-visible band wastes vertical real estate that should belong to the rolodex. |
| **Material Design Icons as the default family** | `00-grounding.md` §3; `03-vocabulary.md` §1 | …you'd anchor on Codicons or stay with geometric Unicode for portability. (Note: portability constraint was *deliberately* dropped — Nerd Fonts are now a hard requirement.) |
| **Clawd in three positions** (header + statusline + panel right gutter) | `03-vocabulary.md` §6 | …you think this overuses the brand mark and dilutes its meaning. |
| **Numeric-only count format** (`2`, `3`, `9+`) | `04-mockups/02-canonical.md` §"Locked decisions" Q3; `03-vocabulary.md` §3 "Numeric agent count formatting" | …you'd prefer the original `2π` (digit + glyph) form for richer identity at the count position. |
| **Ports feature removed entirely** | `03-vocabulary.md` §8 retired list; `04-mockups/02-canonical.md` §"Locked decisions" | …you think detected localhost ports are a feature worth keeping. |
| **4-tier text hierarchy replacing 6 tokens** | `03-vocabulary.md` §4 | …you suspect 4 tiers is too few to express the actual visual hierarchy needed in a multi-section panel. |
| **Identity gutter stays Tier 3 Dim even on errored agents** | `04-mockups/02-canonical.md` §"Canonical state — errored" | …you want the right gutter to also flip to severity colour on errors (HUD redundancy) instead of staying quiet. |
| **Italic as a sanctioned exception** for activity descriptions only | `03-vocabulary.md` §4 "Italic as a sanctioned modifier" | …you think italic should either be unrestricted (a fifth axis) or banned entirely. |

---

## What's NOT in scope (don't review against these)

These were considered and **deliberately deferred** to a later phase:

- **Per-tool-category activity icons** (pen-nib for edit, wrench for
  bash, magnifying glass for grep). Locked decision: single constant
  chevron leader for v1; per-category in a follow-up.
- **Configurable detail levels per session** (persisted detail
  preferences). Locked: not in v1; activity zone visibility is the
  same for every session.
- **Status-aware colour for the tmux statusline glyph**: the redesign
  promotes this from `docs/specs/tmux-header.md`'s future-work list to
  v1 of the new design. The implementer checklist for
  `tmux-header-sync.ts` is in `03-vocabulary.md` §6; spec update is
  pending and tracked in `05-spec.md`.
- **Light-mode palette discipline.** The new vocabulary uses
  Catppuccin tokens which adapt across themes, but specific
  light-theme A/B testing hasn't been done. Latte and Day variants are
  in the theme list but not visually verified against the new design.
- **Accessibility (colour-blind support).** EFIS severity colours
  (green/yellow/red/blue/grey) are already conventional, but
  redundant-coding for colour-blind users (different shapes per
  state, not just colour) is implicit in the design — every state has
  its own glyph — but no explicit testing has been done.
- **Animation curves.** None: the design has no animations. The
  activity zone is permanently reserved, so there is no fade-in or
  fade-out. The freshest activity entry steps from Tier 2 italic to
  Tier 3 italic when a newer entry pushes it down — a single attribute
  change, not animation.

---

## What kinds of feedback help most

In rough order of usefulness:

1. **"This will not work because [specific real-world workflow]."**
   Concrete failure modes are the most valuable.
2. **"This violates [stated principle X] because [Y]."** Internal
   inconsistencies — a design choice that contradicts the doc's own
   stated principles. We can either revise the choice or revise the
   principle, but we can't ship both as-is.
3. **"This glyph / colour / weight is wrong for [reason]."**
   Aesthetic feedback grounded in why.
4. **"You missed [related project / paper / pattern]."** Prior art we
   should have referenced but didn't.
5. **"I would have answered [decision Q] differently."** Useful
   second opinion on the locked decisions, even if it doesn't change
   the outcome — knowing where reasonable disagreement lives is
   valuable.

Less useful (but not unwelcome):

- "This is too dense / too sparse." Without specifics it's hard to act
  on; with specifics (which row, what density measure) it becomes
  type 3 above.
- "I would have done it differently." Without articulating the
  alternative.
- Strong language about colour preferences without a tier-system
  argument.

---

## How decisions get re-opened after this review

If you flag a high-stakes decision, here's what happens:

1. The reviewer's note lands in this folder as `REVIEW-NOTES.md`.
2. The author drafts a response: either accept the change (revise the
   relevant doc) or articulate why the existing decision stands.
3. If the reviewer doesn't accept the response, the decision goes
   back to the original decision-maker (via ask_user) for a second
   pass with the reviewer's argument as input.

This is the same mechanism that produced the locked decisions. It
scales to N reviewers — each round narrows uncertainty.

---

## What happens after review

When this review concludes (either "approved as-is" or "approved with
revisions applied"), implementation proceeds in two phases:

1. **Refactor pass** — extract the current `apps/tui/src/index.tsx`
   (1670 lines, single file) into per-component files. No behaviour
   change. Reviewable as a single PR-shaped diff.
2. **Implementation stages 1–5** — see `04-mockups/02-canonical.md`
   §"What's needed to promote to live OpenTUI". Each stage is its
   own PR:
   - Stage 1: `--mock` flag + vocabulary swap + 4-tier helpers
   - Stage 2: two-gutter grammar + chevron wrap rules
   - Stage 3: always-visible activity zone (rendering against the
     existing `metadata.logs` buffer)
   - Stage 4: ports feature deletion + production promotion (remove
     the `--mock` gate)
   - Stage 5: statusline propagation per `03-vocabulary.md` §6
     implementer checklist

Each stage is independently revertable. The design docs in this
folder are the spec.

---

## Quick links

- The locked design: [`04-mockups/02-canonical.md`](./04-mockups/02-canonical.md)
- The visual baseline (faithful "before"): [`04-mockups/00-baseline.md`](./04-mockups/00-baseline.md)
- The glyph + tier table: [`03-vocabulary.md`](./03-vocabulary.md)
- The zone layout: [`02-zones.md`](./02-zones.md)
- The grounding: [`00-grounding.md`](./00-grounding.md)
- The audit: [`01-audit.md`](./01-audit.md)
