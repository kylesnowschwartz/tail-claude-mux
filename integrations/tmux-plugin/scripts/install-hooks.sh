#!/usr/bin/env sh
# tcm tmux hook installer — declarative, owned by TPM init.
#
# Why this lives in tmux-config-land, not in the server:
# Hooks are static curl-POST strings that point at 127.0.0.1:7391. They have
# no runtime dependency on the server actually being up — curl fails-soft
# until the server boots a few hundred ms later. Installing hooks declaratively
# at TPM init removes the "tmux ready / server not booted" race that used to
# leave new tmux servers without working hooks (see fix/tmux-cold-start-determinism).
#
# This file is the single source of truth for the tmux hook set. Lockstep:
# every hook installed here must be unset in
#   integrations/tmux-plugin/scripts/uninstall.sh
# with the matching scope (-gu vs -guw).

set -e

PORT="${TCM_PORT:-7391}"
HOST="${TCM_HOST:-127.0.0.1}"
BASE="http://${HOST}:${PORT}"

# Build a `run-shell -b "curl ..."` body. tmux fires hooks under sh -c, so the
# escape budget here is "the outer set-hook quoting + the inner curl quoting".
# We use single quotes around the data block (`-d '...'`) so #{...} formats
# expand at hook-fire time without shell variable interpolation.
post_no_data() {
  printf 'run-shell -b "curl -s -o /dev/null -X POST %s%s >/dev/null 2>&1 || true"' "$BASE" "$1"
}
post_with_data() {
  printf "run-shell -b \"curl -s -o /dev/null -X POST %s%s -d '%s' >/dev/null 2>&1 || true\"" "$BASE" "$1" "$2"
}

CTX='#{client_tty}|#{session_name}|#{window_id}'
FOCUS_CMD="$(post_with_data /focus "$CTX")"
ENSURE_CMD="$(post_with_data /ensure-sidebar "$CTX")"
REFRESH_CMD="$(post_no_data /refresh)"
RESIZED_CMD="$(post_no_data /client-resized)"
PANE_EXITED_CMD="$(post_no_data /pane-exited)"
PANE_FOCUS_CMD="$(post_with_data /pane-focus '#{pane_id}')"

# Global hooks (-g): fire from any client, scoped to the server.
# Note: client-session-changed runs both /focus and /ensure-sidebar so a
# session switch repaints the panel AND auto-spawns the sidebar in the new
# session's active window if visibility is on.
tmux set-hook -g client-session-changed "$FOCUS_CMD ; $ENSURE_CMD ; $PANE_FOCUS_CMD"
tmux set-hook -g session-created        "$REFRESH_CMD"
tmux set-hook -g session-closed         "$REFRESH_CMD"
tmux set-hook -g after-select-window    "$ENSURE_CMD"
tmux set-hook -g after-new-window       "$ENSURE_CMD"
tmux set-hook -g client-resized         "$RESIZED_CMD"
tmux set-hook -g after-kill-pane        "$PANE_EXITED_CMD"

# Focused-pane tracking (/pane-focus → the TUI's FOCUS chip + agent-row
# marker). On tmux 3.7b pane-focus-in does NOT fire when the active pane
# changes under an already-focused client (select-pane, clicks, prefix
# navigation — verified empirically), so window-pane-changed is the
# workhorse; session-window-changed covers window switches,
# client-session-changed above covers session switches, and
# client-focus-in re-reports when the terminal itself regains OS focus.
# #{pane_id} expands to the relevant client's active pane at fire time.
tmux set-hook -g window-pane-changed    "$PANE_FOCUS_CMD"
tmux set-hook -g session-window-changed "$PANE_FOCUS_CMD"
tmux set-hook -g client-focus-in        "$PANE_FOCUS_CMD"

# Window-scoped global hooks (-gw): tmux requires this scope for pane-level
# events. Two hooks fire on pane death: pane-exited (process exits cleanly)
# and after-kill-pane (above; pane killed via kill-pane command). Both route
# to /pane-exited so the runtime can prune orphaned sidebar panes.
tmux set-hook -gw pane-exited     "$PANE_EXITED_CMD"
tmux set-hook -gw pane-focus-in   "$PANE_FOCUS_CMD"
