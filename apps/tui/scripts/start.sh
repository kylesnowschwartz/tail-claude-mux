#!/usr/bin/env bash
# Start the tcm TUI.
# Run from inside a tmux pane (auto-detects via $TMUX).

if [ -n "${TMUX:-}" ]; then
    TCM_DIR="$(tmux show-environment -g TCM_DIR 2>/dev/null | cut -d= -f2)"
fi
TCM_DIR="${TCM_DIR:-$(cd "$(dirname "$0")/../../.." && pwd)}"
TUI_DIR="$TCM_DIR/apps/tui"

BUN_PATH="${BUN_PATH:-$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")}"

cd "$TUI_DIR"
export REFOCUS_WINDOW
export TCM_DIR
exec "$BUN_PATH" run src/index.tsx 2>/tmp/tcm-err.log
