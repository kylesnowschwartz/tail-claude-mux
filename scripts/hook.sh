#!/usr/bin/env bash
# Called by Claude Code as a lifecycle hook.
# Reads JSON payload from stdin, merges the event name + parent pid + ps
# snapshot, and POSTs to tcm. Fails silently — hooks must never block the agent.
set -o pipefail

EVENT="${1:-unknown}"
PAYLOAD=$(cat)

# $PPID is the `sh -c` wrapper Claude spawned us in — short-lived. The runtime
# walks ancestry from this pid up to the long-lived `claude` process using the
# snapshot below. See packages/runtime/src/agents/resolve-agent-pid.ts.
HOOK_PID="$PPID"

# Snapshot the local process table so the server can resolve the long-lived
# agent pid without an extra round-trip / racing the wrapper-shell exit.
# `-axww` = all users, wide output, all args. Trim to ~250KB worst case.
PS_SNAPSHOT=$(ps -axww -o pid=,ppid=,command= 2>/dev/null | head -c 200000)
PS_SNAPSHOT_JSON=$(printf '%s' "$PS_SNAPSHOT" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))' 2>/dev/null)
if [[ -z "$PS_SNAPSHOT_JSON" ]]; then
  # python3 unavailable — drop the snapshot rather than ship unescaped data.
  PS_SNAPSHOT_JSON='""'
fi

# Build the request body: merge event name + pid + snapshot into the stdin JSON.
# If payload is missing or malformed, send just the metadata.
INJECTED="\"event\":\"$EVENT\",\"pid\":$HOOK_PID,\"process_snapshot\":$PS_SNAPSHOT_JSON"
if [[ "$PAYLOAD" == "{"* ]]; then
  BODY="{${INJECTED},${PAYLOAD#\{}"
else
  BODY="{${INJECTED}}"
fi

curl -s -X POST "http://127.0.0.1:${TCM_PORT:-7391}/hook" \
  -H "Content-Type: application/json" \
  -d "$BODY" \
  --connect-timeout 1 --max-time 2 2>/dev/null || true
