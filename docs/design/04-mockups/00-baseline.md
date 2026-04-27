# 04-mockups/00 · Baseline (the panel as it ships today)

This is a faithful ASCII transcription of the user's reference screenshot
captured 2026-04-26. The same session-data is used as input for the
proposal mockups in `01-proposal.md`, so the diff between this file and
that file isolates the vocabulary + zone changes.

Width: 38 cells (matching the reference). Sidebar background is `crust`.
Pane is **focused** in this screenshot.

> **Note for grep-based audits:** this baseline doc intentionally contains
> retired glyphs (`·`, `●`, `⎇`, `✁`, `✕`, `⚡`, `◆`, `◇`, `✗`).
> They are present *because* this is the "before" reference. Sanity checks
> verifying the redesign retired these glyphs should exclude this file and
> the side-by-side panels in `01-proposal.md` from their search.

---

## Reference dataset

The actual server state captured in the reference:

| # | Session                       | Branch              | Agents (alive)                       | State              |
|---|-------------------------------|---------------------|--------------------------------------|--------------------|
| 1 | `ai-engineering-template`     | `kyle/cc-native…`   | 1 alive (claude-code)                | ready              |
| 2 | `pi-mono`                     | (no branch shown)   | 2 alive                              | ready              |
| 3 | `tcm` *(focused)*    | `main`              | 4 alive — pi #15c8 working, pi #10bc + 2 claude-code ready | mixed |
| 4 | `claude-code-syste…`          | `main`              | 0 alive                              | (no agents)        |
| 5 | `the-themer`                  | `main`              | 1 dim grey badge (likely exited)     | (stopped tail)     |

Header counters: `Sessions 5 ⚡1` — 5 sessions, 1 running.

The pi #15c8 working agent is running `ask_user` (its `toolDescription`
field). claude-code #1859 has a `threadName` that doesn't fit:
`"Base directory for this skill: /Users/…"` — wraps once, then
truncates.

---

## ASCII transcription

```
┌──────────────────────────────────────┐
│  Sessions  5  ⚡1                    │   ← Panel header zone
│                                      │
│   ai-engineering-te…           ●  ◇  │   ← session 1, collapsed
│   ⎇ kyle/cc-native…                  │
│                                      │
│   pi-mono  ●2                     ◇  │   ← session 2, collapsed
│                                      │
│  ╭──────────────────────────────╮    │   ← rolodex top wrap-rule
│   ╭─────────────────────────────╮    │   ← focused-card border (rounded)
│   │▎tcm  ●4         :  │    │   ← session row (working spinner ":" frozen)
│   │ ⎇ main                      │    │   ← branch row
│   │ × pi  #15c8              :  │    │   ← agent: pi #15c8, working spinner
│   │ · ask_user                  │    │   ← agent's activity (toolDescription)
│   │ × pi  #10bc              ◇  │    │   ← agent: pi #10bc, ready
│   │ × claude-code            ◇  │    │   ← agent: claude-code (no thread), ready
│   │ × claude-code #1859      ◇  │    │   ← agent: claude-code #1859, ready
│   │ · Base directory for        │    │   ← activity wraps mid-message
│   │   this skill: /Users/       │    │   ← (truncates here, real msg longer)
│   ╰─────────────────────────────╯    │
│  ╰──────────────────────────────╯    │   ← rolodex bottom wrap-rule
│                                      │
│   claude-code-syste…                 │   ← session 4, collapsed (no agents)
│   ⎇ main                             │
│                                      │
│   the-themer  ●                   ◇  │   ← session 5, collapsed
│   ⎇ main                             │
│                                      │
│  ─────────────────────────           │   ← footer rule
│  j/k nav  ↵ switch  q quit           │   ← footer
└──────────────────────────────────────┘
```

---

## Per-row colour & tier annotation

| Row content                                     | Glyphs (with role) | Colour roles |
|-------------------------------------------------|--------------------|--------------|
| `Sessions  5  ⚡1`                              | `⚡` running counter | `text` bold (Sessions), `subtext0` (5), `yellow` (⚡1) |
| `  ai-engineering-te…           ●  ◇`           | `●` agent-count badge, `◇` status (ready) | `subtext1` (name), `overlay0` (badge), `green` (◇) |
| `  ⎇ kyle/cc-native…`                           | `⎇` branch glyph     | `overlay0` for both glyph and text |
| `  pi-mono  ●2                     ◇`           | `●2` agent-count, `◇` status | same as above; `2` follows badge |
| `▎tcm  ●4         :`                   | `▎` current-bar, `●4` count, `:` working spinner | `blue` (▎), `subtext0` (name + ●4), `blue` (spinner) |
| `× pi  #15c8              :`                    | `×` dismiss, `:` spinner | `overlay0` (×), `subtext1` (pi), `overlay0` (#15c8 dim), `blue` (spinner) |
| `· ask_user`                                    | `·` activity leader  | `blue` italic (matches working spinner colour) |
| `× pi  #10bc              ◇`                    | `×` dismiss, `◇` ready | `overlay0` (×), `subtext1` (pi), `overlay0` (#10bc), `green` (◇) |
| `× claude-code            ◇`                    | `×`, `◇`             | same |
| `× claude-code #1859      ◇`                    | `×`, `◇`             | same |
| `· Base directory for…`                         | `·` activity leader  | `overlay0` italic (no current activity colour because state is `ready`, not `working`) |

---

## Friction inventory (re-grounded against this exact dataset)

The audit doc enumerated friction abstractly. Now we can be specific:

### F1. The wrap-row catastrophe

```
│ · Base directory for         │
│   this skill: /Users/        │
```

This is a Claude Code permission prompt about a skill. The full message is
likely 50–100 characters; the visible 28-cell content area shows ~24 of
them and continues to truncate after the wrap. **The user has zero
ability to read what permission they're being asked about.** This is the
most concrete instance of the audit's "row 2 wrap problem."

### F2. The `:` working-spinner shape collision

The `:` spinner-frame appears at the right edge of the session header AND
at the right edge of the pi #15c8 agent row. Both reads as "working" in
the user's mental model, but the same glyph in the same column also looks
like a render artifact — there's no visible animation in a static
screenshot, and `:` is not in any documented icon table.

The fix is to either (a) use a glyph that doesn't have a frozen-`:`
frame, or (b) use a glyph clearly distinct from punctuation — both
solved by moving to nf-md glyphs.

### F3. The `●` and `●N` agent-count badge

Five different sessions, three different `●` renderings:
- `ai-engineering-te…  ●` — single-agent session, dim grey dot
- `pi-mono  ●2`              — multi-agent session, dim grey dot+digit
- `tcm  ●4`         — focused multi-agent, brighter dot+digit
- `the-themer  ●`            — single-agent (likely stopped), dim grey dot
- `claude-code-syste…`       — no badge, no agents

The same glyph `●` carries five subtly different meanings depending on
position, count, and colour. The user is parsing all of these with no
explicit legend. Replacing with the nf-md robot-outline glyph + a
clearer count rule resolves the ambiguity.

### F4. The `⎇ main` branch row appears five times

In a 5-session view, `⎇ main` is visible 4 times (every collapsed
session that has a branch). The branch is information, but for sessions
in the same project family it's *repeated* information — eats vertical
space without telling the user anything new. Under the new zone design,
branch only appears inside the focused card, recovering 4 rows of
vertical real estate.

### F5. The dismiss-`×` is always visible across 6 agent rows

In the focused card, every one of the 4 agent rows starts with `×`. Plus
the previous + current cards have other dismiss buttons. The dismiss
control is *almost never used* and is in a column the user's eye
constantly hits. Hiding it until the agent row is the j/k focus removes
4–6 cells of attention pollution from the focused card.

### F6. The activity row's `·` leader is overloaded

```
│ · ask_user                   │   ← pi #15c8's current tool call
│ · Base directory for…        │   ← claude-code #1859's threadName / permission
```

Both rows start with `· ` but they're *fundamentally different things*:
one is an active tool call (working state, blue), the other is a thread
name fragment (ready state, dim). The user has to read the whole row to
disambiguate. Under the new design these become activity-zone entries
with explicit source columns:

```
 cc 1859  Base directory for this skill: /Users/…  (truncates clean)
 pi 15c8  ask_user  (passed)
```

### F7. Header `Sessions 5 ⚡1` — the `Sessions` literal is data-ink waste

`Sessions` doesn't tell the user anything they can't see by looking at
the rolodex. `5` is real information. `⚡1` is real information but
duplicates what a quick scan of the rolodex's severity gutters would
show.

---

## Cell-budget at this exact width (38 cells, 28 of content)

```
0                                    38
│                                     │
[s] [content                       ][i]
 ↑ ↑                                ↑
 │ │                                identity gutter (1 cell, sometimes 2)
 │ left padding (1 cell)
 severity (today: floats; under proposal: column 0)
```

The reference screenshot's effective content width is 28 cells (after
accounting for border + padding on both sides). The wrap-row problem
gets worse at narrower widths and only marginally better at wider ones.
Activity-zone migration is the only intervention that solves it
structurally.

---

## What the baseline tells us about the proposal

Three concrete predictions for `01-proposal.md`:

1. **The activity zone immediately recovers ≈4–6 vertical cells** in the
   focused card by moving `· ask_user` and `· Base directory for…` out.
2. **The branch row reduction recovers ≈4 vertical cells** across the
   collapsed sessions.
3. **The wrap-row problem disappears entirely** because activity entries
   in the dedicated zone get a wider effective content width AND can
   ellipsis-truncate cleanly without breaking the rolodex's vertical
   rhythm.

If the proposal mockup doesn't deliver all three, we go back to the
zones doc.
