#!/usr/bin/env bash
# Runs send-to-pane.sh against panes of a throwaway tmux server (tmux -L).
# `cat` stands in for an agent: the terminal shows what is typed, and cat
# prints the line again once Enter arrives.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
script="$root/skills/peer-session/scripts/send-to-pane.sh"
if ! command -v tmux >/dev/null 2>&1; then
    echo "SKIP send-to-pane cases: tmux is required"
    exit 0
fi
tmp=$(mktemp -d)
server="send-to-pane-test-$$"
real_tmux=$(command -v tmux)
mkdir "$tmp/bin"
printf '#!/bin/sh\nexec %s -L %s -f /dev/null "$@"\n' "$real_tmux" "$server" > "$tmp/bin/tmux"
chmod +x "$tmp/bin/tmux"
socket=''
trap '"$tmp/bin/tmux" kill-server 2>/dev/null || true; rm -rf "$tmp" ${socket:+"$socket"}' EXIT
export PATH="$tmp/bin:$PATH"
unset TMUX TMUX_PANE
tmux new-session -d -s keep -x 120 -y 30 /bin/sh
shell_pane=$(tmux display-message -p -t keep '#{pane_id}')
socket=$(tmux display-message -p -t keep '#{socket_path}')
agent_pane=$(tmux new-window -d -P -F '#{pane_id}' -t keep: cat)
# The terminal of this pane shows nothing that is typed.
silent_pane=$(tmux new-window -d -P -F '#{pane_id}' -t keep: "sh -c 'stty -echo; exec sleep 60'")
# This pane shows what is typed and reads nothing, so Enter changes nothing.
stuck_pane=$(tmux new-window -d -P -F '#{pane_id}' -t keep: "sh -c 'stty -icanon -echoctl; printf ready; exec sleep 60'")
for _ in $(seq 50); do
    [[ $(tmux display-message -p -t "$silent_pane" '#{pane_current_command}') == sleep &&
       $(tmux capture-pane -p -t "$stuck_pane") == *ready* ]] && break
    sleep 0.1
done
run() {
    code=0
    bash "$script" "$@" > "$tmp/out" || code=$?
}
check() {
    python3 - "$1" "$code" "$tmp/out" "${2:-}" <<'PY'
import json, sys
case, code, out, expect = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
lines = open(out, encoding="utf-8").read().splitlines()
def require(ok, message):
    if not ok:
        sys.exit("FAIL " + case + ": " + message + ": " + str(lines))
require(len(lines) == 1, "stdout must contain one line")
v = json.loads(lines[0])
if case in ("check", "check-shell"):
    require(code == 0 and v["ok"] and "typed" not in v, "check sends nothing")
    require(v["command"] in expect.split("|") and v["pane"].startswith("%") and v["path"], "pane facts")
elif case in ("send", "send-file", "send-dash"):
    require(code == 0 and v["ok"] and v["typed"] and v["pane_changed"], "sent")
    require(v["enter_count"] == 1, "one Enter")
    require(v["pane_tail"].count(expect) == 2, "typed line and its echo show in the tail")
elif case == "silent":
    require(code != 0 and not v["ok"] and v["typed"] is False and v["enter_count"] == 0, "no Enter without the text")
    require("Enter was not sent" in v["error"], "error")
elif case == "stuck":
    require(code != 0 and not v["ok"] and v["typed"] and v["pane_changed"] is False, "typed, no change")
    require(v["enter_count"] == 2 and expect in v["pane_tail"], "second Enter and tail")
else:
    require(code != 0 and v["ok"] is False and expect in v["error"], "refused")
    require("typed" not in v, "nothing typed")
print("PASS " + case)
PY
}
run --pane "$agent_pane" --check; check check cat
run --pane "$shell_pane" --check; check check-shell "sh|bash|dash|zsh"
[[ -z $(tmux capture-pane -p -t "$agent_pane" | tr -d '[:space:]') ]] || { echo "FAIL check: typed into the pane" >&2; exit 1; }
run --pane "$agent_pane" --message "Report is at /tmp/report.md: it's done, \$HOME \`x\` stay literal"; check send "it's done, \$HOME \`x\` stay literal"
printf 'Second message from a file\n\n' > "$tmp/message"
run --pane "$agent_pane" --message-file "$tmp/message"; check send-file "Second message from a file"
run --pane "$agent_pane" --message "-l leading dash"; check send-dash "-l leading dash"
run --pane "$shell_pane" --message "hello"; check refuse-shell "No agent runs"
TMUX_PANE=$agent_pane run --pane "$agent_pane" --message "hello"; check refuse-own "own pane"
run --pane "%99999" --message "hello"; check refuse-missing "no pane"
run --pane "keep:1" --message "hello"; check refuse-target "pane id"
run --pane "$agent_pane" --message $'two\nlines'; check refuse-lines "one line"
run --pane "$agent_pane" --message "   "; check refuse-empty "empty"
run --pane "$agent_pane" --message "$(head -c 4001 /dev/zero | tr '\0' x)"; check refuse-long "4000"
run --pane "$agent_pane" --check --message "hello"; check refuse-both "exactly one"
run --pane "$agent_pane"; check refuse-none "exactly one"
run --pane "$agent_pane" --message; check refuse-value "Missing value"
[[ $(tmux capture-pane -p -t "$agent_pane" | grep -c hello) == 0 ]] || { echo "FAIL refusals: typed into the pane" >&2; exit 1; }
echo "PASS refusals-type-nothing"
run --pane "$silent_pane" --wait 1 --message "never shown"; check silent
run --pane "$stuck_pane" --wait 1 --message "stays in place"; check stuck "stays in place"
