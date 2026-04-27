# tcm design notes

This folder is the working design log for the side-panel and tmux statusline
redesign. It is intentionally separate from `docs/explanation/`,
`docs/reference/`, and `docs/specs/`:

- **explanation / reference** describe the product as it ships
- **specs** are durable contracts for implementers
- **design** is the *thinking* — exploration, options, principles, and the
  reasoning trail behind whatever ends up in `specs/`

## Layout

| File | Purpose |
|---|---|
| `00-grounding.md` | Disciplines, references, the resolved style stance |
| `01-audit.md`     | What the current widget shows, where the friction is |
| `02-zones.md`     | Panel zone layout (header / rolodex / activity / footer) |
| `03-vocabulary.md` | Canonical glyph / color / column rules within the zones |
| `04-mockups/00-baseline.md` | Faithful ASCII transcription of the panel as it ships today |
| `04-mockups/01-proposal.md` | Resolution A vs B exploration (historical record; locked design moved to 02) |
| `04-mockups/02-canonical.md` | **The locked design.** Updated 2026-04-26 after Codex review pass. |
| `04-mockups/03-live-opentui/` *(pending)* | `--mock` flag + live OpenTUI render of the canonical mockup |
| `05-spec.md`      *(pending)* | Implementer-facing rules, promoted to `docs/specs/` once locked |

Numbered prefixes are read order, not strict dependency. New explorations land
as siblings (e.g. `03b-mockups-statusline/`) rather than overwriting earlier
thinking.

## Resolved stances

**Style** (locked 2026-04-26):

> HUD grammar + Charm warmth + Tufte restraint by default. Chrome must
> earn its keep — it is allowed when it focuses attention on a zone,
> never as decoration.

**Iconography** (locked 2026-04-26):

> Nerd Fonts are a hard requirement. Material Design Icons (`nf-md-*`)
> are the default family. Four exceptions earn their place: Powerline
> branch glyph, brand letterforms (`π ▲ ♦`), vendored mascots (Clawd
> `\u{100CC0}`), and animated brail spinners. Clawd appears in three
> positions — header (brand), tmux statusline (window-presence), panel
> right gutter (per-agent identity).

**Text hierarchy** (locked 2026-04-26):

> Four tiers replace the previous six-token sprawl: Primary (bold +
> `text`), Secondary (`text`), Dim (`text` + Faint attribute), Muted
> (`overlay0` colour). Pane-unfocused slides every tier one step dimmer.
> Italic is sanctioned only for the activity-zone description column.

Full reasoning lives in `00-grounding.md`; concrete glyph table in
`03-vocabulary.md`. Reviewer-facing tour is `REVIEW.md`. Codex's review
log of the locked docs is `REVIEW-NOTES-codex.md`.
