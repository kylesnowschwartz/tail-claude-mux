#!/usr/bin/env bash
set -euo pipefail

usage='start-peer-session.sh --repo <dir> --session <tmux-session-name> --prompt-file <file> [--attach-existing] [--close-on-exit] [--agent claude|pi] [--window <name>] [--name <name>] [--session-id <uuid>] [--permission-mode <mode>] [--ready-timeout <seconds>] [--port <tcm port>]'
repo='' session='' prompt_file='' pane='' ready='' peer_name='' window='' permission_mode='' name='' session_id='' pane_tail=''
agent=claude attach=0 close_on_exit=0 tcm_seen=0 session_id_set=0 ready_timeout=60 port=${TCM_PORT:-7391}
result() {
    python3 - "$1" "$2" "$pane" "$session" "$ready" "$peer_name" "$agent" "$pane_tail" <<'PY'
import json, sys
ok, error, pane, session, ready, peer, agent, tail = sys.argv[1:]
value = {"ok": ok == "true"}
if pane:
    value.update(pane=pane, session=session, ready=ready,
                 prompt_sent=ok == "true")
    if ok == "true":
        value["peer_name"] = peer
    if agent != "claude":
        value["agent"] = agent
if error:
    value["error"] = error
if tail:
    value["pane_tail"] = tail
print(json.dumps(value, separators=(",", ":")))
PY
}
fail() { result false "$1"; exit 1; }
# With --close-on-exit, a failure after the pane exists records the last
# non-empty lines of the pane, which stays open because remain-on-exit is on.
fail_kept() {
    pane_tail=$(tmux capture-pane -p -t "$pane" 2>/dev/null | python3 -c '
import sys
text = sys.stdin.buffer.read().decode("utf-8", "replace")
print("\n".join([l for l in text.splitlines() if l.strip()][-20:]))
') || pane_tail=
    fail "$1"
}
unexpected() {
    trap - ERR
    if [[ -n "$pane" ]]; then
        ready=${ready:-timeout}
        if (( close_on_exit )); then
            fail_kept "Command failed; the pane is kept for reading."
        fi
        fail "Command failed; tmux session '$session' remains running."
    fi
    fail "Command failed before a pane id was returned."
}
trap unexpected ERR
while [[ $# -gt 0 ]]; do
    case "$1" in
        --repo|--session|--prompt-file|--agent|--window|--name|--session-id|--permission-mode|--ready-timeout|--port)
            [[ $# -ge 2 ]] || fail "Missing value for $1. Usage: $usage"
            case "$1" in
                --repo) repo=$2 ;; --session) session=$2 ;;
                --prompt-file) prompt_file=$2 ;; --agent) agent=$2 ;;
                --window) window=$2 ;; --name) name=$2 ;;
                --session-id) session_id=$2; session_id_set=1 ;;
                --permission-mode) permission_mode=$2 ;;
                --ready-timeout) ready_timeout=$2 ;; --port) port=$2 ;;
            esac
            shift 2 ;;
        --attach-existing) attach=1; shift ;;
        --close-on-exit) close_on_exit=1; shift ;;
        *) fail "Unknown argument: $1. Usage: $usage" ;;
    esac
done
case "$agent" in
    claude)
        window=${window:-claude}
        launch="claude --permission-mode ${permission_mode:=auto}"
        if (( session_id_set )); then
            [[ "$session_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail "--session-id must be a UUID in lower case."
            launch+=" --session-id $session_id"
        fi
        if [[ -n "$name" ]]; then
            [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$ ]] || fail "--name must match ^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}\$."
            launch+=" -n $name"
        fi
        # exec replaces the shell, so the pane ends when Claude Code ends.
        (( ! close_on_exit )) || launch="exec $launch"
        tcm_agent=claude-code ;;
    pi)
        [[ -z "$permission_mode" ]] || fail "--permission-mode applies only to --agent claude."
        [[ -z "$name" ]] || fail "--name applies only to --agent claude; pi has no session name option."
        (( ! session_id_set )) || fail "--session-id applies only to --agent claude."
        (( ! close_on_exit )) || fail "--close-on-exit applies only to --agent claude."
        window=${window:-pi} launch=pi tcm_agent=pi ;;
    *) fail "--agent must be claude or pi." ;;
esac
command -v tmux >/dev/null 2>&1 || fail "tmux is required on PATH."
[[ -d "$repo" ]] || fail "--repo must be an existing directory."
[[ -f "$prompt_file" && -r "$prompt_file" && -s "$prompt_file" ]] || fail "--prompt-file must be a readable, non-empty file."
[[ -n "$session" && "$session" != *[.:]* && "$session" != -* ]] || fail "--session must be a non-empty tmux session name without dots or colons."
[[ "$ready_timeout" =~ ^[0-9]+$ && ${#ready_timeout} -le 6 ]] || fail "--ready-timeout must be an integer from 0 to 999999."
ready_timeout=$((10#$ready_timeout))
[[ "$port" =~ ^[0-9]+$ && ${#port} -le 5 ]] || fail "--port must be an integer from 1 to 65535."
port=$((10#$port))
(( port > 0 && port <= 65535 )) || fail "--port must be an integer from 1 to 65535."
[[ "$agent" == pi || "$permission_mode" =~ ^[a-zA-Z0-9_-]+$ ]] || fail "--permission-mode must be a mode name."
existing=
if (( attach )); then
    # A tmux session's path is the directory it was created in, and it stays
    # fixed while the directories of its panes change. The first tmux session
    # whose path is --repo receives the window.
    sessions=$(tmux list-sessions -F '#{session_path}	#{session_name}' 2>/dev/null) || sessions=
    existing=$(printf '%s\n' "$sessions" | python3 -c '
import os, sys
repo = os.path.realpath(sys.argv[1])
for line in sys.stdin.read().splitlines():
    path, tab, name = line.partition("\t")
    if tab and path and name and os.path.realpath(path) == repo:
        print(name)
        break
' "$repo")
fi
if [[ -n "$existing" ]]; then
    pane=$(tmux new-window -d -P -F '#{pane_id}' -t "=$existing:" -n "$window" -c "$repo")
    session=$existing
else
    if tmux has-session -t "=$session" 2>/dev/null; then
        fail "tmux session '$session' already exists."
    fi
    pane=$(tmux new-session -d -P -F '#{pane_id}' -s "$session" -n "$window" -x 200 -y 50 -c "$repo")
fi
if (( close_on_exit )); then
    tmux set-option -p -t "$pane" remain-on-exit on >&2
fi
tmux send-keys -t "$pane" "$launch" Enter >&2
elapsed=0 stable=0 previous=
name_wait_polls=5
while (( elapsed < ready_timeout )); do
    if (( close_on_exit )) && [[ $(tmux display-message -p -t "$pane" '#{pane_dead}') == 1 ]]; then
        ready=exited
        fail_kept "Claude Code ended before it was ready; the pane is kept for reading."
    fi
    pane_text=$(tmux capture-pane -p -t "$pane")
    if printf '%s' "$pane_text" | python3 -c '
import re, sys
s = sys.stdin.read().lower()
choice = re.search(r"\byes\b", s) and re.search(r"\bno\b", s)
choice = choice or re.search(r"\by\s*/\s*n\b", s)
sys.exit(0 if "trust" in s and choice else 1)
'; then
        ready=dialog
        (( ! close_on_exit )) || fail_kept "Input is blocked by a trust dialog; the pane is kept for reading."
        fail "Input is blocked by a trust dialog; tmux session '$session' remains running."
    fi
    if state=$(curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:$port/state" 2>/dev/null); then
        stable=0 tcm_seen=1
        if peer_name=$(printf '%s' "$state" | python3 -c '
import json, sys
try:
    state = json.load(sys.stdin)
    for session in state.get("sessions", []):
        for agent in session.get("agents", []):
            if (agent.get("agent") == sys.argv[2] and
                agent.get("paneId") == sys.argv[1] and agent.get("status") == "idle"):
                print(agent.get("threadName") or "")
                sys.exit(0)
except (ValueError, TypeError, AttributeError):
    pass
sys.exit(1)
' "$pane" "$tcm_agent"); then
            ready=idle
            for ((name_poll=0; name_poll<name_wait_polls && elapsed+1<ready_timeout; name_poll++)); do
                [[ -z "$peer_name" ]] || break
                sleep 1
                elapsed=$((elapsed + 1))
                if ! state=$(curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:$port/state" 2>/dev/null); then
                    break
                fi
                name_state=$(printf '%s' "$state" | python3 -c '
import json, sys
try:
    for session in json.load(sys.stdin).get("sessions", []):
        for agent in session.get("agents", []):
            if agent.get("agent") == sys.argv[2] and agent.get("paneId") == sys.argv[1]:
                print(agent.get("status") or "")
                print(agent.get("threadName") or "")
                sys.exit(0)
except (ValueError, TypeError, AttributeError):
    pass
' "$pane" "$tcm_agent")
                peer_name=${name_state#*$'\n'}
                [[ "$name_state" == *$'\n'* ]] || peer_name=
                [[ "${name_state%%$'\n'*}" == idle ]] || break
            done
            break
        fi
    elif (( close_on_exit )); then
        # Unchanged pane text is not a sign of readiness with --close-on-exit.
        tcm_seen=0
    else
        if [[ -n "$pane_text" && "$pane_text" == "$previous" ]]; then
            stable=$((stable + 1))
        else
            stable=1
        fi
        if [[ -n "$pane_text" ]] && (( stable >= 3 && elapsed >= 5 )); then
            ready='pane-stable'
            break
        fi
    fi
    previous=$pane_text
    sleep 1
    elapsed=$((elapsed + 1))
done
if [[ -z "$ready" ]]; then
    ready=timeout
    if (( close_on_exit )); then
        if (( tcm_seen )); then
            fail_kept "Readiness timed out: tcm listed no idle conversation in the pane; the pane is kept for reading."
        fi
        fail_kept "Readiness timed out: tcm gave no answer; the pane is kept for reading."
    fi
    fail "Readiness timed out; tmux session '$session' remains running."
fi
buffer="peer-session-$$-$RANDOM"
tmux load-buffer -b "$buffer" "$prompt_file" >&2
if (( close_on_exit )); then
    # Paste only when tcm still lists the pane's conversation as idle.
    if ! state=$(curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:$port/state" 2>/dev/null); then
        tmux delete-buffer -b "$buffer" >&2
        ready=unconfirmed
        fail_kept "tcm gave no answer on the check before the paste; nothing was pasted and the pane is kept for reading."
    fi
    status=$(printf '%s' "$state" | python3 -c '
import json, sys
try:
    for session in json.load(sys.stdin).get("sessions", []):
        for agent in session.get("agents", []):
            if agent.get("agent") == sys.argv[2] and agent.get("paneId") == sys.argv[1]:
                print(agent.get("status") or "")
                sys.exit(0)
except (ValueError, TypeError, AttributeError):
    pass
' "$pane" "$tcm_agent")
    if [[ "$status" != idle ]]; then
        tmux delete-buffer -b "$buffer" >&2
        ready=unconfirmed
        fail_kept "The conversation status changed to '${status:-none}' before the paste; nothing was pasted and the pane is kept for reading."
    fi
    tmux set-option -p -t "$pane" remain-on-exit off >&2
fi
tmux paste-buffer -p -d -b "$buffer" -t "$pane" >&2
sleep 0.5
tmux send-keys -t "$pane" Enter >&2
result true ""
