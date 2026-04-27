#!/usr/bin/env zsh
# Build TUI, restart server (or cold-start), wait for ready.
# Idempotent — safe to run from any state.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUN="${BUN_PATH:-$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")}"
PORT="${OPENSESSIONS_PORT:-7391}"
HOST="${OPENSESSIONS_HOST:-127.0.0.1}"
BASE="http://${HOST}:${PORT}"

server_alive() {
  curl -s -o /dev/null -m 0.3 "${BASE}/" 2>/dev/null
}

wait_for_server() {
  for i in {1..30}; do
    server_alive && return 0
    sleep 0.1
  done
  echo "error: server did not come up within 3s" >&2
  return 1
}

# 1. Build TUI
echo "==> Building TUI..."
cd "$ROOT/apps/tui" && "$BUN" run build

# 2. Restart or cold-start the server
if server_alive; then
  echo "==> Restarting server..."
  curl -s -o /dev/null -X POST "${BASE}/restart"
else
  echo "==> Server not running, starting fresh..."
  "$BUN" run "$ROOT/apps/server/src/main.ts" >/dev/null 2>&1 &
fi

# 3. Wait for the new server
wait_for_server

# 4. Focus the sidebar pane (server respawns sidebars async, so poll)
WINDOW_ID="$(tmux display-message -p '#{window_id}' 2>/dev/null || true)"
if [[ -n "$WINDOW_ID" ]]; then
  for i in {1..40}; do
    PANE_ID=$(tmux list-panes -t "$WINDOW_ID" -F '#{pane_id} #{pane_title}' 2>/dev/null |
      awk '$2 == "opensessions-sidebar" { print $1; exit }')
    if [[ -n "$PANE_ID" ]]; then
      tmux select-pane -t "$PANE_ID" >/dev/null 2>&1
      break
    fi
    sleep 0.05
  done
fi

# 5. Re-source the tmux header so on-disk format-string and theme-token edits
#    take effect immediately. The plugin entry sources header.tmux once at TPM
#    load; without this step `just restart` would leave the live tmux session
#    holding the previous format strings until prefix+r. Gated on the user's
#    @opensessions-header opt-in so we never write status-line options when
#    the header is disabled. Tolerant of "not in tmux" — show-option then
#    returns empty and we skip.
HEADER_ENABLED="$(tmux show-option -gv '@opensessions-header' 2>/dev/null || true)"
if [[ "$HEADER_ENABLED" == "on" ]]; then
  HEADER_FILE="$ROOT/integrations/tmux-plugin/scripts/header.tmux"
  if [[ -f "$HEADER_FILE" ]] && tmux source-file "$HEADER_FILE" 2>/dev/null; then
    echo "==> Re-sourced tmux header."
  fi
fi

echo "==> Ready."
