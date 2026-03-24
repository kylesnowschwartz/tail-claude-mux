#!/usr/bin/env bash
# Toggle the opensessions sidebar via the server.
# Ensures the server is running first, then calls POST /toggle.

PORT="${OPENSESSIONS_PORT:-7391}"
HOST="${OPENSESSIONS_HOST:-127.0.0.1}"

PLUGIN_DIR="$(tmux show-environment -g OPENSESSIONS_DIR 2>/dev/null | cut -d= -f2)"
PLUGIN_DIR="${PLUGIN_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"
BUN_PATH="${BUN_PATH:-$(command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun")}"
SERVER_ENTRY="$PLUGIN_DIR/packages/core/src/server/start.ts"
PID_FILE="/tmp/opensessions.pid"

# --- Ensure server is running ---
server_alive() {
    curl -s -o /dev/null -m 0.2 "http://${HOST}:${PORT}/" 2>/dev/null
}

if ! server_alive; then
    # Start server in background
    "$BUN_PATH" run "$SERVER_ENTRY" &>/dev/null &
    disown
    # Wait up to 3s for it to come up
    for i in $(seq 1 30); do
        sleep 0.1
        server_alive && break
    done
fi

# --- Toggle sidebar (pass clientTty|session|window context) ---
CTX=$(tmux display-message -p '#{client_tty}|#{session_name}|#{window_id}' 2>/dev/null)
curl -s -o /dev/null -X POST "http://${HOST}:${PORT}/toggle" -d "$CTX"
