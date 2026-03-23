#!/usr/bin/env bash
# opensessions hook for Claude Code
# Usage: opensessions-hook <event>
# Events: prompt-submit, stop, post-tool-use, notification, session-start, session-end
#
# Claude Code pipes JSON to stdin with session_id, tool_name, notification_type, etc.
# This script maps Claude Code events → opensessions AgentStatus and POSTs to the server.

set -euo pipefail

EVENT="${1:-}"
SERVER_URL="${OPENSESSIONS_URL:-http://127.0.0.1:7391/event}"
EVENTS_FILE="${OPENSESSIONS_EVENTS_FILE:-/tmp/opensessions-events.jsonl}"

# Read stdin (Claude Code sends JSON payload)
INPUT=""
if [ ! -t 0 ]; then
  INPUT=$(cat)
fi

# Get session name from tmux (or zellij, or fallback)
get_session() {
  if [ -n "${TMUX:-}" ]; then
    tmux display-message -p '#S' 2>/dev/null || echo "unknown"
  elif [ -n "${ZELLIJ_SESSION_NAME:-}" ]; then
    echo "$ZELLIJ_SESSION_NAME"
  else
    echo "unknown"
  fi
}

SESSION=$(get_session)

# Extract notification_type from JSON if present
NOTIFICATION_TYPE=""
if [ -n "$INPUT" ] && command -v jq &>/dev/null; then
  NOTIFICATION_TYPE=$(echo "$INPUT" | jq -r '.notification_type // empty' 2>/dev/null || true)
fi

# Map Claude Code event → opensessions status
map_status() {
  case "$EVENT" in
    prompt-submit)   echo "running" ;;
    post-tool-use)   echo "running" ;;
    session-start)   echo "idle" ;;
    session-end)     echo "done" ;;
    stop)            echo "idle" ;;
    notification)
      case "$NOTIFICATION_TYPE" in
        permission_prompt)     echo "waiting" ;;
        elicitation_dialog)    echo "waiting" ;;
        idle_prompt)           echo "idle" ;;
        *)                     echo "idle" ;;
      esac
      ;;
    *)               echo "" ;;
  esac
}

STATUS=$(map_status)
if [ -z "$STATUS" ]; then
  exit 0
fi

TIMESTAMP=$(($(date +%s) * 1000))
PAYLOAD=$(printf '{"agent":"claude-code","session":"%s","status":"%s","ts":%s}' "$SESSION" "$STATUS" "$TIMESTAMP")

# Try HTTP first, fall back to JSONL file
if command -v curl &>/dev/null; then
  curl -s -o /dev/null -X POST "$SERVER_URL" \
    -H 'Content-Type: application/json' \
    -d "$PAYLOAD" 2>/dev/null || \
    echo "$PAYLOAD" >> "$EVENTS_FILE" 2>/dev/null || true
else
  echo "$PAYLOAD" >> "$EVENTS_FILE" 2>/dev/null || true
fi

exit 0
