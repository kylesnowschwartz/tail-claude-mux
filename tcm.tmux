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

# --- Bootstrap: tie bun-server lifetime to tmux-server lifetime (1:1) ---
#
# Why unconditional: the bun server holds in-memory caches (palette diff,
# sidebar visibility, sessionProviders map) that are only valid for the tmux
# server it bootstrapped against. After `tmux kill-server`, those caches
# diverge from the new tmux server's empty option store — hooks gone, palette
# tokens gone — and the long-lived bun process refuses to re-emit because its
# diff cache says "already wrote that". Killing on every TPM init guarantees
# the bun server boots fresh whenever tmux does, mirroring the catppuccin/tmux
# pattern where TPM init = source of truth.
#
# Persistent state worth keeping (theme, sidebar width, session order, agent
# metadata) lives on disk under ~/.config/tcm/*.json and rehydrates on the
# fresh boot — only ephemeral in-memory caches are lost.
# Verify the PID file actually points at our bun server before killing.
# /tmp is per-boot on macOS, but Linux keeps it around — a stale PID file
# from a previous crash could now point at someone else's process whose PID
# was reused. Match against the server entry path so we only kill our own.
SERVER_ENTRY="$CURRENT_DIR/apps/server/src/main.ts"
if [ -f /tmp/tcm.pid ]; then
  OLD_PID="$(cat /tmp/tcm.pid 2>/dev/null)"
  if [ -n "$OLD_PID" ] && ps -p "$OLD_PID" -o command= 2>/dev/null | grep -qF "$SERVER_ENTRY"; then
    kill "$OLD_PID" 2>/dev/null || true
    # Wait up to 1s for the kill to actually release port 7391. `kill` is
    # async; without this wait the OLD process is still answering on the
    # port and the spawn below would skip via ensure_server's alive-check,
    # leaving us with hooks installed but no daemon (Codex review F1).
    i=0
    while [ "$i" -lt 20 ]; do
      kill -0 "$OLD_PID" 2>/dev/null || break
      sleep 0.05
      i=$((i + 1))
    done
  fi
  rm -f /tmp/tcm.pid
fi
rm -f /tmp/tcm.version  # legacy version-file no longer used; clean up if present

# --- Bootstrap: install deps if needed ---
if [ ! -d "$CURRENT_DIR/node_modules" ]; then
  BUN_PATH="$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")"
  if [ -x "$BUN_PATH" ]; then
    (cd "$CURRENT_DIR" && "$BUN_PATH" install --frozen-lockfile 2>/tmp/tcm-install.log) &
  fi
fi

# --- Bootstrap: install tmux hooks declaratively (catppuccin pattern) ---
# Hooks are static curl POSTs; they don't need the bun server to be alive at
# install time — curl fails-soft until the server boots. Installing them here
# (rather than from the bun server's setupHooks()) removes the "tmux ready /
# bun not booted" race that used to leave new tmux servers without hooks.
sh "$SCRIPTS_DIR/install-hooks.sh" >/dev/null 2>&1 || true

# --- Bootstrap: spawn the bun server in the background ---
# Spawn directly rather than going through ensure_server's alive-check.
# We just killed the previous server above, but its port may not be released
# until the kernel finishes the FIN/CLOSE_WAIT cycle (a few ms after the
# process exits). ensure_server's `server_alive` probe could see a stale
# response and decline to spawn, which is wrong here — we KNOW we want a
# fresh server. Other call sites (focus.sh, toggle.sh) keep using
# ensure_server because they don't know whether one is running.
BUN_PATH="$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")"
if [ -x "$BUN_PATH" ] && [ -f "$SERVER_ENTRY" ]; then
  "$BUN_PATH" run "$SERVER_ENTRY" >/dev/null 2>&1 &
fi

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
# When @tcm-header == "on", populate the palette options first (so the format
# strings paint correctly on first repaint) and then source the header.
# Active palette wins over vendored fallback; both are sourced with `-q` so a
# missing file doesn't error — first-ever attach hits the fallback only.
HEADER_ENABLED=$(get_option "@tcm-header" "off")
if [ "$HEADER_ENABLED" = "on" ]; then
  THEMES_DIR="$CURRENT_DIR/integrations/tmux-plugin/themes"
  ACTIVE_PALETTE="$HOME/.config/tcm/palette-active.tmux.conf"
  tmux source-file -q "$THEMES_DIR/default-palette.tmux.conf"
  [ -f "$ACTIVE_PALETTE" ] && tmux source-file -q "$ACTIVE_PALETTE"
  tmux source-file "$SCRIPTS_DIR/header.tmux"
fi
