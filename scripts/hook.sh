#!/usr/bin/env bash
# Called by Claude Code as a lifecycle hook.
# Reads JSON payload from stdin, merges the event name, and POSTs to opensessions.
# Fails silently — hooks must never block the agent.
set -o pipefail

EVENT="${1:-unknown}"
PAYLOAD=$(cat)

# Build the request body: merge event name into the stdin JSON payload.
# If payload is missing or malformed, send just the event name.
if [[ "$PAYLOAD" == "{"* ]]; then
  BODY="{\"event\":\"$EVENT\",${PAYLOAD#\{}"
else
  BODY="{\"event\":\"$EVENT\"}"
fi

curl -s -X POST "http://127.0.0.1:${TCM_PORT:-7391}/hook" \
  -H "Content-Type: application/json" \
  -d "$BODY" \
  --connect-timeout 1 --max-time 2 2>/dev/null || true
