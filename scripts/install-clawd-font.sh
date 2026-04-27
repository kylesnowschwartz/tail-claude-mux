#!/usr/bin/env bash
# Install the Clawd mascot font (U+100CC0) into the user font directory.
# Idempotent: safe to re-run. The tcm tmux header detects the
# font's presence at server start and lights up the Clawd glyph for
# claude-code agents; without it, claude-code falls back to U+2605 (★).
#
# Source font lives at fonts/Clawd.ttf (vendored from
# https://github.com/kylesnowschwartz/dotfiles/tree/main/clawd-icon).
# Mascot likeness is a trademark of Anthropic.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FONT_SRC="$ROOT/fonts/Clawd.ttf"

if [[ ! -f "$FONT_SRC" ]]; then
  echo "error: $FONT_SRC missing" >&2
  exit 1
fi

case "$(uname -s)" in
Darwin)
  DEST_DIR="$HOME/Library/Fonts"
  ;;
Linux)
  DEST_DIR="$HOME/.local/share/fonts"
  ;;
*)
  echo "error: unsupported OS $(uname -s) — copy fonts/Clawd.ttf manually" >&2
  exit 1
  ;;
esac

mkdir -p "$DEST_DIR"
cp -f "$FONT_SRC" "$DEST_DIR/"
echo "installed -> $DEST_DIR/Clawd.ttf"

if [[ "$(uname -s)" == "Linux" ]] && command -v fc-cache >/dev/null 2>&1; then
  fc-cache -f "$DEST_DIR" >/dev/null
  echo "ran fc-cache"
fi

echo
echo "Restart the tcm server to pick up the font:"
echo "  just restart"
