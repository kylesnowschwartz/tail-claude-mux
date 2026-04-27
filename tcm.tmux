#!/usr/bin/env bash
# tcm.tmux — TPM entry point
# Registers keybindings and bootstraps the TUI if needed.
#
# Install:
#   1. Add to .tmux.conf:  set -g @plugin 'kylesnowschwartz/tail-claude-mux'
#   2. Press prefix + I to install
#   3. Requires: bun (https://bun.sh)
#
# Default keybindings:
#   prefix + o → s   — reveal and focus sidebar
#   prefix + o → t   — toggle sidebar
#   prefix + o → 1-9 — switch to visible session by index
#
# Options (set before TPM init):
#   @tcm-prefix-key        "o"  — prefix + key to enter tcm command table
#   @tcm-focus-global-key  ""   — optional no-prefix key to reveal and focus sidebar
#   @tcm-index-keys        ""   — optional no-prefix keys mapped to visible sessions 1..9
#   @tcm-width             "26" — sidebar width in columns
#   @tcm-header            "off" — set to "on" to apply the tcm
#                                            theme + per-window agent glyphs to the
#                                            tmux status line (see docs/specs/tmux-header.md)

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_DIR="$CURRENT_DIR/integrations/tmux-plugin/scripts"

# --- Read user options with defaults ---

get_option() {
  local option="$1"
  local default="$2"
  local value
  value=$(tmux show-option -gqv "$option" 2>/dev/null)
  echo "${value:-$default}"
}

PREFIX_KEY=$(get_option "@tcm-prefix-key" "o")
FOCUS_GLOBAL_KEY=$(get_option "@tcm-focus-global-key" "")
INDEX_KEYS=$(get_option "@tcm-index-keys" "")
WIDTH=$(get_option "@tcm-width" "26")
COMMAND_TABLE="tcm"

bind_global_key() {
  local key="$1"
  local command="$2"
  [ -n "$key" ] || return
  tmux bind-key -n "$key" run-shell "$command"
}

bind_global_index_keys() {
  local index=1
  local key
  for key in $INDEX_KEYS; do
    [ "$index" -le 9 ] || break
    tmux bind-key -n "$key" run-shell "sh '$SCRIPTS_DIR/switch-index.sh' $index"
    index=$((index + 1))
  done
}

# Export so scripts can read them
tmux set-environment -g TCM_DIR "$CURRENT_DIR"
tmux set-environment -g TCM_WIDTH "$WIDTH"

# --- Bootstrap: kill stale server if version or install path changed ---
VERSION_FILE="/tmp/tcm.version"
CURRENT_VERSION="${CURRENT_DIR}:$(grep -o '"version": *"[^"]*"' "$CURRENT_DIR/package.json" 2>/dev/null | head -1 | cut -d'"' -f4)"
RUNNING_VERSION=""
[ -f "$VERSION_FILE" ] && RUNNING_VERSION=$(cat "$VERSION_FILE" 2>/dev/null)

if [ "$CURRENT_VERSION" != "$RUNNING_VERSION" ] && [ -f /tmp/tcm.pid ]; then
  kill "$(cat /tmp/tcm.pid)" 2>/dev/null || true
  rm -f /tmp/tcm.pid
fi
echo -n "$CURRENT_VERSION" > "$VERSION_FILE"

# --- Bootstrap: install deps if needed ---
if [ ! -d "$CURRENT_DIR/node_modules" ]; then
  BUN_PATH="$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")"
  if [ -x "$BUN_PATH" ]; then
    (cd "$CURRENT_DIR" && "$BUN_PATH" install --frozen-lockfile 2>/tmp/tcm-install.log) &
  fi
fi

# --- Bootstrap: spawn the server in the background ---
# Without this, the TCM server only starts when the user presses prefix+o,
# s/t — meaning a fresh tmux-server attach has no agent watcher and no tmux
# hooks installed (the server installs them via setupHooks() at boot).
# Result: agents would not appear in the sidebar until the first keypress,
# and switching sessions would not auto-spawn the sidebar in new windows.
#
# Fire-and-forget: ensure_server() inside server-common.sh bails fast if the
# server is already alive, and exits silently if bun is unavailable.
( . "$SCRIPTS_DIR/server-common.sh" && ensure_server ) >/dev/null 2>&1 &

# --- Bind tmux shortcuts ---

# Command table for manual use: prefix o → s/t/1-9
if [ -n "$PREFIX_KEY" ]; then
  tmux bind-key "$PREFIX_KEY" switch-client -T "$COMMAND_TABLE"
  tmux bind-key -T "$COMMAND_TABLE" Any switch-client -T root
  tmux bind-key -T "$COMMAND_TABLE" s run-shell "sh '$SCRIPTS_DIR/focus.sh'"
  tmux bind-key -T "$COMMAND_TABLE" t run-shell "sh '$SCRIPTS_DIR/toggle.sh'"
  for i in 1 2 3 4 5 6 7 8 9; do
    tmux bind-key -T "$COMMAND_TABLE" "$i" run-shell "sh '$SCRIPTS_DIR/switch-index.sh' $i"
  done
fi

# Direct prefix bindings for programmatic use (terminal emulator shortcuts).
# C-s/C-t are single-byte Ctrl codes; M-1..9 are 2-byte Alt sequences.
# Both are safe to send as text from terminal emulators without timing issues.
tmux bind-key C-s run-shell "sh '$SCRIPTS_DIR/focus.sh'"
tmux bind-key C-t run-shell "sh '$SCRIPTS_DIR/toggle.sh'"
for i in 1 2 3 4 5 6 7 8 9; do
  tmux bind-key "M-$i" run-shell "sh '$SCRIPTS_DIR/switch-index.sh' $i"
done

bind_global_key "$FOCUS_GLOBAL_KEY" "sh '$SCRIPTS_DIR/focus.sh'"
bind_global_index_keys

# --- Status-line header (opt-in) ---
# When @tcm-header == "on", apply the tcm theme + agent
# glyphs to tmux's status line. The tcm server writes @tcm-thm-*
# and @tcm-agent* options that header.sh reads. See docs/specs/tmux-header.md.
HEADER_ENABLED=$(get_option "@tcm-header" "off")
if [ "$HEADER_ENABLED" = "on" ]; then
  tmux source-file "$SCRIPTS_DIR/header.tmux"
fi
