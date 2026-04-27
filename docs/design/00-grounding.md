# 00 · Grounding

This document anchors the redesign in established disciplines so later choices
have a defensible "why." It is deliberately curated, not exhaustive — one or
two canonical references per discipline, distilled to the principles we will
actually use.

The resolved style stance lives at the bottom (§7).

---

## 1. HUD philosophy (aerospace EFIS)

### Canonical references

- **MIL-STD-1472H** (US DoD Human Factors Engineering) — the public-domain
  baseline for cockpit, console, and control-room iconography. Chapter 5
  (visual displays) and the colour-coding table in §5.2.6 are the source of
  the green/amber/red/cyan severity convention every modern HUD inherits.
- **ARINC 661** (Cockpit Display System Interfaces) — the "widget" model that
  Boeing/Airbus PFDs and MFDs use. Read for: the idea that every readout has
  a *fixed location*, *fixed label*, and *fixed unit*; pilots scan by
  position, not by reading.
- **F-35 Panoramic Cockpit Display** writeups (open press kits) — useful as a
  modern example of the "declutter levels" idea: the same display can shed
  90 % of its glyphs on a button press because most of the time the pilot
  doesn't need them.

### Principles we'll borrow

1. **Severity by colour, semantics by shape.** Colour answers "should I care
   right now?"; glyph shape answers "what is this thing?". Never overload one
   onto the other.
   - green = nominal, amber = caution, red = warning, cyan = advisory,
     white/grey = neutral readout.
2. **Position is meaning.** A status indicator that lives in the same column
   on every row is read by location, not by parsing. Once positions are
   established, *do not move them* even when the row is empty — leave the
   gutter blank.
3. **Declutter levels.** A HUD has at least two: full and clean. The user
   should be able to ask the panel for less without losing safety-critical
   data. Map this to: collapsed card vs. focused card vs. focus-hidden mode.
4. **No ornamentation.** A bezel exists to frame, not to decorate. Every
   pixel of chrome must answer "what does this make easier to read?"

---

## 2. Information design (Tufte, Bertin)

### Canonical references

- **Edward Tufte, *The Visual Display of Quantitative Information*** —
  data-ink ratio, chartjunk, small multiples. The relevant chapter for us is
  Ch. 4 ("Data-Ink and Graphical Redesign") — the iterative stripping of a
  Playfair time-series down to its information core is the exact mental
  exercise we'll do on `SessionCard`.
- **Edward Tufte, *Envisioning Information*** — Ch. 3 ("Layering and
  Separation") and Ch. 4 ("Small Multiples"). The relevant ideas:
  - **1 + 1 = 3** — every visual element above the minimum creates *new*
    visual relationships you didn't ask for. Subtract until 1+1=2.
  - **Layering** — different categories of information can coexist on the
    same surface if each is on its own visual layer (weight, value,
    saturation), not its own row.
- **Jacques Bertin, *Semiology of Graphics*** — the seven retinal variables
  (position, size, shape, value, colour, orientation, texture). For a
  monospace TUI, *position*, *value* (lightness), and *colour* are the only
  three we control freely; *shape* is constrained to the glyph set; *size*
  and *orientation* are essentially unavailable.

### Principles we'll borrow

1. **Maximise the data-ink ratio.** Every line, glyph, and colour should
   carry meaning. Decorative borders, repeated counts, and "spacing dots"
   are pure overhead.
2. **Layer; don't list.** Rather than "row 1, row 2, row 3, row 4" of equal
   weight, use weight, dim, and indent to make some information recede.
   Subordinate facts should be readable but not *equally* loud.
3. **Subtract first.** Before adding any new glyph or label, ask whether two
   existing ones can be merged. The current widget is dense partly because
   nothing was ever taken away.
4. **Small multiples beat one big chart.** Three identical, predictable
   session cards stacked are easier to scan than one rich card with
   conditional rows.

---

## 3. Iconography & glyphs in monospace

### Canonical references

- **Nerd Fonts project** — the de-facto Powerline+icons font stack for
  terminal apps. We treat Nerd Fonts as a **hard requirement**, not a
  fallback layer. Users without a patched font are not in our support
  matrix; the trade is fidelity over inclusivity.
- **Material Design Icons (`nf-md-*`)** — the largest and most stylistically
  coherent family inside Nerd Fonts. Consistent stroke weight, modern shape
  language, ~7000 icons. We anchor on this family as the *default*; anything
  outside it has to earn its presence.
- **Powerline conventions** — the original "segmented bar" idiom. We
  reject the segment-arrow chrome (`\uE0B0`-style triangles), which fails
  Tufte's 1+1=3 rule. Earlier tcm designs cherry-picked the
  Powerline branch glyph (`\uE0A0`) for the branch row leader; the
  canonical design has since moved that to MD `source-branch`
  (`\uF062C`) for icon-family coherence — one fewer exception to the
  Material-Design-by-default rule.
- **`tail-claude-hud`** (sibling project) — already operates a curated
  Nerd-Font icon system with a 4-tier text hierarchy. Concrete prior art for
  per-tool-category icons (pen-nib for edit, wrench for bash, magnifying
  glass for grep) and circle-slice progress glyphs (`\u{F0A9E}` →
  `\u{F0AA5}`). We borrow the discipline; we may borrow specific glyphs.
- **The tcm tmux-header spec** (`docs/specs/tmux-header.md` §4) —
  already vendors the Clawd mascot at `fonts/Clawd.ttf` (codepoint
  `\u{100CC0}`). The precedent: when no existing glyph captures what we
  want, we are willing to vendor a custom one.

### Principles we'll borrow

1. **One family is the default; exceptions earn their place.**
   Material Design Icons are the anchor. The three exceptions are:
   - **Brand letterforms** (`π` for pi, `▲` for codex, `♦` for amp) —
     iconic *because* they aren't part of an icon family. They are marks.
   - **Vendored custom glyphs** (Clawd `\u{100CC0}`) — when an agent has its
     own visual identity, we'd rather vendor than approximate.
   - **Animated brail spinners** for indeterminate-progress — animation is
     its own signal that no static glyph can replicate.
2. **One cell, one meaning, one column.** A glyph that means different
   things in different positions (`·` = stopped status / activity leader /
   port separator / neutral tone, today) is a decoder ring the user has to
   carry. Pick one role per glyph.
3. **Shape encodes taxonomy; colour encodes severity.** Outline vs. filled,
   circle vs. square vs. triangle — these are stable distinctions in the MD
   family. Use them as taxonomy; never overload colour and shape onto the
   same axis.
4. **Brand glyphs (Clawd, π, ▲, ♦) signal identity in the same position
   across surfaces.** Same glyph, different positions, different meanings —
   header = brand mark, statusline = window-presence, panel right gutter =
   per-agent identity. The user learns the glyph once.

## 4. Reference TUIs to study

These are not inspirations to copy — they are *prior art* we should be able
to point to when defending or rejecting a choice.

| Tool | What it does well | What we should *not* copy |
|---|---|---|
| `btop` | Header counters, severity colour, sparkline strips inside a fixed grid | Heavy box-drawing borders everywhere; cluttered when full |
| `k9s` | Tabular density, footer key-hints, mode/context badge in header | Modal-heavy keymap; assumes you've memorised it |
| `lazygit` | Multi-pane focus model, cyan/yellow severity scheme, per-pane chrome only when focused | Pane titles in border-corners — easy to misread |
| `gitui` | Calmer than lazygit; restrained borders; clear focus highlight | Less information-dense than we want |
| `helix` statusline | Compact left/centre/right grammar; mode-as-colour | None — the helix bar is a strong reference |
| `atuin` history UI | Excellent column-aligned scan layout for ranked lists | Bottom-anchored chrome that we don't need |
| `zellij` tab-bar | Plain coloured tabs, no border decoration | Per-tab chrome can balloon when many tabs open |
| Charm.sh apps (`gum`, `glow`, `soft-serve`) | Generous spacing, soft palette, modern feel; they prove a TUI doesn't have to be brutalist to be respectable | Excessive spacing — fine for one-shot tools, bad for a sticky sidebar |
| Bloomberg Terminal | Per-cell value density; abbreviated labels in fixed positions | Onboarding curve; we are not training day-traders |

The two anchors most aligned with our brief are **helix's statusline grammar**
(left-centre-right zones, mode-as-colour) and **Charm's restraint with
warmth** (palette discipline without brutalism).

---

## 5. Console UX patterns we'll lean on

- **Progressive disclosure.** A collapsed `SessionCard` shows the headline.
  The focused card expands the same data — it does not invent new
  vocabulary. If a glyph means *editing* in the focused card, it must mean
  *editing* in the collapsed card (or be hidden, never reused).
- **Focus pinning.** The current rolodex-with-pinned-focus pattern is good
  HUD discipline — the focused row is at a fixed location, the user's eye
  doesn't track. Keep it.
- **Sticky vs. flexed sizing.** The chrome (header, footer) is sticky;
  content (rolodex) flexes. A redesigned widget should be explicit about
  which boxes flex on terminal resize and which don't.
- **The peripheral-vision tier.** The tmux statusline is read with peripheral
  vision while the user works in another pane. It must survive being
  *almost-not-looked-at*. The side panel is read with foveal vision and can
  carry more.

---

## 6. Constraints particular to this codebase

- Renderer is **OpenTUI / SolidJS**. Layout primitives are flexbox-style
  (`flexDirection`, `flexGrow`, `flexShrink`, `gap`, `padding*`). No grid
  primitive — multi-column alignment is achieved by reserving fixed cell
  widths via `flexShrink={0}` boxes.
- **Theme palette is Catppuccin-shaped** across all 7 builtin themes
  (`packages/runtime/src/themes.ts`): three "neutral" tiers (`base/mantle/
  crust` = backgrounds, `surface0/1/2` = chrome fills, `overlay0/1` = dim
  text), three "text" tiers (`text/subtext0/subtext1`), eight accents
  (`blue/lavender/pink/mauve/yellow/green/red/peach/teal/sky`). Any new
  vocabulary must compose from those tokens.
- **Five-state agent vocabulary already exists**: `working / waiting / ready
  / stopped / error`, mapped to `blue / yellow / green / surface2 / red` for
  colour. The five **labels** and the five **colours** are stable design
  currency. The five **glyphs** were the weakest part of the system (`·`
  reused four ways) and are being refreshed under the new font-first stance
  in `03-vocabulary.md`.
- **tmux header spec** (`docs/specs/tmux-header.md`) already vendors the
  Clawd font and uses Plane-16 PUA-B (`\u{100CC0}`). The redesign formalises
  this precedent: novel glyphs *are* allowed when they earn their place;
  vendoring is the mechanism. v1 of the spec called Nerd Font codepoints "a
  font dependency we impose on every user" — that constraint is now lifted.
  Nerd Fonts are a hard requirement.
- **Text hierarchy must collapse to four tiers**, not the six the codebase
  uses today (`text / subtext0 / subtext1 / overlay0 / overlay1 /
  surface2`). The four tiers are Primary (bold + `text`), Secondary
  (`text`), Dim (`text` + Faint attribute), Muted (`overlay0` colour). See
  `03-vocabulary.md` §4.

---

## 7. Resolved style stance

> **HUD grammar + Charm warmth + Tufte restraint by default. Chrome must
> earn its keep — it is allowed when it focuses attention on a zone, never
> as decoration.**

Operationalised:

| Principle | Implication for the redesign |
|---|---|
| HUD: position is meaning | Status icons live in a *fixed gutter column*, never floating right of variable-width text. Empty gutters stay blank — they don't collapse. |
| HUD: severity by colour | Re-anchor on the EFIS palette: `green = ready / nominal`, `yellow = waiting / caution`, `red = error / warning`, `blue = working / active`, `surface2 = stopped / neutral`. The current mapping already matches; we make it explicit. |
| HUD: declutter levels | Three modes: collapsed, focused, focused-clean (advisories hidden). Bound to keypress, not auto-revealed. **Always-visible zones whose *content* updates on events (e.g. the activity zone) are NOT covered by this rule** — the zone is structural, not chrome that pops in and out. The user-controlled toggle still applies for the activity history view (the `a` keybind). |
| Tufte: data-ink ratio | Every glyph and label currently in `SessionCard` and `AgentListItem` is justified explicitly in `01-audit.md`. Anything that fails the test is removed in the first redesign pass. |
| Tufte: layering | Four text tiers (Primary / Secondary / Dim / Muted) carry hierarchy; subordinate facts recede via *attribute and value*, not via removal or extra rows. |
| Charm: warmth | Keep one warm accent (the existing pink/mauve for the focused-branch glyph; teal for unseen). Do not flatten the palette to monochrome in the name of austerity. |
| Earned chrome | A border around the focused card *does* earn its keep — it tells the eye where the rolodex pin is. A border around every collapsed card does *not* — same job done by indent + weight. |
| Iconography: family-anchored | Material Design Icons are the default family. Brand letterforms (`π ▲ ♦`), vendored glyphs (Clawd `\u{100CC0}`), and animated brail spinners are the three exceptions. Anything else is a violation. (The branch glyph was previously a fourth exception via Powerline `\uE0A0`; it has since been moved into the MD family as `source-branch \uF062C`.) |
| Iconography: Clawd in three positions | Header (brand mark), tmux statusline (window-presence), panel right gutter (per-agent identity). Same glyph; meaning is given by position. |

This stance is the standing rule. When a later decision is contested, the
question is: *does this choice serve HUD position-grammar, Charm warmth, or
Tufte restraint?* If it serves none, drop it.
