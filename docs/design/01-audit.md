# 01 · Audit of the current side panel & statusline

This document inventories what is on screen *today*, where the friction
sits, and what needs to be resolved before any mockup work begins. It is the
baseline `02-zones.md` and `03-vocabulary.md` (and the mockups) are diffed against.

Source files audited:

- `apps/tui/src/index.tsx` — `SessionCard` (line 1352), `AgentListItem`
  (line 1217), the panel header (line 770), the panel footer (line 920)
- `packages/runtime/src/server/tmux-header-sync.ts` — agent glyphs, theme
  options
- `integrations/tmux-plugin/scripts/header.tmux` — statusline format strings
- `docs/specs/tmux-header.md` — durable spec for the statusline

---

## 1. Side-panel inventory

### 1.1 Header zone (`SessionCard.tsx:770`)

```
  Sessions  3  ⚡1  · flash msg  ✗1  ● 2
  ^^         ^   ^   ^^^^^^^^^^^   ^   ^
  │          │   │   │             │   └─ unseen count (teal)
  │          │   │   │             └───── error count (red)
  │          │   │   └─────────────────── transient flash message (dim)
  │          │   └─────────────────────── running spinner-count (yellow)
  │          └─────────────────────────── total session count (subtext0)
  └────────────────────────────────────── pane-focus indicator + bold "Sessions"
```

**Information present:** total / running / errored / unseen / transient flash.

**Friction:**
- `⚡` (running) and `●` (unseen) both appear here *and* on individual
  `SessionCard` rows. The header is summarising; the rows are detailing. The
  user cannot tell at a glance whether the header counters are
  authoritative or whether they're flickering aggregations.
- The flash message and the counters share the same horizontal lane with no
  separator. When a flash appears, the counters reflow.
- The literal word `Sessions` is read once, every session, and never carries
  new information. Pure data-ink waste under Tufte's rule.

### 1.2 Rolodex zone (`SessionCard.tsx:783`)

```
┌──────────────────── (border only when current card is focused) ───┐
│ │ before-focus session N-1                                         │
│ │ before-focus session N                                           │
│ ───────────────────── (rule when wrapping) ──────────────────────  │
│ ▎ FOCUSED CARD                                                     │
│ ───────────────────── (rule when wrapping) ──────────────────────  │
│ │ after-focus session 1                                            │
│ │ after-focus session 2                                            │
└────────────────────────────────────────────────────────────────────┘
```

**Friction:**
- Border only wraps the *focused* card, but the wrap-rule (`─` × 200) and
  the focused-border interact awkwardly when `wrapBefore`/`wrapAfter`
  triggers near the edge — visible in the screenshot as the `:` artifacts
  in the upper-right.
- `paddingTop={1}` and `paddingBottom={1}` on the rolodex create a
  rhythmically-spaced list, but combined with `gap={1}` between cards the
  vertical rhythm is 2 cells — half-a-card of unused vertical real estate
  in a sidebar that's already bottlenecked on height.

### 1.3 Collapsed `SessionCard` row (current)

```
▎name             ●N           ●  ◇
^                 ^^            ^   ^
│                 │            │   └─ status icon (working/waiting/ready/stopped/error)
│                 │            └────── unseen marker (teal)
│                 └─────────────────── live-agent count badge ("●" or "●3")
└──────────────────────────────────── current-session bar (▎ or space)

⎇ branch                  ⌁port
^                         ^
│                         └─ port hint, only if .ports exists
└─ branch glyph + truncated branch name

(collapsed-only) status text · 2/5 · stage-name
^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
└─ tone-coloured metadata summary (status + progress)
```

**Information density:** ~6 glyphs, up to 4 colour roles, 3 rows max.
**Friction:**
- `●N` (agent count) on the same line as `●` (unseen marker) reuses the
  same glyph at adjacent positions with different meanings. Direct violation
  of "one cell, one meaning."
- The status icon (`◇`/`◆`/`·`/`✗`) sits at the *right edge of variable-
  width text*, so its column drifts by row. HUD principle violated: status
  should live in a fixed gutter.
- `⎇` (U+2387) is a Nerd-Font-adjacent codepoint that renders inconsistently
  across fonts. Spec §4 already warns against this class of glyph.
- `⌁` (U+2301, ELECTRIC ARROW) for ports is an idiosyncratic choice that
  doesn't appear in any other reference TUI. Unfamiliar without a legend.
- The metadata summary line *only* renders when collapsed. The same data
  reappears in expanded form at a different position. The user has to learn
  two layouts.

### 1.4 Focused (expanded) `SessionCard`

Adds, in order:

1. Directory parent (when cwd ≠ session name) — two indented rows
2. Ports — clickable chunks at 3-per-row, with `local`/`     ` prefix
3. Agent instances list (`AgentListItem` × N)
4. Status / progress block with tone icons
5. Up to 8 log lines

**Friction:**
- The expanded card has **no internal structure cues** — it's a flat stack
  of rows, each free to define its own indent and glyph vocabulary. The
  ports section starts with `local`, agents start with `✕`, the status
  block starts with a tone-icon. No through-line.
- The 8-line log tail is the densest information block on screen but has
  the *least* visual hierarchy — every entry is the same dim grey with a
  tiny tone icon, and the source tag `[source]` is in `surface2` (almost
  invisible).
- Thread name vs. tool description vs. activity collapse into the *same*
  Row 2 of `AgentListItem` with the same `· ` leader, so the user can't
  tell at a glance whether the agent is working ("· editing tmux-…") or
  just-named ("· refactor watchers"). They look identical.

### 1.5 `AgentListItem` row

```
✕ claude-code #1859        ●  ◇
^ ^^^^^^^^^^^ ^^^^^         ^   ^
│ │           │            │   └─ status icon
│ │           │            └────── unseen
│ │           └─ short threadId (last 4 chars, dim)
│ └─ agent name
└─ dismiss control (red on hover)

· Editing tmux-header-sync.ts
^ ^^^^^^^^^^^^^^^^^^^^^^^^^^
│ └─ activity / threadName
└─ leader (also "stopped" status icon, also tone-neutral, also port separator)
```

**Friction:**
- The row 2 wraps mid-word at narrow widths because the truncation budget
  (60 chars) doesn't account for the actual sidebar width.
- The `✕` dismiss glyph and the `✗` error status glyph are visually
  near-identical in most fonts (U+2715 vs. U+2717 — different shapes by
  spec, often rendered identically by terminal fonts). On an errored agent
  the row reads "✕ claude-code … ✗" — two crosses, different meanings.

---

## 2. Glyph & colour collision matrix

The single biggest density problem is glyph reuse. Here's the current state.

| Glyph | Roles today | Conflict? |
|---|---|---|
| `●` | unseen marker; live-agent count badge; `generic` agent type in statusline | **Yes** — three meanings, two of them adjacent on the same row. |
| `·` | "stopped" status icon; activity-line leader (`AgentListItem` row 2); neutral tone-icon for log entries; port separator | **Yes** — four meanings, all visible simultaneously when an agent is stopped and has logs. |
| `◇` | "ready" status icon | Clean. |
| `◆` | "waiting" status icon; `amp` agent type in statusline | Mild — different surfaces, same shape, different colours. Acceptable if statusline keeps colour-coded glyphs. |
| `✕` | dismiss control | Risk of confusion with `✗` (error status) when both render in the same row. |
| `✗` | error status icon | See above. |
| `⚡` | header running-counter | Clean. |
| `⎇` | branch indicator | Font-portability risk; renders as tofu in non-Nerd-Font terminals. |
| `⌁` | port indicator | Unfamiliar; no precedent in reference TUIs. |
| `▎` | current-session left bar | Clean and conventional. |
| `★ /  ` | claude-code agent type (statusline) | Clean (already fallback-handled by spec). |
| `π / ▲ / ♦` | pi / codex / amp agent types (statusline) | Clean. |

| Colour role | Used for | Conflict? |
|---|---|---|
| `green` | `ready` status; success tone-icon | Clean. |
| `yellow` | `waiting` status; running-counter (`⚡`); warn tone-icon; running tone in some themes | Mild — `running` agent state is **blue** but the running header counter is **yellow**. Inconsistent. |
| `red` | `error` status; error tone-icon; dismiss-button hover; error-counter | Clean — all are "this is bad." |
| `blue` | `working` status; pane-focus indicator on header; info tone-icon | Mild — pane-focus and "currently working" both pull blue. They never collide visually but the *meaning* of blue depends on position. |
| `teal` | unseen marker; sky-related accents | Clean. |
| `pink` | branch glyph in focused card | Clean. |
| `surface2` | `stopped` status; deactivated chrome | Clean. |

---

## 3. Information hierarchy as it stands

If you list every fact the panel can display, ranked by *how often the user
needs it within 1 second of glancing*:

| Tier | Fact | Visible in collapsed card? | Visible at headline glance? |
|---|---|---|---|
| 1 (essential) | Which session is *current* | Yes (▎ bar) | Yes — full row position |
| 1 | Which session is *focused* (cursor) | Implicit (centre of rolodex) | Yes |
| 1 | Is anything running? | Yes (status icon, running-counter) | Yes |
| 1 | Is anything errored? | Yes (status icon, error-counter) | Yes |
| 2 (important) | Number of live agents per session | Yes (`●N` badge) | No — only on focused card or scroll |
| 2 | Branch per session | Yes (row 2) | Partial — truncated heavily |
| 2 | Unseen activity since last look | Yes (teal `●`) | Yes |
| 3 (on-demand) | Per-agent status | Only when focused | No |
| 3 | Per-agent thread ID | Only when focused | No |
| 3 | Per-agent current activity | Only when focused, narrow window | No |
| 3 | Listening localhost ports | Hint only when collapsed | Full only when focused |
| 4 (rare) | Working directory | Only when focused & ≠ name | No |
| 4 | Metadata logs | Only when focused | No |

**Observation:** the audit shows tier-1 and tier-2 are well-served, but
tier-3 is crammed into the same `AgentListItem` row with no visual
distinction between *agent identity* and *agent activity*. This is where
the redesign has the most room to gain density-by-clarity, not
density-by-cramming.

---

## 4. Statusline (tmux header) inventory

Per `docs/specs/tmux-header.md` and `header.tmux`:

```
  tcm   1: project   2:★ project   3:π project   4:▲ project   …
  ^^^^^^^^^^^^   ^  ^^^^^^^   ^  ^^^^^^^^   ^  ^^^^^^^^   ^  ^^^^^^^^
  │              │  │         │  │          │  │          │  │
  │              │  │         │  │          │  │          │  └─ window basename
  │              │  │         │  │          │  │          └─ codex agent glyph (blue)
  │              │  │         │  │          │  └─ pi agent glyph (blue)
  │              │  │         │  └─ claude-code agent glyph (blue)
  │              │  └─ inactive window basename (terminal default fg)
  │              └─ window index
  └─ session-name pill (theme.blue, bold)
```

**Information present:** session pill, window index, agent glyph (presence
only), window dir basename, zoom marker.

**Friction:**
- Glyph colour is **always blue**, regardless of agent state. Spec §1
  (Non-goals) acknowledges this — v1 is presence-only. But this means the
  statusline cannot tell you "claude-code is *errored* in window 3";
  you have to open the panel.
- The format string mixes `#I:` (window index + colon) with the glyph,
  producing `1:★ project` — the colon is a literal separator that creates
  visual noise when rapidly scanning a row of tabs.
- Active window styling is carried by `window-status-current-style:
  fg=blue,bold`. Inactive windows get `fg=default`. The agent glyph is
  *also* blue. On the active window, the glyph and the text are the same
  colour — the glyph loses its job as a category marker.

---

## 5. To investigate (not blocking)

- The `:` artifact in the upper-right of the focused border in the
  reference screenshot. Suspect: an OpenTUI border-corner edge case or a
  stray label render. Should be reproduced and filed before mockup work,
  but does not block design exploration.
- Whether the `paddingTop={1}` + `gap={1}` rhythm in the rolodex zone is
  intentional or accidental.
- Whether the 8-line log tail length is configurable (it's a magic 8 in
  `visibleLogs()`).

---

## 6. Open questions for the next phase

These were resolved in `02-zones.md` and `03-vocabulary.md` after this
audit was written. Kept here as the historical record of what the audit
surfaced.

1. **Glyph deduplication.** `·` and `●` both had multiple roles. Resolved:
   both retired entirely; replaced by Material Design Icon glyphs (see
   `03-vocabulary.md` §2 / §3).
2. **Status gutter.** Resolved as **two gutters per row** — left for
   severity, right for identity. (`02-zones.md` §3.)
3. **Branch glyph.** Resolved as MD `source-branch` `\u{F062C}` (Tier 4
   muted leader on the branch row inside the focused card only). Earlier
   resolution used Powerline `\u{E0A0}`; superseded for icon-family
   coherence — see `00-grounding.md` §"Principles we'll borrow" #1.
4. **Port glyph.** Initially resolved as nf-md server-network `\u{F048D}`,
   but **subsequently the entire ports feature was removed** during the
   Codex review pass (the user reported never having noticed ports in
   their workflow). See `03-vocabulary.md` §8 retired list, item "Ports
   detection (feature)" for the implementation deletion list.
5. **Activity vs. thread name.** Resolved as **progressive disclosure** —
   activity moves to the new activity zone (`02-zones.md` §4); thread name
   is dropped from default UI.
6. **Statusline colour-by-state.** Resolved — promoted to v1 of the
   redesign. Active-window vs. severity colour collision resolved as
   "active uses bold, severity uses colour without bold." Full rules in
   `03-vocabulary.md` §6.
7. **Header counters vs. row-level icons.** Resolved by **dropping the
   counters entirely**; the rolodex is the summary.
8. **Chrome around the focused card.** Resolved as kept (earned chrome —
   the rounded border tells the eye where the rolodex pin is).
