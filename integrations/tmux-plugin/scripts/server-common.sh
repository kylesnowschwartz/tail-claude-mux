#!/usr/bin/env sh

PORT="${TCM_PORT:-7391}"
HOST="${TCM_HOST:-127.0.0.1}"

PLUGIN_DIR="$(tmux show-environment -g TCM_DIR 2>/dev/null | cut -d= -f2)"
PLUGIN_DIR="${PLUGIN_DIR:-$(cd "$SCRIPT_DIR/../../.." && pwd)}"
SERVER_BIN="$PLUGIN_DIR/apps/server-go/bin/tcm-server"

server_alive() {
  curl -s -o /dev/null -m 0.2 "http://${HOST}:${PORT}/" 2>/dev/null
}

ensure_server() {
  if server_alive; then
    return 0
  fi

  # The Go binary is the only backend (bun server retired). A missing
  # binary is a build error — scripts/restart.sh builds it.
  [ -x "$SERVER_BIN" ] || return 1

  "$SERVER_BIN" >>/tmp/tcm-server-go.log 2>&1 &

  attempt=0
  while [ "$attempt" -lt 30 ]; do
    sleep 0.1
    if server_alive; then
      return 0
    fi
    attempt=$((attempt + 1))
  done

  return 1
}
