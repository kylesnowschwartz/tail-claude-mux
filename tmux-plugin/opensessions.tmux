#!/usr/bin/env bash
# opensessions.tmux — TPM entry point
# Registers keybindings and bootstraps the TUI if needed.
#
# Install:
#   1. Add to .tmux.conf:  set -g @plugin 'palanikannan1437/opensessions'
#   2. Press prefix + I to install
#   3. Requires: bun (https://bun.sh)
#
# Options (set before TPM init):
#   @opensessions-key       "s"    — prefix + key to toggle sidebar
#   @opensessions-focus-key "S"    — prefix + key to reveal and focus sidebar
#   @opensessions-prefix-key        "o"  — prefix + key to enter opensessions command table
#   @opensessions-prefix-focus-key  "s"  — command-table key to reveal and focus sidebar
#   @opensessions-prefix-toggle-key "t"  — command-table key to toggle sidebar
#   @opensessions-prefix-index-keys "1 2 3 4 5 6 7 8 9" — command-table keys mapped to visible sessions 1..9
#   @opensessions-focus-global-key  ""  — optional no-prefix key to reveal and focus sidebar
#   @opensessions-index-keys        ""  — optional no-prefix keys mapped to visible sessions 1..9
#   @opensessions-width     "26"   — sidebar width in columns

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS_DIR="$CURRENT_DIR/scripts"

# --- Read user options with defaults ---

get_option() {
  local option="$1"
  local default="$2"
  local value
  value=$(tmux show-option -gqv "$option" 2>/dev/null)
  echo "${value:-$default}"
}

KEY=$(get_option "@opensessions-key" "s")
FOCUS_KEY=$(get_option "@opensessions-focus-key" "S")
PREFIX_KEY=$(get_option "@opensessions-prefix-key" "o")
PREFIX_FOCUS_KEY=$(get_option "@opensessions-prefix-focus-key" "s")
PREFIX_TOGGLE_KEY=$(get_option "@opensessions-prefix-toggle-key" "t")
PREFIX_INDEX_KEYS=$(get_option "@opensessions-prefix-index-keys" "1 2 3 4 5 6 7 8 9")
FOCUS_GLOBAL_KEY=$(get_option "@opensessions-focus-global-key" "")
INDEX_KEYS=$(get_option "@opensessions-index-keys" "")
WIDTH=$(get_option "@opensessions-width" "26")
COMMAND_TABLE="opensessions"

bind_prefixed_key() {
  local key="$1"
  local command="$2"
  [ -n "$key" ] || return
  tmux bind-key "$key" run-shell "$command"
}

bind_global_key() {
  local key="$1"
  local command="$2"
  [ -n "$key" ] || return
  tmux bind-key -n "$key" run-shell "$command"
}

bind_index_keys() {
  local index=1
  local key
  for key in $INDEX_KEYS; do
    [ "$index" -le 9 ] || break
    tmux bind-key -n "$key" run-shell "sh '$SCRIPTS_DIR/switch-index.sh' $index"
    index=$((index + 1))
  done
}

bind_table_key() {
  local table="$1"
  local key="$2"
  local command="$3"
  [ -n "$key" ] || return
  tmux bind-key -T "$table" "$key" run-shell "$command"
}

bind_table_index_keys() {
  local index=1
  local key
  for key in $PREFIX_INDEX_KEYS; do
    [ "$index" -le 9 ] || break
    tmux bind-key -T "$COMMAND_TABLE" "$key" run-shell "sh '$SCRIPTS_DIR/switch-index.sh' $index"
    index=$((index + 1))
  done
}

bind_command_table() {
  [ -n "$PREFIX_KEY" ] || return
  tmux bind-key "$PREFIX_KEY" switch-client -T "$COMMAND_TABLE"
  tmux bind-key -T "$COMMAND_TABLE" Any switch-client -T root
  bind_table_key "$COMMAND_TABLE" "$PREFIX_FOCUS_KEY" "sh '$SCRIPTS_DIR/focus.sh'"
  bind_table_key "$COMMAND_TABLE" "$PREFIX_TOGGLE_KEY" "sh '$SCRIPTS_DIR/toggle.sh'"
  bind_table_index_keys
}

# Export so scripts can read them
tmux set-environment -g OPENSESSIONS_DIR "$CURRENT_DIR"
tmux set-environment -g OPENSESSIONS_WIDTH "$WIDTH"

# --- Bootstrap: install deps if needed ---
if [ ! -d "$CURRENT_DIR/packages/tui/node_modules" ]; then
  BUN_PATH="$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")"
  if [ -x "$BUN_PATH" ]; then
    (cd "$CURRENT_DIR" && "$BUN_PATH" install --frozen-lockfile 2>/tmp/opensessions-install.log) &
  fi
fi

# --- Bind tmux shortcuts ---
bind_prefixed_key "$KEY" "sh '$SCRIPTS_DIR/toggle.sh'"
bind_prefixed_key "$FOCUS_KEY" "sh '$SCRIPTS_DIR/focus.sh'"
bind_command_table
bind_global_key "$FOCUS_GLOBAL_KEY" "sh '$SCRIPTS_DIR/focus.sh'"
bind_index_keys
