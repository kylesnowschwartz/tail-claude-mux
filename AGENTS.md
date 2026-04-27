# tcm — AI Agent Instructions

This project uses Specky2 for spec-driven development. Specky2 orchestrates agents through a staged pipeline (research, blueprint, implement, test, review) with governance rules that prevent implementation before specifications exist. See [SPECKY-ROUTING.md](SPECKY-ROUTING.md) for available agents, skills, and methodology configuration.

## Tooling

### Nerd Font glyph lookup & preview

**Never guess Nerd Font codepoints from memory.** Glyph names are upstream identifiers, not visual descriptions — e.g. `md-arrow-u-left-top` is at U+F17B3, but U+F18A6 is `md-cards-playing-diamond`. Codepoints also drift across MDI / Nerd Fonts versions. Authoritative data is vendored at `scripts/glyph/glyphnames.json` (Nerd Fonts v3.4.0).

Use the `glyph` tool to search names, reverse-lookup codepoints, and **rasterise glyphs to PNG** so you can read the actual shape before changing code:

```bash
just glyph-search arrow --prefix md     # find candidates by name (regex, case-insensitive)
just glyph-lookup F054C                 # reverse: codepoint → name
just glyph-render F004D F054C F17B3     # rasterise side-by-side → /tmp/glyph-render.png
```

Then `read` the PNG — it returns as an image attachment — and verify the glyph visually matches your intent before editing `apps/tui/src/vocab.ts`, `integrations/tmux-plugin/scripts/header.tmux`, or anywhere else a codepoint lands. See `scripts/glyph/README.md` for design rationale and full usage.
