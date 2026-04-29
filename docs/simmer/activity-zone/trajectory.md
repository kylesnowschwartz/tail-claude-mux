# Simmer Trajectory — activity-zone redesign

| Iteration | hud-fidelity | four-questions | terminal-feasibility | Composite | Key Change |
|-----------|--------------|----------------|----------------------|-----------|------------|
| 0         | 7            | 8              | 5                    | 6.7       | seed       |
| 1         | 9            | 8              | 8                    | 8.3       | edge closure: sparkline contract + verb dict + hybrid source + 4-state mocks + codepoint pin |
| 2         | 9            | 8              | 9                    | 8.7       | surgical: system-tag precedence + splitOutcome bridge + column arithmetic |
| 3         | 9            | 9              | 9                    | 9.0       | stuck-vs-idle suffix + freshest gutter override + multi-thread tie-breaker |

Best candidate: iteration 3 (composite: 9.0/10)

**Result file:** `docs/simmer/activity-zone/result.md` (= iteration-3-candidate.md)

## Iteration 0 — seed (judge-only)

**Panel:** Information Designer · Terminal-UX Pragmatist · Implementation Realist (board mode)

**Cross-panel convergence:** All three lenses landed on the same shape — bones are right, edges are hand-waved.

- **hud-fidelity (7):** unanimous. Both terminal tests pass. Four data-ink offences identified independently by all three judges:
  - freshest is double-encoded (gutter `●` AND Tier-1 bold)
  - sparkline label `7/min` restates what the shape shows
  - per-row primitive count hits ceiling (4 on freshest row)
  - verb-glyph column's small-multiples discipline broken — dictionary unclosed, so column 1 sometimes carries shape and sometimes doesn't

- **four-questions (8):** designer + pragmatist 8, realist 7. Two weaknesses:
  - Pattern conflates "stuck" (recent-but-unchanging agent) with "idle" (no events) — both render flat `▁▁▁▁▁▁▁▁`
  - Exception encoded as end-of-row badge rather than gutter mass — forfeits the canonical Tufte vertical-stripe-under-load read

- **terminal-feasibility (5):** pragmatist + realist 5, designer 7→6 conceded after deliberation. Seven build-blocking gaps:
  1. sparkline contract entirely in Open Questions (data source / geometry / refresh / y-axis / empty-state / unfocus)
  2. multi-source eyebrow thrash unresolved — costs 30–50% vertical budget on `pi`+`cc` interleave
  3. only 1 of 4 states mocked (empty / multi-source / error-heavy / 30-col absent)
  4. codepoint width risks — `●`, `✓`, `✗` are EAW Ambiguous; `⚠` is Wide; none committed to vocab.ts
  5. verb-glyph classifier has no producer-side data source (logs entries lack verb tag)
  6. outcome × pre-truncation collision rule undefined
  7. unfocus behaviour deferred to Q10 (sparkline cells, verb glyphs)

**Three ASIs surfaced (complementary, not substitutive):**

1. *Realist* — pin the sparkline contract (7 commitments)
2. *Designer* — close the verb-glyph column dictionary at 5 + producer verb-tag + renderer fallback
3. *Pragmatist* — comprehensive: 4-state 30-col mocks + hybrid source rule + drop end-row badge for column-1 displacement (gutter-mass exception) + drop freshest double-encoding + drop `7/min` + unfocus rule + closed dictionary + producer verb tag + codepoint width pin

**Synthesizer's calibration anchor (forward to iteration-1 generator):** seed hand-waves critical edges. Better = four mocked states at 30 cols, resolved source-position rule (hybrid eyebrow/inline keyed on run-length), sparkline contract pinned (64s window, buckets, refresh, scaling), exception moved to gutter-displacement for vertical-stripe under load, unfocus tier-slide committed, width-deterministic glyphs.

## Iteration 1 — edge closure

**Generator action:** Closed seven of the seed's eight foundational gaps in a single coherent edit. Pinned the sparkline contract across all six dimensions (8×8s/64s window, `max(localMax,1)` scaling, dual refresh, three explicit empty-states, fixed alphabet, tier-slide unfocus). Closed the verb-glyph dictionary at exactly 5 entries (read/list/search/edit/run → nf-md-eye/list/magnify/pencil/play) with a 4-rule fallback. Resolved source-position via hybrid run-length rule (eyebrow when source-run ≥2; 3-cell `pi│` chip when interleaving). Moved exception from end-of-row badge to column-1 SEV_ERROR displacement (gutter-mass on cascade). Dropped freshest double-encoding and `7/min` suffix. Pinned codepoint widths (retired EAW-Ambiguous `●`/`✓`/`✗` and EAW-Wide `⚠`; committed six MD-PUA codepoints). Committed unfocus tier-slide. Added four 30-col ASCII mocks (empty/single-source/multi-source/error-heavy).

**Phase 1 panel:** Information Designer 9/9/9 · Terminal-UX Pragmatist 9/9/8 · Implementation Realist 8/8/8.

**Phase 2 deliberation outcomes:**
- Information Designer conceded terminal-feasibility 9→8 after Realist surfaced three concrete tomorrow-build stalls (system-tag chip precedence, splitOutcome bridge, off-by-2 columns) that her lens had under-weighted.
- Terminal-UX Pragmatist conceded four-questions 9→8 after Realist surfaced the stuck-vs-idle Pattern conflation she'd missed (post-burst decay vs wedged agent both render flat).
- Implementation Realist held 8/8/8 across the board.

**Stable wins (preserved across both iterations):** sparkline + gutter + small-multiples + demoted source. Single-source steady mockup. Demoting source out of description column.

**Remaining gaps (ASI for iteration-2):** three single-section edits to convert spec from 'buildable with three known stalls' to 'buildable straight through' — (a) system-tag precedence rule for `[bell]`/`[event]` ahead of chip/eyebrow branching, (b) splitOutcome bridge note so legacy `(failed)` trailers don't dual-mark with column-1 SEV_ERROR, (c) fix off-by-2 column arithmetic in 30-col mocks (28-cell inner content). Optional 9→10 tightening: drop freshest-row `●` gutter when SEV_ERROR occupies column 1.

**Deferred (likely iteration-3 or user design call):** same-agent multi-thread chip collision (`pi 15c8` + `pi 9a01` → `pi│ pi│`) — the fix forks across multiple design choices, each with trade-offs.

## Iteration 2 — surgical refinement

**Generator action:** Three localised single-section edits (~25 added lines, no scope expansion). Prepended a Rule 0 to §Source position so `[bell]`-style system tags branch to a 1-cell tone-coloured glyph in column 1 *before* the run-length-keyed eyebrow/chip evaluation. Added a splitOutcome bridge note inside §Verb-glyph column rule (a) so `tone:"error"` rows strip the legacy `(failed)` suffix from the displayed description (no double-marking with column-1 SEV_ERROR). Annotated §States with column arithmetic clarification: 30-col outer = pad-1 + 28-cell inner + pad-1.

**Phase 1 panel:** Information Designer 9/9/9 · Terminal-UX Pragmatist 9/8/9 · Implementation Realist 8/8/9.

**Phase 2 deliberation outcomes (three-way convergence):**
- Information Designer conceded four-questions 9→8 — Pragmatist's stuck-vs-idle catch is a real four-questions failure (touches both Liveness and Pattern in the window-empty sub-case), not just a soft demerit.
- Terminal-UX Pragmatist held all three — Designer's Pattern=Pass argument addresses active-window cases but doesn't resolve the window-empty conflation; Realist's hud-fidelity=8 anchor was iter-1 continuity, but the column-arithmetic edit IS a design-surface change.
- Implementation Realist conceded hud-fidelity 8→9 — once the column arithmetic is verified against `fullDescWidth()` in `apps/tui/src/index.tsx:283`, it ceases to be polish and becomes a measurable contract.

**All three converged at 9/8/9 = 8.7.**

**Stable wins (preserved across all three iterations):** sparkline + gutter + small-multiples + demoted source. Single-source steady mockup. Demoting source out of description column. Sparkline contract (8×8s/64s window, max(localMax,1) scaling, three empty-states). Closed verb-glyph dictionary at 5 entries. Hybrid eyebrow/chip source rule. Six MD-PUA codepoints. Unfocus tier-slide.

**Remaining gap (iteration-3 ASI):** stuck-vs-idle Pattern conflation in the window-empty sub-case — both 'wedged agent' and 'finished agent' currently render flat `▁▁▁▁▁▁▁▁`. Add a minimal age-of-newest-entry annotation (e.g. ` ·Nm` dim suffix on the sparkline) to disambiguate. Plus optional secondary tightening (drop gutter on SEV_ERROR rows, resolve same-agent multi-thread chip via eyebrow fall-through).

## Iteration 3 — last-question fix

**Generator action:** One primary surgical edit (10 lines) plus two optional secondary fixes (~5 lines each, both clean to apply). Added a Tier-3 dim ` ·Nm` suffix to §Sparkline contract case (ii) (window-empty sub-case): when `logs[]` is non-empty but the 64s window is empty, render `▁▁▁▁▁▁▁▁ ·2m` (newest event 2 minutes ago = wedged) vs `▁▁▁▁▁▁▁▁` alone (no events at all = idle). Updated §States empty-state mock to show both sub-cases at 30 cols. Added a one-line gutter override in §Verb-glyph column rule (a) so the freshest `●` is suppressed when SEV_ERROR occupies col 1 (drops the freshest+failed double-encoding). Added a same-agent multi-thread tie-breaker to §Source position: when two consecutive rows have distinct sources but matching agentcode prefix, fall through to eyebrow mode for those rows.

**Phase 1 panel:** Information Designer 9/10/9 · Terminal-UX Pragmatist 9/9/9 · Implementation Realist 9/9/9.

**Phase 2 deliberation outcomes:**
- Information Designer conceded four-questions 10→9 — Realist's 'magnitude of N reads, binary disambiguation glances' framing convinced her that 10 requires the magnitude to also glance (which a relative-time string doesn't).
- Terminal-UX Pragmatist UPGRADED four-questions 9→10 after seeing Designer's phase-1 rationale on the closure of the last watcher-question gap.
- Implementation Realist held 9/9/9.

**Median consensus: 9/9/9 = 9.0.**

**Stable wins (all four iterations):** sparkline + gutter + small-multiples + demoted source. Single-source steady mockup. Source out of description column. Sparkline contract. Closed verb-glyph dictionary. Hybrid eyebrow/chip source rule. Six MD-PUA codepoints. Unfocus tier-slide. System-tag precedence. splitOutcome bridge. Column arithmetic. Stuck-vs-idle disambiguation. Freshest-gutter override. Multi-thread tie-breaker.

**Remaining gap (deferred to implementation):**
- chip-mode `│` per-row separator is structural — would require chip geometry redesign
- `·Nm` magnitude reads rather than glances — inherent to relative-time text
- Final +1 across all axes requires a green-tick smoke-test implementation against the four-state mocks — iteration-into-code, not spec

## Conclusion

Three iterations took the spec from 6.7 to 9.0 (+2.3 composite). All foundational seed gaps closed; spec is buildable straight through. Recommended next step: implement against `result.md`.
