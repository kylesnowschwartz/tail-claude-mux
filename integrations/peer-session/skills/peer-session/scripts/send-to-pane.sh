#!/usr/bin/env bash
set -euo pipefail

usage='send-to-pane.sh --pane <pane id> (--check | --message <text> | --message-file <file>) [--wait <seconds>]'
pane='' message='' message_file='' mode='' wait=5 limit=4000
command_name='' path='' typed='' changed='' enters=0 tail_text=''
result() {
    python3 - "$1" "$2" "$pane" "$command_name" "$path" "$typed" "$changed" "$enters" "$tail_text" <<'PY'
import json, sys
ok, error, pane, command, path, typed, changed, enters, tail = sys.argv[1:]
value = {"ok": ok == "true"}
if command:
    value.update(pane=pane, command=command, path=path)
if typed:
    value.update(typed=typed == "true", enter_count=int(enters))
if changed:
    value["pane_changed"] = changed == "true"
if error:
    value["error"] = error
if tail:
    value["pane_tail"] = tail
print(json.dumps(value, separators=(",", ":")))
PY
}
fail() { result false "$1"; exit 1; }
trap 'trap - ERR; fail "Command failed."' ERR
# The visible text of the pane, with wrapped lines joined.
capture() { tmux capture-pane -p -J -t "$pane"; }
# The last 15 non-empty lines of the pane.
read_tail() {
    tail_text=$(capture 2>/dev/null | python3 -c '
import sys
text = sys.stdin.buffer.read().decode("utf-8", "replace")
print("\n".join([l.rstrip() for l in text.splitlines() if l.strip()][-15:]))
') || tail_text=
}
# How often the start of the message shows in the pane text on stdin, with
# white space left out, because a pane wraps and indents what is typed.
occurrences() {
    python3 -c '
import sys
squeeze = lambda s: "".join(s.split())
print(squeeze(sys.stdin.buffer.read().decode("utf-8", "replace")).count(squeeze(sys.argv[1])[:20]))
' "$message"
}
is_shell() {
    case "${1#-}" in
        sh|bash|zsh|fish|dash|ksh|tcsh|csh|"$(basename -- "${SHELL:-sh}")") return 0 ;;
    esac
    return 1
}
while [[ $# -gt 0 ]]; do
    case "$1" in
        --pane|--message|--message-file|--wait)
            [[ $# -ge 2 ]] || fail "Missing value for $1. Usage: $usage"
            case "$1" in
                --pane) pane=$2 ;;
                --message) message=$2; mode=${mode:+both}; mode=${mode:-send} ;;
                --message-file) message_file=$2; mode=${mode:+both}; mode=${mode:-send} ;;
                --wait) wait=$2 ;;
            esac
            shift 2 ;;
        --check) mode=${mode:+both}; mode=${mode:-check}; shift ;;
        *) fail "Unknown argument: $1. Usage: $usage" ;;
    esac
done
[[ "$mode" == check || "$mode" == send ]] || fail "Give exactly one of --check, --message, or --message-file. Usage: $usage"
[[ "$pane" =~ ^%[0-9]+$ ]] || fail "--pane must be a tmux pane id such as %12."
[[ "$wait" =~ ^[0-9]+$ && ${#wait} -le 3 ]] || fail "--wait must be an integer from 0 to 999."
wait=$((10#$wait))
command -v tmux >/dev/null 2>&1 || fail "tmux is required on PATH."
if [[ "$mode" == send ]]; then
    if [[ -n "$message_file" ]]; then
        [[ -f "$message_file" && -r "$message_file" ]] || fail "--message-file must be a readable file."
        # Command substitution drops the newlines that end the file.
        message=$(cat -- "$message_file")
    fi
    [[ "$message" =~ [^[:space:]] ]] || fail "The message is empty."
    [[ ! "$message" =~ [[:cntrl:]] ]] || fail "The message must be one line without control characters: a newline submits early in an agent's input."
    (( ${#message} <= limit )) || fail "The message holds ${#message} characters; the limit is $limit. Write the text to a file and send a line that names the file."
fi
[[ "$pane" != "${TMUX_PANE:-}" ]] || fail "Pane $pane is this session's own pane."
facts=$(tmux display-message -p -t "$pane" '#{pane_id}	#{pane_dead}	#{pane_current_command}	#{pane_current_path}' 2>/dev/null) || facts=
IFS=$'\t' read -r found dead command_name path <<< "$facts"
[[ "$found" == "$pane" ]] || { command_name=''; fail "tmux has no pane $pane."; }
read_tail
[[ "$dead" != 1 ]] || fail "The program in pane $pane has ended."
if [[ "$mode" == check ]]; then
    result true ""
    exit 0
fi
! is_shell "$command_name" || fail "No agent runs in pane $pane: its current program is the shell $command_name."
polls=$((wait * 5))
before=$(capture | occurrences)
tmux send-keys -t "$pane" -l -- "$message" >&2
typed=false
for ((poll = 0; poll <= polls; poll++)); do
    shown=$(capture)
    if (( $(printf '%s' "$shown" | occurrences) > before )); then
        typed=true
        break
    fi
    sleep 0.2
done
if [[ "$typed" == false ]]; then
    read_tail
    fail "The message did not show in pane $pane within $wait seconds, so Enter was not sent. The text may sit in the input line."
fi
# Enter is a separate call, so paste handling in the agent does not take it
# as part of the text. One more Enter follows when the pane shows no change.
changed=false
while (( enters < 2 )) && [[ "$changed" == false ]]; do
    tmux send-keys -t "$pane" Enter >&2
    enters=$((enters + 1))
    for ((poll = 0; poll <= polls; poll++)); do
        if [[ "$(capture)" != "$shown" ]]; then
            changed=true
            break
        fi
        sleep 0.2
    done
done
read_tail
[[ "$changed" == true ]] || fail "Pane $pane showed no change after Enter was sent twice; the message may sit in the input line."
result true ""
