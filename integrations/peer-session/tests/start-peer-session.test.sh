#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
real_tmp=$(cd "$tmp" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
export CALL_LOG="$tmp/calls" FAKE_STATE="$tmp/state" FAKE_PANE="$tmp/pane"
export EXISTING=0 SERVER_DOWN=0 FAKE_COUNT="$tmp/count" FAKE_SESSIONS="$tmp/sessions"
cat > "$tmp/bin/tmux" <<'FAKE'
#!/usr/bin/env bash
set -eu
python3 - "$@" >> "$CALL_LOG" <<'PY'
import json, sys
print(json.dumps(sys.argv[1:]))
PY
[[ "$1" != "${FAIL_CMD:-}" ]] || exit 1
case "$1" in
    has-session) [[ "$EXISTING" == 1 ]] ;;
    new-session|new-window) printf '%%128\n' ;;
    list-sessions) cat "$FAKE_SESSIONS" ;;
    capture-pane) cat "$FAKE_PANE" ;;
    display-message)
        if [[ "$*" == *pane_current_command* ]]; then printf '%s\n' "${FAKE_CMD:-claude}"
        elif [[ -f "$FAKE_PANE.dead" ]]; then cat "$FAKE_PANE.dead"; else printf '0\n'; fi ;;
esac
FAKE
cat > "$tmp/bin/curl" <<'FAKE'
#!/usr/bin/env bash
set -eu
printf 'curl\n' >> "$CALL_LOG.extra"
[[ "$SERVER_DOWN" == 0 ]] || exit 7
printf 'curl\n' >> "$CALL_LOG"
count=$(cat "$FAKE_COUNT")
count=$((count + 1))
printf '%s' "$count" > "$FAKE_COUNT"
[[ "$count" != "${DOWN_AT:-}" ]] || exit 7
if [[ -f "$FAKE_STATE.$count" ]]; then cat "$FAKE_STATE.$count"; else cat "$FAKE_STATE"; fi
FAKE
cat > "$tmp/bin/sleep" <<'FAKE'
#!/usr/bin/env bash
printf 'sleep %s\n' "$*" >> "$CALL_LOG.extra"
FAKE
chmod +x "$tmp/bin/"*
orig_path=$PATH
export PATH="$tmp/bin:$PATH"
script="$root/scripts/start-peer-session.sh"
printf 'Task line one\nTask line two\n' > "$tmp/prompt"
run() {
    : > "$CALL_LOG"
    : > "$CALL_LOG.extra"
    printf 0 > "$FAKE_COUNT"
    code=0
    local prompt=$1
    shift
    bash "$script" --repo "$tmp" --session peer --prompt-file "$prompt" --ready-timeout 7 "$@" > "$tmp/out" || code=$?
}
check() {
    python3 - "$tmp" "$1" "$code" <<'PY'
import json, os, pathlib, sys
p, case, code = pathlib.Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
read = "peer_task=\"$(cat -- '" + os.path.realpath(p / "prompt") + "')\" && [ -n \"$peer_task\" ] && "
task = " -- \"$peer_task\""
def require(ok, message):
    if not ok:
        sys.exit("FAIL " + case + ": " + message)
lines = (p / "out").read_text().splitlines()
require(len(lines) == 1, "stdout must contain one line")
v = json.loads(lines[0])
events = (p / "calls").read_text().splitlines()
calls = [json.loads(s) for s in events if s != "curl"]
names = [c[0] for c in calls]
pi = case.startswith("pi-")
if case in ("existing", "attach-taken", "missing", "empty", "quote-path", "quote-dir", "blank", "large", "pi-mode", "bad-agent", "bad-name", "pi-name", "bad-id", "pi-id"):
    require(code != 0 and v["ok"] is False, "validation must fail")
    require("new-session" not in names, "validation created session")
    if case in ("existing", "attach-taken"):
        require(v == {"ok": False, "error": "tmux session 'peer' already exists."}, "existing session error")
    if case == "pi-mode":
        require("--permission-mode" in v["error"], "pi mode error")
    if case in ("bad-name", "pi-name"):
        require("--name" in v["error"], "name error")
        require(not calls, "name validation made tmux calls: " + str(names))
    if case in ("bad-id", "pi-id"):
        require("--session-id" in v["error"], "session id error")
        require(not calls, "session id validation made tmux calls: " + str(names))
    if case in ("quote-path", "quote-dir"):
        require("single quote" in v["error"] and not calls, "quoted path refused")
    if case == "blank":
        require("white space" in v["error"] and not calls, "blank task refused")
    if case == "large":
        require("262144" in v["error"] and not calls, "large task refused")
    if case == "bad-agent":
        require("--agent" in v["error"], "agent error")
else:
    if case.startswith("attach-"):
        require(calls[0] == ["list-sessions", "-F", "#{session_path}\t#{session_name}"], "session path lookup")
        calls, names = calls[1:], names[1:]
    if case in ("attach-match", "attach-fallback"):
        require(names[:2] == ["new-window", "send-keys"], "startup order")
        require("has-session" not in names and "new-session" not in names, "attach created session")
        require(calls[0] == ["new-window", "-d", "-P", "-F", "#{pane_id}", "-t",
                                       "=work:", "-n", "claude", "-c", str(p)],
                "window arguments")
        require(v["session"] == "work", "session field names the existing session")
        launch = calls[1]
    else:
        require(names[:3] == ["has-session", "new-session", "send-keys"], "startup order")
        require(calls[0] == ["has-session", "-t", "=peer"], "exact session match")
        require(calls[1] == ["new-session", "-d", "-P", "-F", "#{pane_id}", "-s",
                            "peer", "-n", "pi" if pi else "claude", "-x", "200", "-y", "50", "-c", str(p)],
                "session arguments")
        require(v["session"] == "peer", "session field")
        launch = calls[2]
    expected = "pi" if pi else read + "claude --permission-mode auto"
    if case == "name":
        expected += " -n peer/s4-agent-test"
    if case == "session-id":
        expected += " --session-id 123e4567-e89b-42d3-a456-426614174000"
    if case == "session-id-name":
        expected += " --session-id 123e4567-e89b-42d3-a456-426614174000 -n peer/x"
    if not pi:
        expected += task
    require(launch == ["send-keys", "-t", "%128", expected, "Enter"],
            "launch command")
    require(v.get("agent") == ("pi" if pi else None), "agent field")
    for c in calls[2:]:
        if c[0] != "load-buffer":
            require("-t" in c and c[c.index("-t")+1] == "%128", "pane target: " + str(c))
    if case in ("idle", "working", "relative", "pi-quote", "name", "session-id", "session-id-name", "fallback", "delayed", "unnamed", "pi-idle", "attach-match", "attach-fallback", "attach-none"):
        require(code == 0 and v["ok"] and v["prompt_sent"], "success")
        require(v["ready"] == ("pane-stable" if case in ("fallback", "attach-fallback") else case if case == "working" else "idle"), "ready")
        require(v["peer_name"] == ("other-project" if case in ("idle", "working", "relative", "pi-quote", "name", "session-id", "session-id-name", "delayed", "pi-idle", "attach-match", "attach-none") else ""), "peer")
        if case == "delayed":
            require(events.count("curl") == 3, "name poll count")
        if case == "unnamed":
            require(events.count("curl") <= 6, "bounded name wait")
        if not pi:
            require("paste-buffer" not in names and "load-buffer" not in names, "Claude Code gets no paste")
            require(names.count("send-keys") == 1, "Claude Code gets no Enter after start")
            print("PASS " + case)
            sys.exit()
        require(names[-3:] == ["load-buffer", "paste-buffer", "send-keys"], "prompt order")
        load, paste = calls[-3:-1]
        require("-p" in paste and "-d" in paste, "paste flags")
        require(load[load.index("-b")+1] == paste[paste.index("-b")+1], "named buffer")
        require(load[-1] == str(p / ("it's.txt" if case == "pi-quote" else "prompt")), "prompt file")
        require(calls[-1] == ["send-keys", "-t", "%128", "Enter"], "submit")
        require("sleep 0.5" in (p / "calls.extra").read_text(), "paste pause")
        require(all("-l" not in c for c in calls), "literal send-keys")
    else:
        require(code != 0 and not v["ok"] and not v["prompt_sent"], "failure")
        require(v["ready"] == case.removeprefix("pi-") and "remains running" in v["error"], "failure details")
        if case == "not-started":
            require("command not found" in v.get("pane_tail", ""), "pane tail")
        require("paste-buffer" not in names and "load-buffer" not in names, "unexpected prompt")
print("PASS " + case)
PY
}
printf 'Claude input\n' > "$FAKE_PANE"
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt"; check idle
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"working","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt"; check working
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
(cd "$tmp" && run prompt; check relative)
cp "$tmp/prompt" "$tmp/it's"
run "$tmp/it's"; check quote-path
mkdir "$tmp/'\$(printf INJECTED)'"
cp "$tmp/prompt" "$tmp/'\$(printf INJECTED)'/task"
(cd "$tmp/'\$(printf INJECTED)'" && run task; check quote-dir)
printf '\n\n \t\n' > "$tmp/blank"
run "$tmp/blank"; check blank
head -c 262145 /dev/zero | tr '\0' x > "$tmp/large"
run "$tmp/large"; check large
cp "$FAKE_STATE" "$FAKE_STATE.3"
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":""}]}]}' > "$FAKE_STATE"
run "$tmp/prompt"; check delayed
rm "$FAKE_STATE.3"
run "$tmp/prompt"; check unnamed
EXISTING=1
run "$tmp/prompt"; check existing
EXISTING=0
run "$tmp/missing"; check missing
: > "$tmp/empty"
run "$tmp/empty"; check empty
printf 'Do you trust this folder? Yes / No\n' > "$FAKE_PANE"
run "$tmp/prompt"; check dialog
printf 'Claude input \377\nDo you trust this folder? Yes / No\n' > "$FAKE_PANE"
run "$tmp/prompt"; check dialog
printf 'Starting Claude\n' > "$FAKE_PANE"
printf '{"sessions":[]}\n' > "$FAKE_STATE"
run "$tmp/prompt"; check timeout
SERVER_DOWN=1
run "$tmp/prompt"; check fallback
printf 'zsh: command not found: claude\n' > "$FAKE_PANE"
FAKE_CMD=zsh run "$tmp/prompt"; check not-started
SERVER_DOWN=0
printf 'pi input\n' > "$FAKE_PANE"
printf '%s\n' '{"sessions":[{"agents":[{"agent":"pi","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt" --agent pi; check pi-idle
cp "$tmp/prompt" "$tmp/it's.txt"
run "$tmp/it's.txt" --agent pi; check pi-quote
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt" --agent pi; check pi-timeout
run "$tmp/prompt" --agent pi --permission-mode auto; check pi-mode
run "$tmp/prompt" --agent codex; check bad-agent
printf 'Claude input\n' > "$FAKE_PANE"
run "$tmp/prompt" --name peer/s4-agent-test; check name
run "$tmp/prompt" --name 'bad name; rm'; check bad-name
run "$tmp/prompt" --name '-leading'; check bad-name
run "$tmp/prompt" --agent pi --name peer/x; check pi-name
run "$tmp/prompt" --session-id 123e4567-e89b-42d3-a456-426614174000; check session-id
run "$tmp/prompt" --session-id 123e4567-e89b-42d3-a456-426614174000 --name peer/x; check session-id-name
for bad_id in not-a-uuid 123E4567-E89B-42D3-A456-426614174000 123e4567-e89b-42d3-a456-42661417400A 123e4567-e89b-42d3-a456-426614174000x 123e4567 '' ' 123e4567-e89b-42d3-a456-426614174000' '{123e4567-e89b-42d3-a456-426614174000}'; do
    run "$tmp/prompt" --session-id "$bad_id"; check bad-id
done
run "$tmp/prompt" --agent pi --session-id 123e4567-e89b-42d3-a456-426614174000; check pi-id
run "$tmp/prompt" --session-id; check bad-id
printf 'Claude input\n' > "$FAKE_PANE"
agents='"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]'
printf '{"sessions":[{"name":"work","dir":"%s/elsewhere",%s}]}\n' "$tmp" "$agents" > "$FAKE_STATE"
printf '%s/elsewhere\tother\n%s\twork\n' "$tmp" "$tmp" > "$FAKE_SESSIONS"
run "$tmp/prompt" --attach-existing; check attach-match
printf '%s/elsewhere\tother\n' "$tmp" > "$FAKE_SESSIONS"
run "$tmp/prompt" --attach-existing; check attach-none
EXISTING=1
run "$tmp/prompt" --attach-existing; check attach-taken
EXISTING=0
SERVER_DOWN=1
printf '%s/elsewhere\tother\n%s\twork\n' "$tmp" "$tmp" > "$FAKE_SESSIONS"
run "$tmp/prompt" --attach-existing; check attach-fallback

# --close-on-exit cases.
close_check() {
    python3 - "$tmp" "$1" "$code" <<'PY'
import json, os, pathlib, sys
p, case, code = pathlib.Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
read = "peer_task=\"$(cat -- '" + os.path.realpath(p / "prompt") + "')\" && [ -n \"$peer_task\" ] && "
task = " -- \"$peer_task\""
def require(ok, message):
    if not ok:
        sys.exit("FAIL " + case + ": " + message)
lines = (p / "out").read_text().splitlines()
require(len(lines) == 1, "stdout must contain one line")
v = json.loads(lines[0])
events = (p / "calls").read_text().splitlines()
calls = [json.loads(s) for s in events if s != "curl"]
names = [c[0] for c in calls]
on = ["set-option", "-p", "-t", "%128", "remain-on-exit", "on"]
off = ["set-option", "-p", "-t", "%128", "remain-on-exit", "off"]
if case == "close-pi":
    require(code != 0 and "--close-on-exit" in v["error"] and not calls, "pi refused before tmux")
    print("PASS " + case)
    sys.exit()
launch = next(c for c in calls if c[0] == "send-keys")
require(launch[3] == read + "exec claude --permission-mode auto" + task, "exec launch: " + str(launch))
require(calls.index(on) < calls.index(launch), "remain-on-exit on before launch")
if case in ("close-idle", "close-done"):
    require(code == 0 and v["ok"] and v["prompt_sent"] and v["ready"] == case[6:], "success")
    end = [off, ["display-message", "-p", "-t", "%128", "#{pane_dead}"], ["kill-pane", "-t", "%128"]] if case == "close-done" else [off]
    require(calls[-len(end):] == end and "paste-buffer" not in names, "off last, no paste")
    require(names.count("send-keys") == 1, "no Enter after start")
    require(events.count("curl") == 1, "one tcm check")
    require("pane_tail" not in v, "no tail on success")
else:
    require(code != 0 and not v["ok"] and not v["prompt_sent"], "failure")
    require(off not in calls and "paste-buffer" not in names, "no paste, setting stays on")
    require("Claude input" in v.get("pane_tail", ""), "pane tail")
    require("remains running" not in v["error"], "no running claim")
    expect = {"close-down": ("timeout", "tcm gave no answer"),
              "close-busy": ("timeout", "no ready or working conversation"),
              "close-dead": ("exited", "kept for reading"),
              "close-lost": ("lost", "stopped listing"),
              "close-dialog": ("dialog", "trust dialog")}[case]
    require(v["ready"] == expect[0] and expect[1] in v["error"], "details: " + str(v))
print("PASS " + case)
PY
}
SERVER_DOWN=0
idle_state='{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}'
if [[ -z "${RECORD_GOLDEN_FROM:-}" ]]; then
printf 'Claude input\n' > "$FAKE_PANE"
idle_state='{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}'
printf '%s\n' "$idle_state" > "$FAKE_STATE"
run "$tmp/prompt" --close-on-exit; close_check close-idle
run "$tmp/prompt" --close-on-exit --agent pi; close_check close-pi
SERVER_DOWN=1
run "$tmp/prompt" --close-on-exit; close_check close-down
SERVER_DOWN=0
printf '%s\n' "${idle_state/\"idle\"/\"error\"}" > "$FAKE_STATE"
run "$tmp/prompt" --close-on-exit; close_check close-busy
printf '%s\n' "${idle_state/\"idle\"/\"done\"}" > "$FAKE_STATE"
printf '1\n' > "$FAKE_PANE.dead"
run "$tmp/prompt" --close-on-exit; close_check close-done
rm "$FAKE_PANE.dead"
printf '%s\n' "${idle_state/other-project/}" > "$FAKE_STATE.1"
printf '{"sessions":[]}\n' > "$FAKE_STATE"
run "$tmp/prompt" --close-on-exit; close_check close-lost
rm "$FAKE_STATE.1"
printf '1\n' > "$FAKE_PANE.dead"
run "$tmp/prompt" --close-on-exit; close_check close-dead
rm "$FAKE_PANE.dead"
printf 'Claude input\nDo you trust this folder? Yes / No\n' > "$FAKE_PANE"
run "$tmp/prompt" --close-on-exit; close_check close-dialog
printf 'Claude input\n' > "$FAKE_PANE"
printf '%s\n' "$idle_state" > "$FAKE_STATE"
export FAIL_CMD=send-keys
run "$tmp/prompt" --close-on-exit
FAIL_CMD=
python3 - "$tmp/out" "$code" <<'PY'
import json, sys
v = json.loads(open(sys.argv[1]).read())
if sys.argv[2] == "0" or v["error"] != "Command failed; the pane is kept for reading." or "pane_tail" not in v:
    sys.exit("FAIL close-start-fails: " + str(v))
print("PASS close-start-fails")
PY
printf 'say "hi" \\ back\tslash\nbefore \377 after\n' > "$FAKE_PANE"
SERVER_DOWN=1
run "$tmp/prompt" --close-on-exit
SERVER_DOWN=0
python3 - "$tmp/out" "$code" <<'PY'
import json, sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
if len(lines) != 1 or sys.argv[2] == "0":
    sys.exit("FAIL close-tail-bytes: one failing JSON line")
tail = json.loads(lines[0]).get("pane_tail", "")
for part in ('"hi"', "\\", "\t", "before ", " after"):
    if part not in tail:
        sys.exit("FAIL close-tail-bytes: tail lacks " + repr(part) + ": " + repr(tail))
print("PASS close-tail-bytes")
PY
fi

# Without --close-on-exit, the tmux and curl calls equal those recorded in
# no-option-calls.golden. RECORD_GOLDEN_FROM=<script> records that file by
# running the given script.
golden_cases() {
    local out=$1
    : > "$out"
    one() {
        local label=$1
        shift
        run "$tmp/prompt" "$@"
        printf '== %s %s\n' "$label" "$code" >> "$out"
        sed -E -e "s#peer-session-[0-9]+-[0-9]+#peer-session-BUFFER#g" -e "s#$real_tmp#TMP#g" -e "s#$tmp#TMP#g" "$CALL_LOG" >> "$out"
    }
    printf 'Claude input\n' > "$FAKE_PANE"
    printf '%s\n' "$idle_state" > "$FAKE_STATE"
    one idle
    printf '%s\n' "$idle_state" > "$FAKE_STATE.3"
    printf '%s\n' "${idle_state/other-project/}" > "$FAKE_STATE"
    one delayed
    rm "$FAKE_STATE.3"
    SERVER_DOWN=1
    one fallback
    SERVER_DOWN=0
    printf '{"sessions":[]}\n' > "$FAKE_STATE"
    printf 'Starting Claude\n' > "$FAKE_PANE"
    one timeout
    printf 'Do you trust this folder? Yes / No\n' > "$FAKE_PANE"
    one dialog
    printf 'pi input\n' > "$FAKE_PANE"
    printf '%s\n' "${idle_state/claude-code/pi}" > "$FAKE_STATE"
    one pi-idle --agent pi
    printf 'Claude input\n' > "$FAKE_PANE"
    printf '%s\n' "$idle_state" > "$FAKE_STATE"
    printf '%s/elsewhere\tother\n%s\twork\n' "$tmp" "$tmp" > "$FAKE_SESSIONS"
    one attach-match --attach-existing
    one name-id --name peer/x --session-id 123e4567-e89b-42d3-a456-426614174000
}
golden="$root/tests/no-option-calls.golden"
if [[ -n "${RECORD_GOLDEN_FROM:-}" ]]; then
    script=$RECORD_GOLDEN_FROM
    golden_cases "$golden"
    echo "recorded $golden"
else
    golden_cases "$tmp/golden"
    if cmp -s "$golden" "$tmp/golden"; then
        echo "PASS no-option-golden"
    else
        diff "$golden" "$tmp/golden" >&2 || true
        echo "FAIL no-option-golden" >&2
        exit 1
    fi
fi

# Execution cases on a throwaway tmux server with a real zsh and a stand-in
# claude that records its arguments. A stand-in curl answers for tcm and lists
# a conversation in every pane of that server with the status in tcm-status
# once the stand-in claude has started.
if [[ -z "${RECORD_GOLDEN_FROM:-}" ]] && command -v tmux >/dev/null 2>&1 && command -v zsh >/dev/null 2>&1; then
    real=$tmp/real server="peer-session-test-$$"
    mkdir -p "$real/bin" "$real/zdot" "$real/repo"
    trap '"$real/bin/tmux" kill-server 2>/dev/null || true; rm -rf "$tmp"' EXIT
    real_tmux=$(PATH=$orig_path command -v tmux)
    printf '#!/bin/sh\nexec %s -L %s -f /dev/null "$@"\n' "$real_tmux" "$server" > "$real/bin/tmux"
    cat > "$real/bin/claude.stand-in" <<'FAKE'
#!/usr/bin/env python3
import json, os, sys, time
real = os.environ["PEER_REAL"]
open(os.path.join(real, "args"), "w").write(json.dumps(sys.argv[1:]))
print("stand-in claude ready", flush=True)
if open(os.path.join(real, "mode")).read().strip() != "quick":
    time.sleep(60)
FAKE
    cat > "$real/bin/curl" <<'FAKE'
#!/bin/sh
[ -s "$PEER_REAL/tcm-status" ] || exit 7
status=$(cat "$PEER_REAL/tcm-status")
# A conversation exists only once the stand-in claude has started.
[ -e "$PEER_REAL/args" ] || { echo '{"sessions":[]}'; exit 0; }
tmux list-panes -a -F '#{pane_id}' | python3 -c '
import json, sys
agents = [{"agent": "claude-code", "paneId": p, "status": sys.argv[1], "threadName": "peer"}
          for p in sys.stdin.read().split()]
print(json.dumps({"sessions": [{"agents": agents}]}))
' "$status"
FAKE
    # The stand-ins run with the python3 found now, because PATH for the
    # cases can leave out its directory.
    ln -s "$(command -v python3)" "$real/bin/python3"
    chmod +x "$real/bin/"*
    export PEER_REAL=$real
    # PATH for the cases leaves out every directory that holds a real claude.
    real_path=$real/bin
    IFS=: read -r -a dirs <<< "$orig_path"
    for dir in "${dirs[@]}"; do
        [[ -n "$dir" && ! -e "$dir/claude" ]] && real_path+=":$dir"
    done
    # zsh reads no user startup files, so PATH in the pane is real_path.
    PATH=$real_path ZDOTDIR=$real/zdot "$real/bin/tmux" new-session -d -s keep \; set -g default-shell "$(command -v zsh)"
    task=$'First "double" and \'single\' quotes\n$HOME `whoami` $(printf INJECTED)\n-dash line'
    real_run() {
        local label=$1 prompt=$2
        shift 2
        rm -f "$real/args"
        code=0
        PATH=$real_path bash "$script" --repo "$real/repo" --session "$label" --prompt-file "$prompt" --ready-timeout 8 "$@" > "$tmp/out" || code=$?
    }
    real_check() {
        python3 - "$1" "$code" "$tmp/out" "$real" "$2" <<'PY'
import json, os, sys
case, code, out, real, expect = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5]
v = json.loads(open(out).read())
def require(ok, message):
    if not ok:
        sys.exit("FAIL " + case + ": " + message + ": " + str(v))
args = os.path.join(real, "args")
if expect == "task":
    require(code == 0 and v["ok"] and v["prompt_sent"], "success")
    got = json.load(open(args))
    task = open(os.path.join(real, "task-file")).read().rstrip("\n")
    require(got[-2:] == ["--", task], "task arrives as one argument")
elif expect == "refused":
    require(code != 0 and not v["ok"] and "pane" not in v and not os.path.exists(args), "refused before the pane")
else:
    require(code != 0 and not v["ok"] and not os.path.exists(args), "failure without a start")
    require(expect in v.get("pane_tail", ""), "pane tail")
print("PASS " + case)
PY
    }
    printf '%s\n' "$task" > "$real/task-file"
    printf stay > "$real/mode"
    printf working > "$real/tcm-status"
    cp "$real/bin/claude.stand-in" "$real/bin/claude"
    injected="$real/\$(printf INJECTED)"
    mkdir "$injected"
    cp "$real/task-file" "$injected/task"
    (cd "$injected" && real_run injection task --close-on-exit && real_check real-injection task) || exit 1
    [[ ! -e "$real/repo/INJECTED" ]] || { echo "FAIL real-injection: command ran" >&2; exit 1; }
    mkdir "$real/'\$(printf INJECTED)'"
    cp "$real/task-file" "$real/'\$(printf INJECTED)'/task"
    (cd "$real/'\$(printf INJECTED)'" && { real_run quoted task; real_check real-quoted-dir refused; }) || exit 1
    rm "$real/bin/claude"
    : > "$real/tcm-status"
    real_run missing "$real/task-file"; real_check real-not-found "command not found"
    cp "$real/bin/claude.stand-in" "$real/bin/claude"
    cp "$real/task-file" "$real/locked"
    chmod 000 "$real/locked"
    real_run unreadable "$real/locked"; real_check real-unreadable refused
    printf '\n\n\n' > "$real/newlines"
    real_run newlines "$real/newlines"; real_check real-newlines refused
    printf quick > "$real/mode"
    printf "done" > "$real/tcm-status"
    real_run quick "$real/task-file"; real_check real-quick task
    real_run quick-close "$real/task-file" --close-on-exit; real_check real-quick-close task
    pane=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["pane"])' "$tmp/out")
    polls=0
    while "$real/bin/tmux" list-panes -a -F '#{pane_id}' | grep -qx -- "$pane" && (( polls < 20 )); do
        "$(PATH=$orig_path command -v sleep)" 0.1
        polls=$((polls + 1))
    done
    if "$real/bin/tmux" list-panes -a -F '#{pane_id}' | grep -qx -- "$pane"; then
        echo "FAIL real-quick-close-pane-ends: pane stays after a finished task" >&2
        exit 1
    fi
    echo "PASS real-quick-close-pane-ends"
    "$real/bin/tmux" kill-server
else
    echo "SKIP real tmux cases: tmux and zsh are required"
fi
