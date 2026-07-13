#!/bin/bash
# Committed counterpart of tcm-delegate.js's spawnScript(p). Behavior-frozen:
# see .agent-history or the workflow's git log for the extraction commit.
# Args: DIR NAME BRIEF_JSON OWNER
#   DIR        - absolute working directory for the spawned delegate
#   NAME       - kebab-case session name
#   BRIEF_JSON - the task brief as a JSON-encoded string (a single line, e.g.
#                "step one\nstep two"). The caller JSON-encodes it so the whole
#                brief travels as ONE shell token with no literal newlines — it
#                survives the agent's line-joining shell wrapper — and with no
#                quoting hazard for arbitrary content. Decoded back here with jq,
#                which replaces the old "agent writes the brief to a scratchpad
#                file first" step.
#   OWNER      - owning tmux session name, or "" to auto-detect via tmux
DIR="$1"
NAME="$2"
BRIEF_JSON="$3"
OWNER="$4"
BRIEF=$(printf '%s' "$BRIEF_JSON" | jq -r . 2>/dev/null)
if [ -z "$BRIEF" ]; then
  echo "SESSION=none PANE=none WINDOW=none OWNER=$OWNER ALIVE=no NOTE=brief-decode-failed"
  exit 0
fi
if [ -z "$OWNER" ]; then OWNER=$(tmux display-message -p '#{session_name}' 2>/dev/null || true); fi
RESP=$(curl -fsS -X POST localhost:7391/spawn-agent -H 'Content-Type: application/json' -d "$(jq -n --arg dir "$DIR" --arg name "$NAME" --arg owner "$OWNER" --arg pr "$BRIEF" '{dir:$dir, agent:"codex", prompt:$pr, name:$name, command:["codex","--profile","tcm-delegate","-c","mcp_servers.just.enabled=false"]} + (if $owner != "" then {ownerSession:$owner} else {} end)')")
if [ -z "$RESP" ]; then
  echo "SESSION=none PANE=none WINDOW=none OWNER=$OWNER ALIVE=no NOTE=spawn-request-failed"
  exit 0
fi
SESH=$(printf '%s' "$RESP" | jq -r .sessionName)
PANE=$(printf '%s' "$RESP" | jq -r .paneId)
WIN=$(printf '%s' "$RESP" | jq -r .windowId)
if [ -n "$OWNER" ] && [ "$SESH" != "$OWNER" ]; then OWNER=""; fi
sleep 2
if [ -n "$PANE" ] && [ "$PANE" != "none" ] && tmux display-message -p -t "$PANE" '#{pane_id}' >/dev/null 2>&1; then ALIVE=yes; else ALIVE=no; fi
echo "SESSION=$SESH PANE=$PANE WINDOW=$WIN OWNER=$OWNER ALIVE=$ALIVE NOTE=ok"
