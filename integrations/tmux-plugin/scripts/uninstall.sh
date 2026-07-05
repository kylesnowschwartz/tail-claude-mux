#!/usr/bin/env sh
# tcm uninstall — clean up all tmux hooks, keybindings, sidebar panes, and env vars
# Run this BEFORE removing the plugin files.
#
# Usage:
#   sh /path/to/tcm/integrations/tmux-plugin/scripts/uninstall.sh

set -e

echo "tcm: uninstalling..."

# --- Remove global hooks ---
# Keep this list in lockstep with
#   integrations/tmux-plugin/scripts/install-hooks.sh
# (the single source of truth for the tmux hook set). Every hook installed
# there must be unset here with the matching scope (-gu vs -guw).
for hook in \
  client-session-changed \
  session-created \
  session-closed \
  client-resized \
  after-select-window \
  after-new-window \
  after-kill-pane; do
  tmux set-hook -gu "$hook" 2>/dev/null || true
done
for whook in \
  pane-exited \
  pane-focus-in; do
  tmux set-hook -guw "$whook" 2>/dev/null || true
done
echo "  ✓ removed global hooks"

# --- Kill sidebar + companion panes ---
# Identify by @tcm-sidebar/@tcm-companion pane-local options (primary) +
# pane_title fallback.
managed_panes=$(tmux list-panes -a -F '#{pane_id} #{@tcm-sidebar} #{@tcm-companion} #{pane_title}' 2>/dev/null \
  | awk '$2 == "1" || $3 == "1" || $4 == "tcm-sidebar" || $4 == "tcm-companion" { print $1 }') || true
if [ -n "$managed_panes" ]; then
  for pane in $managed_panes; do
    tmux kill-pane -t "$pane" 2>/dev/null || true
  done
  echo "  ✓ killed sidebar/companion panes"
fi

# --- Kill stash session ---
tmux kill-session -t "_tcm_stash" 2>/dev/null || true
echo "  ✓ removed stash session"

# --- Kill the server ---
PORT="${TCM_PORT:-7391}"
HOST="${TCM_HOST:-127.0.0.1}"
curl -s -o /dev/null -X POST "http://${HOST}:${PORT}/shutdown" 2>/dev/null || true
echo "  ✓ stopped server (if running)"

# --- Remove keybindings ---
# Command table bindings
PREFIX_KEY=$(tmux show-option -gqv "@tcm-prefix-key" 2>/dev/null)
PREFIX_KEY="${PREFIX_KEY:-o}"
tmux unbind-key "$PREFIX_KEY" 2>/dev/null || true

# Direct prefix bindings
tmux unbind-key C-s 2>/dev/null || true
tmux unbind-key C-t 2>/dev/null || true
for i in 1 2 3 4 5 6 7 8 9; do
  tmux unbind-key "M-$i" 2>/dev/null || true
done

# Global keys (if configured)
FOCUS_GLOBAL_KEY=$(tmux show-option -gqv "@tcm-focus-global-key" 2>/dev/null)
if [ -n "$FOCUS_GLOBAL_KEY" ]; then
  tmux unbind-key -n "$FOCUS_GLOBAL_KEY" 2>/dev/null || true
fi
INDEX_KEYS=$(tmux show-option -gqv "@tcm-index-keys" 2>/dev/null)
for key in $INDEX_KEYS; do
  tmux unbind-key -n "$key" 2>/dev/null || true
done
echo "  ✓ removed keybindings"

# --- Remove environment variables ---
tmux set-environment -gu TCM_DIR 2>/dev/null || true
tmux set-environment -gu TCM_WIDTH 2>/dev/null || true
echo "  ✓ removed environment variables"

# --- Remove palette options + active palette file ---
# tcm.tmux re-applies these on every TPM init via source-file. Unset them so
# a stale palette doesn't linger across an uninstall → reinstall cycle.
for tok in base text blue surface0 surface2 overlay0 yellow red green; do
  tmux set-option -gu "@tcm-thm-$tok" 2>/dev/null || true
done
tmux set-option -gu @tcm-shell-glyph 2>/dev/null || true
tmux set-option -gu @tcm-last-window-glyph 2>/dev/null || true
rm -f "$HOME/.config/tcm/palette-active.tmux.conf"
echo "  ✓ removed palette options and active palette file"

echo "tcm: uninstall complete. You can now remove the plugin files."
echo "  If using TPM: remove the line from .tmux.conf and run prefix + alt + u"
