# Side-Panel + Statusline Redesign (Apr 2026)

This is a retrospective summary of the panel + tmux-statusline redesign that
landed in late April 2026. It captures the durable style stance and the locked
decisions that drove the implementation.

The full design log (8 files, ~1.8K lines: grounding, audit, zones, vocabulary,
mockups, Codex review pass) lived under `docs/design/` and was collapsed to
this file once the work shipped. The reasoning trail is in git history at
commit `7a55f26~` and earlier.

---

## Resolved style stance

> **HUD grammar + Charm warmth + Tufte restraint by default. Chrome must
> earn its keep — it is allowed when it focuses attention on a zone, never
> as decoration.**

Operationalised:

| Principle | Implication |
|---|---|
| **Position is meaning** | Status icons live in fixed gutter columns, never floating right of variable-width text. Empty gutters stay blank — they don't collapse. |
| **Severity by colour, taxonomy by shape** | EFIS-derived palette: `green = nominal`, `yellow = caution / waiting`, `red = warning / error`, `blue = working`, `surface2 = stopped / neutral`. Don't double-load colour and shape onto the same axis. |
| **Declutter levels** | Three modes: collapsed, focused, focused-clean. Bound to keypress, not auto-revealed. Permanently-reserved zones whose *content* updates on events (e.g. activity) are not "chrome" — they're structural. |
| **Data-ink ratio** | Every glyph and label was audited against `01-audit.md`. Anything that didn't justify its cell got removed. |
| **Four text tiers** | Primary (bold + `text`), Secondary (`text`), Dim (`text` + Faint), Muted (`overlay0`). Italic is sanctioned only inside the activity-zone description column. |
| **Family-anchored iconography** | Material Design Icons (`nf-md-*`) are the default. Three exceptions: brand letterforms (`π / ▲ / ♦`), vendored Clawd glyph (`\u{100CC0}`), animated brail spinners. Nerd Fonts are a hard requirement. |
| **Earned chrome** | A border around the focused card earns its keep — it tells the eye where the pin is. A border around every collapsed card does not. |

When a later decision is contested, the question is: *does it serve HUD
position-grammar, Charm warmth, or Tufte restraint?* If it serves none, drop
it.

---

## Locked decisions (the design that shipped)

### Zones

The panel is split into four zones, top to bottom:

1. **Header** (sticky, 1 cell) — `tcm   N sessions`
2. **Rolodex** (flex) — sessions list, focused card pinned at vertical centre
3. **Activity** (permanently reserved) — freshest event populates first; older entries scroll up
4. **Footer** (sticky) — keybind hints, status

Header counters were dropped — the rolodex is the summary.

### Status grammar

Two gutters per row:

```
[severity]  content                 [identity]
   ^^                                   ^^
   col 0 — 1 cell                       last col — 1 cell
```

- **Severity** (left): `working` = brail spinner (blue), `done` = check (green), `error` = X (red), `waiting` = clock (yellow), `interrupted` = pause (yellow), nominal/idle = blank.
- **Identity** (right): per-agent glyph — Clawd for `claude-code`, `π` for `pi`, etc. Same glyph appears in three positions across surfaces (header brand, tmux statusline window-presence, panel right gutter) — meaning given by position.

### Rolodex layout (the post-Codex pivot)

The rolodex is a **linear tape**, not a wheel:

- Sessions stay in their natural order
- The focused card pins at vertical centre
- The viewport slides over the tape as focus moves
- `j` / `k` wrap modularly (one keystroke at the bottom snaps to the top)
- Chevron separators (nf-md chevron-up / chevron-down) anchored mid-rule above and below the focused card; semantics are "structural separator," not "wrap point"

The earlier wheel/rotation model (clockwise split into halves around the
focused pin) was retired during live-QA because users lost track of where they
were in the list.

### Activity zone

- **Permanently reserved** — no auto-show, no fade, no animation
- Heading is the focused-session name + nf-md arrow-right separator
- Description column is the only place italic is sanctioned (Tier 3 + italic; freshest entry steps up to Tier 2 + italic)

### Same-type count format

**Numeric-only**: `2`, `3`, `9+`. The earlier `2π` form (which encoded
"2 of the same agent type") was reverted during the Codex review (B1) — it
collided with the position-is-meaning rule because count and identity were
sharing the same cell.

### Unseen state

**Colour-only** — name shifts to `teal`. No glyph. The position-is-meaning
rule means an unseen indicator can't claim a gutter that already has a job.

### What got removed

- **Ports as a feature.** During the redesign, the lsof polling loop, the
  `ports: number[]` field on `SessionData`, the rendering code, and the
  width-sync accounting were all deleted. README still mentions
  "detected localhost ports" — that's the watcher path which remained,
  not the per-session port-list rendering.
- **Collapsed-session severity gutter when nominal.** Conditional render —
  blank when there's nothing to report.
- **Header counters.** The rolodex itself is the count.
- **Glyph reuse.** `·` previously had four roles, `●` had three. Each glyph
  now has exactly one role.

---

## Why the constraints were what they were

- **Nerd Fonts required, not optional.** The whole position-grammar collapses
  if the Material Design Icons family isn't available. Falling back to ASCII
  defeats the redesign — better to declare the dependency than half-render.
- **MDI as anchor family.** Two thousand glyphs, consistent visual weight,
  versioned (we pin `glyphnames.json` v3.4.0 in `scripts/glyph/`). Letterform
  brand glyphs (`π ▲ ♦`) get an exception because they *are* identity in a
  way that an icon can't substitute. Clawd gets an exception because it's
  the project's mascot and users learn it once across three surfaces. Brail
  spinners get an exception because animation is an MDI gap.
- **Four tiers, not six.** Audit found six-token sprawl with no rules; tier
  was being used to encode "importance" *and* "supporting fact" *and*
  "deferred attention." Collapsed to four with explicit roles.

---

## Reference: the structural pieces

For implementation lookups, the runtime code lives in:

- `apps/tui/src/vocab.ts` — glyph table, codepoints, families
- `apps/tui/src/components/SessionCard.tsx` and friends — zone rendering
- `packages/runtime/src/themes.ts` — palette tokens
- `integrations/tmux-plugin/scripts/header.tmux` — tmux statusline glyphs

For glyph work: `just glyph-search`, `just glyph-render` (see [AGENTS.md](../../AGENTS.md)).
