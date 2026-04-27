#!/usr/bin/env zsh
# Stop server and all sidebars. Tries graceful quit first, then kills.
set -euo pipefail

PORT="${TCM_PORT:-7391}"
HOST="${TCM_HOST:-127.0.0.1}"
BASE="http://${HOST}:${PORT}"

# Graceful quit via server endpoint
if curl -s -o /dev/null -m 0.3 "${BASE}/" 2>/dev/null; then
  echo "==> Quitting via server..."
  curl -s -o /dev/null -X POST "${BASE}/quit"
  sleep 0.3
fi

# Clean up anything the graceful quit missed
if [[ -f /tmp/tcm.pid ]]; then
  kill "$(cat /tmp/tcm.pid)" 2>/dev/null || true
  rm -f /tmp/tcm.pid
fi
pkill -f "bun.*apps/server/src/main.ts" 2>/dev/null || true

echo "==> Stopped."
