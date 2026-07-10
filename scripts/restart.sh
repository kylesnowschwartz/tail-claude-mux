#!/usr/bin/env zsh
# Build TUI, restart server (or cold-start), wait for ready.
# Idempotent — safe to run from any state.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUN="${BUN_PATH:-$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")}"
PORT="${TCM_PORT:-7391}"
HOST="${TCM_HOST:-127.0.0.1}"
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

wait_for_server_gone() {
  for i in {1..30}; do
    server_alive || return 0
    sleep 0.1
  done
  return 1
}

# restart_decision figures out whether POST /restart would actually land the
# binary we just built. restartInPlace (apps/server-go/cmd/tcm-server/main.go)
# re-execs os.Executable() — the path the LIVE server was originally launched
# from. If that's a different checkout (e.g. you ran `just restart` from a
# worktree other than the one that launched the live server), /restart
# silently re-execs the OLD binary and the new build never goes live.
#
# We identify the live server's launch path via lsof (txt = the process's
# executable mapping) and compare it to $GO_BIN by device+inode via stat —
# not by comparing to the binary we just rebuilt in isolation, but by
# resolving the *current* directory entry at that path, which is exactly
# what a fresh `stat` on the path gives us. Sets DECISION to "restart" or
# "cold-start", and REASON to a plain-language note (empty when unremarkable).
#
# Inconclusive discovery (lsof missing or unable to inspect the listener)
# falls back to COLD-START: POST /restart against an unidentified build is
# exactly the silent stale-binary bug this function guards against, while
# an unnecessary cold-start merely costs a stop/start.
restart_decision() {
  local live_pid live_exe
  live_pid="$(lsof -ti "tcp:${PORT}" -sTCP:LISTEN 2>/dev/null | head -n1 || true)"
  if [[ -z "$live_pid" ]]; then
    DECISION="cold-start"
    REASON="couldn't identify the live server's build; cold-starting to be safe"
    return
  fi
  live_exe="$(lsof -p "$live_pid" -a -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | grep -F "/tcm-server" | head -n1 || true)"
  if [[ -z "$live_exe" ]]; then
    DECISION="cold-start"
    REASON="couldn't identify the live server's build; cold-starting to be safe"
    return
  fi
  if [[ "$(stat -f '%d %i' "$live_exe" 2>/dev/null)" == "$(stat -f '%d %i' "$GO_BIN" 2>/dev/null)" ]]; then
    DECISION="restart"
    REASON=""
  else
    DECISION="cold-start"
    REASON="live server was running a different build ($live_exe); cold-starting this one"
  fi
}

# 1. Build TUI
echo "==> Building TUI..."
cd "$ROOT/apps/tui" && "$BUN" run build

# 1b. Build the Go server (the only backend — bun server retired).
GO_BIN="$ROOT/apps/server-go/bin/tcm-server"
echo "==> Building Go server..."
(cd "$ROOT/apps/server-go" && GOWORK=off go build -o bin/tcm-server ./cmd/tcm-server)

# 2. Restart or cold-start the server
if server_alive; then
  restart_decision
  if [[ "$DECISION" == "cold-start" ]]; then
    echo "==> ${REASON}"
    "$SCRIPT_DIR/stop.sh"
    if ! wait_for_server_gone; then
      echo "error: old server did not stop within 3s" >&2
      exit 1
    fi
    "$GO_BIN" >>/tmp/tcm-server-go.log 2>&1 &
  else
    [[ -n "$REASON" ]] && echo "==> ${REASON}"
    echo "==> Restarting server..."
    curl -s -o /dev/null -X POST "${BASE}/restart"
  fi
else
  echo "==> Server not running, starting fresh..."
  "$GO_BIN" >>/tmp/tcm-server-go.log 2>&1 &
fi

# 3. Wait for the new server
wait_for_server

# 4. Focus the sidebar pane (server respawns sidebars async, so poll)
WINDOW_ID="$(tmux display-message -p '#{window_id}' 2>/dev/null || true)"
if [[ -n "$WINDOW_ID" ]]; then
  for i in {1..40}; do
    PANE_ID=$(tmux list-panes -t "$WINDOW_ID" -F '#{pane_id} #{@tcm-sidebar} #{pane_title}' 2>/dev/null |
      awk '$2 == "1" || $3 == "tcm-sidebar" { print $1; exit }')
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
#    @tcm-header opt-in so we never write status-line options when
#    the header is disabled. Tolerant of "not in tmux" — show-option then
#    returns empty and we skip.
HEADER_ENABLED="$(tmux show-option -gv '@tcm-header' 2>/dev/null || true)"
if [[ "$HEADER_ENABLED" == "on" ]]; then
  HEADER_FILE="$ROOT/integrations/tmux-plugin/scripts/header.tmux"
  if [[ -f "$HEADER_FILE" ]] && tmux source-file "$HEADER_FILE" 2>/dev/null; then
    echo "==> Re-sourced tmux header."
  fi
fi

echo "==> Ready."
