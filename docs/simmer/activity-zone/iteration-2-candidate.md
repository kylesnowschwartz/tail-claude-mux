# Activity Zone Redesign — Design Spec

> **Iteration 2.** Surgical refinement of iteration-1. The bones, edges, and
> mocks are preserved verbatim; this iteration adds three buildability fixes
> at the spec edges (system-tag precedence, splitOutcome bridge,
> column-arithmetic annotation) plus one deferred future-work item. No new
> glyphs, no new sections, no scope expansion.

## Context

The "activity zone" is a fixed-height structural band beneath the rolodex in the
tcm tmux sidebar. It surfaces `metadata.logs` for the focused session — a stream
of derived events emitted by the runtime's agent watchers (Claude Code, pi,
codex, amp).

The user looks at it to know *"what is the agent doing?"* — without it, they
must context-switch to the agent's pane to read tool output, breaking flow.
The watcher's four questions (liveness / pattern / exception / latest) must be
answerable by **glance**, not read.

## Constraints

**Spatial.** Sidebar pane, typical width 30–60 cols. Cap of 5–7 entries
depending on terminal height. Lives below the rolodex (which ends with a `▼`
wrap chevron) and above the footer keybind row. **All mockups in this iteration
are drawn at 30 cols** — the tightest realistic width — so the design proves it
degrades there before judging wider widths.

**Source data.** Each entry is

```ts
{ message: string; ts: number; source?: string; tone?: "neutral" | "info" | "success" | "warn" | "error"; verb?: "read" | "list" | "search" | "edit" | "run" }
```

The `verb?` tag is **new** — see §Verb-glyph column for the producer-side
recommendation and the renderer-side fallback when `verb?` is absent.

Sources are either `pi xxxx` / `cc xxxx` (7 chars, `agentCode + " " + 4-char threadId suffix`)
or system tags like `[bell]`. Many entries have no source. The runtime
debounces emissions and emits one entry per *changed* signal (toolDescription,
threadName, status transition).

**Existing primitives — `vocab.ts`:**

- Severity glyphs: brail spinner (working), `nf-md-bell-alert` (waiting),
  `nf-md-check-circle-outline` (ready), `nf-md-stop-circle` (stopped),
  `nf-md-alert-circle` (error)
- Identity: vendored Clawd glyph for Claude-Code threads
- Structural: `nf-md-source-branch`, `nf-md-folder-question-outline`,
  `nf-md-chevron-right` / `nf-md-arrow-right`,
  `nf-md-chevron-up` / `nf-md-chevron-down`
- All `nf-md-*` glyphs are PUA (U+F0000–U+F1AFF), guaranteed single column-cell,
  East Asian Width Neutral. New glyphs go through `vocab.ts`.

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

Source label appears inline, Tier 4 muted, with ` · ` separator between
source and description. Renders only on rows where source differs from the
row above, *or* on the first visible row (anchor).

Continuation rows have **no indent** — they start at the left margin. The
ragged left at source-change rows is intentional: it visually tags the change.

Description weight: Tier 1 (bold) for freshest, Tier 3 (dim) for older. Italic
dropped.

Description is pre-truncated with `…` to avoid wrap orphans.

Empty state: `(no recent activity)` in Tier 4 muted, with breathing-room above
and below.

## HUD-first evaluation of current state

The user reads the rows as prose. Eye scans verb→object pairs.

| Question | Current state | Verdict |
|---|---|---|
| Liveness — *is something happening?* | Unrepresented. No rate signal. | Fail |
| Pattern — *cycling, converging, stuck?* | Rows show events, not shape. | Fail |
| Exception — *did anything go wrong?* | Tone-coloured but no peripheral hit. | Fail |
| Latest — *what's the freshest action?* | Bold-weight freshest row. | Pass |

**1-of-4.** The one we answer is the copilot's question. The three we miss are
the HUD's questions.

**Spellcheck test:** within current constraints, signals are on the lines they
describe (good); no `!`-style gutter on failed rows (could be).

**Sparkline test:** fails — no sparkline.

## Proposed direction

Three primitives layered, one per band of the panel:

1. **Sparkline (top row)** — answers liveness + pattern by peripheral glance.
2. **Gutter cell + verb-glyph column** (left of every activity row) — answers
   latest (gutter on freshest) and exception (severity glyph displaces verb on
   failure, accumulating as a vertical stripe under load).
3. **Source position** (eyebrow OR inline chip, hybrid) — keeps source out of
   the description column without thrashing under multi-source interleave.

Per row, the primitive count is **at most 3**:
`gutter + verb-glyph + description`. Source is not per-row in eyebrow mode;
in chip mode it replaces the gutter+verb column-block. Tone is colour, not a
counted primitive.

### Sparkline contract

The sparkline answers liveness ("is anything happening?") and pattern ("burst,
flat, converging?") with a single fixed-glyph row. All six dimensions are
pinned here:

| Dimension | Commitment |
|---|---|
| **Geometry** | 8 cells × 8 s/bucket = **64 s window**. Bucket `i` (0 = oldest, 7 = newest) counts log entries with `now - ts ∈ [(7-i)·8s, (8-i)·8s)`. |
| **Alphabet** | Fixed `▁▂▃▄▅▆▇█` (U+2581–U+2588). Zero renders as `▁` (visible flat baseline), not blank — calm reads as a continuous line, not as absence of the band. |
| **Y-axis scaling** | Auto-rescale per render: `max = Math.max(localMax, 1)`. Each cell maps `count / max` to the 8-step alphabet via `Math.ceil(7 * count / max)`. The `,1)` floor prevents division-by-zero in the all-zero case and keeps a single event from saturating the line. |
| **Refresh** | Recomputed on (a) `logs[]` mutation and (b) a 1 Hz tick driven by the existing focused-session timer. The 1 Hz tick is what makes the right-edge cell "fall off" smoothly as time passes without new events. |
| **Empty states** | Three cases. **(i) no-logs** — `logs.length === 0`: sparkline row suppressed entirely; the band collapses to the `(no recent activity)` message. **(ii) window-empty** — `logs.length > 0` but no entries within 64 s: render flat `▁▁▁▁▁▁▁▁`. The shipped (now stale) entries continue below in Tier 3. **(iii) active** — at least one entry within 64 s: normal sparkline. |
| **Unfocus** | Sparkline cells **slide one tier dimmer** with the rest of the panel (text → subtext0). They are not severity-bypass; they are status-of-display, not status-of-agent. See §Unfocus rule. |

The sparkline carries **no text label**. The shape carries the rate; a `7/min`
suffix would be the panel re-stating itself, the canonical Tufte data-ink
offence. (This is the seed's main hud-fidelity demerit, here closed.)

The sparkline owns the focused-session colour band — shape *and* whose-shape
in one primitive. (See Q1 in Open questions for the residual heading-absorption
case.)

### Verb-glyph column

Column 1 is a **small-multiples instrument**: the same shape in the same
column on every row, so the eye reads verb-rate at a glance ("five reads, then
a search" reads as one shape). For this to work, the column must be
**guaranteed-populated by a closed dictionary** — an open dictionary breaks
small-multiples discipline (one of the seed's hud-fidelity demerits).

**Dictionary — closed at exactly 5 entries:**

| Verb | Glyph | Codepoint | Rationale |
|---|---|---|---|
| `read` | `nf-md-eye` | U+F0208 | "look at" — semantically tighter than book-open for tool reads |
| `list` | `nf-md-format-list-bulleted` | U+F0279 | universal list affordance |
| `search` | `nf-md-magnify` | U+F0349 | universal search affordance |
| `edit` | `nf-md-pencil` | U+F03EB | universal edit affordance |
| `run` | `nf-md-play` | U+F040A | universal play / execute affordance |

**Producer-side recommendation (SHOULD; out of scope for this artifact):**
the runtime tags emitted log entries with `verb?: "read" | "list" | "search" |
"edit" | "run"` derived from the agent's tool name. This is a one-line addition
to the existing watcher classifier (it already discriminates toolDescription
shape).

**Renderer-side fallback (until producer ships `verb?`):**

The renderer applies a **single explicit precedence** when computing column 1:

1. If `tone === "error"` → render `SEV_ERROR` (existing severity glyph,
   bypasses tiers — see §Source position for the gutter-mass behaviour).
   **Bridge to legacy `splitOutcome()`:** the shipped renderer detects a
   trailing `(passed)`/`(failed)` marker on the message via `splitOutcome()`
   (regex `/^(.*?)(\s*)(\((passed|failed)\))\s*$/`, see
   `apps/tui/src/index.tsx:210`) and renders it as a tone-coloured suffix.
   When `tone === "error"` AND `splitOutcome(message).outcome?.tone ===
   "error"`, **strip the `(failed)` suffix** from the displayed description
   and rely solely on the column-1 `SEV_ERROR` displacement — failed rows
   must not be double-marked. For `(passed)` outcomes, keep the existing
   tone-coloured suffix render (no displacement); success is the unmarked
   default and the suffix is its sole carrier.
2. Else if `verb` is one of the 5 keys → render the dictionary glyph.
3. Else if `verb` is absent and a regex-classifier matches the message
   (`/^Reading /` → read, `/^Listing /` → list, etc.) → render the dictionary
   glyph. The classifier is best-effort and lives in `vocab.ts`-adjacent
   `classify.ts`; misclassifications degrade to rule 4.
4. Else → **single space**. The column is reserved but blank.

Rule 4 is a deliberate design choice, not a fallback embarrassment. Gaps in
the verb-glyph stripe are themselves informative: they read as
"non-tool event" (status transitions, `[bell]`, agent-internal moments).
Small-multiples discipline survives because the *column itself* never moves —
only its content varies. The eye still reads "five-glyph stripe, one gap, two
more glyphs" as a coherent shape.

### Source position

The renderer applies **precedence-ordered rules** to compute the source rendering for each row. The first matching rule wins.

**Rule 0 — system tags (highest precedence).** When `entry.source?.startsWith("[")` (e.g. `[bell]`, `[event:status]`), the source is a *system tag*, not an agent threadId. It renders as a **single 1-cell tone-coloured glyph at column 1** of the row — never as an eyebrow row, never as a 3-char chip. The glyph is selected from `vocab.ts` keyed on the tag literal (`[bell]` → `nf-md-bell-alert`, etc., **reusing existing severity-glyph entries** — no new glyph budget). The tag's `tone` (defaulting to `info`) drives the colour, bypassing tiers like other severity glyphs. The description renders from column 3+ as usual. System tags do **not** participate in the agent-source run-length stream below — a `[bell]` between two `pi db92` rows does not break the run.

This rule branches **before** the run-length-keyed eyebrow/chip evaluation. Without precedence, a single `[bell]` would either trigger a fresh eyebrow (wasting a row on `[bell]`) or produce the nonsense chip `[b│ ...`. Branching first also keeps the verb-glyph column dictionary closed: system-tag glyphs displace col 1 just like `SEV_ERROR` does, by the same severity-bypass mechanism.

**Rules 1–2 — agent sources, run-length keyed.** The seed used eyebrow-only, which thrashes under multi-source interleave (every row triggers a fresh eyebrow, eating 30–50% of the vertical budget). Iteration 1 commits a **hybrid rule** keyed on run-length:

```
let run = currentSourceRunLength(logs, i);
if (run >= 2)  → eyebrow mode for this run
if (run == 1)  → inline chip mode for this row
```

**Eyebrow mode** (source-run ≥ 2 rows):

```
 pi db92                  ← eyebrow row, Tier 4 muted, no separator
 g v desc                 ← g=gutter, v=verb glyph
 g v desc                 ← continuation rows in same source-run
```

The eyebrow costs one row per source-run. Worth it because the source-run
covers ≥ 2 description rows under it.

**Inline chip mode** (source-run = 1, i.e. interleaving):

```
pi│ v desc                ← chip occupies cols 0–2, then pad+verb+pad+desc
cc│ v desc
pi│ v desc
cc│ v desc
```

The chip is **3 cells** at column 0: agent-code (2 chars: `pi` / `cc` /
`am` / `cx`) + box-drawing `│` (U+2502, EAW Neutral, single cell). Threadid
is dropped in chip mode — the agent code alone is enough to read interleave.
The chip occupies the same columns the gutter+verb would have used, so chip
mode **forfeits the freshest gutter** for that row. Latest is recovered by
position (top of list) and by the freshest-only Tier-2 secondary on the
description (vs Tier 3 dim for older rows).

In chip mode, verb glyph stays — it's the small-multiples instrument and
deserves the column. So chip mode layout is:

```
col 0: chip char 1 (agent code)
col 1: chip char 2 (agent code)
col 2: │ (chip separator)
col 3: pad
col 4: verb glyph (or SEV_ERROR on failure)
col 5: pad
col 6+: description (≤24 cells at 30-col panel)
```

**The hybrid rule fires per source-run, not per row.** A 4-row run of `pi
db92` followed by a 1-row blip of `cc 1859` followed by another 4-row `pi`
run renders as eyebrow / chip / eyebrow, not all-chip. The eye sees structure,
not noise.

### Unfocus rule

Pane focus already slides text tiers one step dimmer (text → subtext0,
overlay0 → surface2). This iteration commits the rule for the new primitives:

| Primitive | Unfocus behaviour |
|---|---|
| Sparkline cells | **Slide.** They are status-of-display (a rendering of agent activity), not status-of-agent. When the user isn't looking, they should recede with the rest of the band. |
| Gutter `●` (freshest) | **Slide.** Same reasoning — it's a salience marker, not a severity signal. |
| Verb glyphs (read/list/search/edit/run) | **Slide.** Pure UI affordance, not severity. |
| Source eyebrow / chip | **Slide.** Already Tier 4; the existing mirror handles it. |
| `SEV_ERROR` displacement (failed rows) | **Bypass.** Severity colours always render at full intensity, by §4 of the existing vocab rule. An exception is an exception whether or not you're looking. |

This makes the unfocus state read as one coherent dim band with bright red
spots where errors happened — the canonical HUD-at-rest pattern.

## States

Four mockups at **exactly 30 cols** (ruler at top of the first one for
reference). Each shows the focused rendering; the unfocused rendering is the
same content with every tier slid one step dimmer except `SEV_ERROR`.

**Column arithmetic.** The 30-col ruler counts the **full panel pane width**,
padding included. The shipped layout is `pad(1) | content(28) | pad(1)`, so
the inner content area is **28 cells**. In the mocks below the leading space
on most rows literally **is** the left `pad(1)`, and the unused right tail
of each row literally **is** the right `pad(1)` plus any unused content
cells. Where a freshest-row gutter (`●`) or chip head (`pi│`) appears in
column 0, it displaces the conceptual left pad for that row — this is
intentional and matches the shipped renderer's gutter/chip behaviour. Read
the ruler as panel-outer width, not content-inner width.

Verb-glyph placeholders in mocks: `r`=read, `l`=list, `s`=search, `e`=edit,
`R`=run. Real renderer uses the codepoints from §Verb-glyph column. Gutter
freshest mark is shown as `●` for legibility; real renderer uses `nf-md-record`
U+F05CB (see §Glyph palette). Failed-row displacement shown as `✗`; real
renderer uses existing `SEV_ERROR` (`nf-md-alert-circle` U+F0028).

### State 1 — empty (no-logs)

```
0         1         2         
012345678901234567890123456789

  (no recent activity)
```

Sparkline row suppressed. Air row above and below the message
(handled by the band's outer `pad(1)`).

### State 2 — single-source steady (most common)

```
 ▂▃▄▅▆▆▅▃

 pi db92
●r build.ts
 r tsconfig.json
 r package.json
 s scenarios.ts
 r tiers.ts
```

- Sparkline (8 cells, leading pad cell): a gentle hump — agent did a burst,
  is settling.
- Air row.
- Eyebrow `pi db92` Tier 4 muted (one row, source-run = 5).
- Activity rows: gutter `●` on freshest only, verb glyph in col 1, description
  in col 3+ (≤27 cells, pre-truncated with `…`).
- Description tiers: row 0 (freshest) Tier 2, rows 1–4 Tier 3 dim.

### State 3 — multi-source interleave (pi + cc)

```
 ▃▅▆▇▇▇▆▆

pi│ r build.ts
cc│ r types.ts
pi│ r scenarios.ts
cc│ e index.tsx
pi│ r vocab.ts
cc│ r tiers.ts
```

- Sparkline: sustained mid-high — both agents busy.
- No air row needed (chip mode is its own visual divider; the chip column
  reads as a 2-channel stripe).
- No eyebrow — every row's source-run is 1, so chip mode applies throughout.
- Chip column reads as `pi cc pi cc pi cc` — the eye perceives interleave as
  a vertical-stripe pattern.
- Freshest gutter forfeited; latest signalled by top-row position +
  Tier 2 description weight on row 0.
- Verb stripe still readable (`r r r e r r` — the one edit pops).

### State 4 — error-heavy

```
 ▆▆▇▆▅▄▃▂

 pi 15c8
●✗ src/index.tsx
 ✗ types.ts
 r tsconfig.json
 ✗ vocab.ts
 ✗ tiers.ts
```

- Sparkline: high then decaying — burst of activity tailing off (typical
  failure-cascade shape).
- Eyebrow `pi 15c8` (source-run = 5, eyebrow mode applies).
- Column 1 is now mostly `✗` — the failed rows have `SEV_ERROR` in the verb
  position (full red, bypasses tiers). The eye sees a vertical red stripe
  from columns 1 down: gutter-mass exception count without reading.
- Row 2 (`tsconfig.json`) survived — its verb glyph still shows. The single
  surviving glyph in a stripe of `✗` is itself diagnostic.
- Top row has **both** freshest gutter (`●`) AND error displacement (`✗`):
  cols 0 and 1 are both lit. That's the worst-case "the latest thing also
  failed" reading, which is exactly what we want salient.

### Unfocused variant (any state)

Every glyph except `SEV_ERROR` slides one tier. Sparkline goes from `text` to
`subtext0`; gutter `●` and verb glyphs go from `text` to `subtext0`; eyebrow
goes from `overlay0` to `surface2`; descriptions slide per existing rule. The
red `✗` column in state 4 stays bright — the band reads as one dimmed
landscape with bright red spots, exactly the HUD-at-rest signature.

## Glyph palette

Total **new glyphs introduced: 6** (within budget). All codepoints are in
the Material Design Icons PUA range U+F0000–U+F1AFF, which is guaranteed
single column-cell wide and EAW Neutral by the nerd-fonts patcher. The seed's
EAW-Ambiguous `●` / `✓` / `✗` and EAW-Wide `⚠` are all retired or replaced.

| Purpose | Codepoint | Name | EAW | Rationale |
|---|---|---|---|---|
| Freshest-row gutter | **U+F05CB** | `nf-md-record` | Neutral | Filled small disc — "now". Smaller visual weight than `nf-md-circle` U+F0765 so it whispers rather than shouts. Replaces seed `●` U+25CF (EAW Ambiguous). |
| Verb: read | **U+F0208** | `nf-md-eye` | Neutral | "look at" — semantically tightest fit for read-tool calls. |
| Verb: list | **U+F0279** | `nf-md-format-list-bulleted` | Neutral | Universal list/enumerate affordance. |
| Verb: search | **U+F0349** | `nf-md-magnify` | Neutral | Universal search affordance (Glob/Grep/etc). |
| Verb: edit | **U+F03EB** | `nf-md-pencil` | Neutral | Universal edit affordance (Edit/Write/MultiEdit). |
| Verb: run | **U+F040A** | `nf-md-play` | Neutral | Universal play / execute affordance (Bash/run-tests). |

**Reused (not new):** `SEV_ERROR` U+F0028 (`nf-md-alert-circle`) for the
failed-row column-1 displacement. This is the existing severity glyph; reusing
it makes the failed-row stripe consistent with how errors render elsewhere
in the UI (full red, bypasses tiers).

**Retired:** seed's row-end `✓`/`✗` badges (U+2713/U+2717, EAW Ambiguous) are
gone — exception is now column-1 displacement, success is implicit (no glyph
needed; the row finished). Seed's `⚠` (U+26A0, EAW Wide — would render
2-cell on many terminals) is retired entirely; warning tone is colour, not
glyph.

**Box-drawing in chip:** `│` (U+2502) for the chip separator. EAW Neutral,
already used by box-drawing infrastructure across the codebase.

## Open design questions

The iteration-0 list had ten items; the seed-to-iteration-1 ASI closed eight.
Two remained from iteration-1, plus one new future-work item surfaced during
iteration-2 review:

1. **Heading absorption.** The sparkline row could double as the heading
   region — the band's *as a whole* identity is "the focused session's
   activity." Applying the focused-session colour to the sparkline already
   telegraphs whose-activity. The remaining question is whether the band
   needs *any* explicit name tag (e.g. a small `nf-md-pulse` glyph at col 0
   of the sparkline row), or whether colour alone carries it. Iteration 1
   ships colour-only and listens for "I forgot what this band was" feedback.

2. **Gutter freshest only, vs gradient.** Iteration 1 commits to single-cell
   gutter on row 0 only — HUD-clean, one signal one cell. A recency-rank
   gradient (`●◐○○○`) over rows 0–4 was considered and rejected as
   double-encoding (description Tier 2 → 3 already encodes recency). The
   open question is whether under very-low-volume sessions (1–2 events per
   minute) the gradient would help signal "these are all recent" vs "this
   is one fresh event among stale ones". Defer to user testing.

3. **Same-agent multi-thread chip collision (deferred).** Chip mode renders
   the agent code as the chip head (`pi│`, `cc│`). When two threads of the
   *same* agent interleave — e.g. `pi 15c8` and `pi 9a01` — the chip column
   reads `pi│ pi│ pi│ pi│`, losing the per-thread distinction the chip was
   meant to carry. Possible fixes (use threadId suffix as the chip head;
   colour-tint per thread; fall back to eyebrow under same-agent multi-thread)
   each fork across multiple design choices and benefit from user testing
   first. **Flagged as future work; not addressed in iteration-2.**

## What's out of scope

- Rolodex (focused card, session listings, branch row)
- Footer keybind row
- Theme palette
- Mux protocol or log producer (the runtime decides what entries to emit;
  the `verb?` tag is recommended as a SHOULD, but the renderer ships with
  the regex-classifier fallback so this iteration is buildable today)
- Performance optimisation (typical volume is ~tens of entries/minute)
