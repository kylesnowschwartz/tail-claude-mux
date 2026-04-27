# tcm development recipes

set shell := ["zsh", "-cu"]

root := justfile_directory()
bun := env("BUN_PATH", `command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun"`)

# Build TUI, restart server, respawn sidebars
[group('dev')]
restart:
    "{{root}}/scripts/restart.sh"

# Kill server and all sidebars
[group('dev')]
stop:
    "{{root}}/scripts/stop.sh"

# Build the TUI bundle
[group('dev')]
build:
    @cd "{{root}}/apps/tui" && "{{bun}}" run build

# Run runtime tests
[group('test')]
test:
    cd "{{root}}/packages/runtime" && "{{bun}}" test

# Tail server debug log
[group('dev')]
log:
    tail -f /tmp/tcm-debug.log

# Install the Clawd mascot font for the tmux header (idempotent)
[group('dev')]
install-clawd:
    "{{root}}/scripts/install-clawd-font.sh"

# Search Nerd Font glyph names (regex, case-insensitive)
[group('glyph')]
glyph-search PATTERN *ARGS:
    @"{{bun}}" run "{{root}}/scripts/glyph/glyph.ts" search "{{PATTERN}}" {{ARGS}}

# Reverse-lookup a Nerd Font codepoint (hex) -> name
[group('glyph')]
glyph-lookup HEX:
    @"{{bun}}" run "{{root}}/scripts/glyph/glyph.ts" lookup "{{HEX}}"

# Rasterise glyphs (codepoints or names) side-by-side to /tmp/glyph-render.png
[group('glyph')]
glyph-render +IDS:
    @"{{bun}}" run "{{root}}/scripts/glyph/glyph.ts" render {{IDS}}

# Print the vendored glyphnames.json METADATA (Nerd Fonts version pin)
[group('glyph')]
glyph-version:
    @"{{bun}}" run "{{root}}/scripts/glyph/glyph.ts" version
