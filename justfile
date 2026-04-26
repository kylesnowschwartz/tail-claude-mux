# opensessions development recipes

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
    tail -f /tmp/opensessions-debug.log

# Install the Clawd mascot font for the tmux header (idempotent)
[group('dev')]
install-clawd:
    "{{root}}/scripts/install-clawd-font.sh"
