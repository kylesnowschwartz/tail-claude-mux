#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
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
    display-message) if [[ -f "$FAKE_PANE.dead" ]]; then cat "$FAKE_PANE.dead"; else printf '0\n'; fi ;;
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
import json, pathlib, sys
p, case, code = pathlib.Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
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
if case in ("existing", "attach-taken", "missing", "empty", "pi-mode", "bad-agent", "bad-name", "pi-name", "bad-id", "pi-id"):
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
    expected = "pi" if pi else "claude --permission-mode auto"
    if case == "name":
        expected += " -n liaison/s4-agent-test"
    if case == "session-id":
        expected += " --session-id 123e4567-e89b-42d3-a456-426614174000"
    if case == "session-id-name":
        expected += " --session-id 123e4567-e89b-42d3-a456-426614174000 -n liaison/x"
    require(launch == ["send-keys", "-t", "%128", expected, "Enter"],
            "launch command")
    require(v.get("agent") == ("pi" if pi else None), "agent field")
    for c in calls[2:]:
        if c[0] != "load-buffer":
            require("-t" in c and c[c.index("-t")+1] == "%128", "pane target: " + str(c))
    if case in ("idle", "name", "session-id", "session-id-name", "fallback", "delayed", "unnamed", "pi-idle", "attach-match", "attach-fallback", "attach-none"):
        require(code == 0 and v["ok"] and v["prompt_sent"], "success")
        require(v["ready"] == ("pane-stable" if case in ("fallback", "attach-fallback") else "idle"), "ready")
        require(v["peer_name"] == ("other-project" if case in ("idle", "name", "session-id", "session-id-name", "delayed", "pi-idle", "attach-match", "attach-none") else ""), "peer")
        require(names[-3:] == ["load-buffer", "paste-buffer", "send-keys"], "prompt order")
        if case == "delayed":
            require(events.count("curl") == 3, "name poll count")
            require(events.index("curl", events.index("curl", events.index("curl") + 1) + 1) < next(i for i, e in enumerate(events) if e.startswith('["paste-buffer"')), "paste follows name")
        if case == "unnamed":
            require(events.count("curl") <= 6, "bounded name wait")
        load, paste = calls[-3:-1]
        require("-p" in paste and "-d" in paste, "paste flags")
        require(load[load.index("-b")+1] == paste[paste.index("-b")+1], "named buffer")
        require(load[-1] == str(p / "prompt"), "prompt file")
        require(calls[-1] == ["send-keys", "-t", "%128", "Enter"], "submit")
        require("sleep 0.5" in (p / "calls.extra").read_text(), "paste pause")
        require(all("-l" not in c for c in calls), "literal send-keys")
    else:
        require(code != 0 and not v["ok"] and not v["prompt_sent"], "failure")
        require(v["ready"] == case.removeprefix("pi-") and "remains running" in v["error"], "failure details")
        require("paste-buffer" not in names and "load-buffer" not in names, "unexpected prompt")
print("PASS " + case)
PY
}
printf 'Claude input\n' > "$FAKE_PANE"
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt"; check idle
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
SERVER_DOWN=0
printf 'pi input\n' > "$FAKE_PANE"
printf '%s\n' '{"sessions":[{"agents":[{"agent":"pi","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt" --agent pi; check pi-idle
printf '%s\n' '{"sessions":[{"agents":[{"agent":"claude-code","paneId":"%128","status":"idle","threadName":"other-project"}]}]}' > "$FAKE_STATE"
run "$tmp/prompt" --agent pi; check pi-timeout
run "$tmp/prompt" --agent pi --permission-mode auto; check pi-mode
run "$tmp/prompt" --agent codex; check bad-agent
printf 'Claude input\n' > "$FAKE_PANE"
run "$tmp/prompt" --name liaison/s4-agent-test; check name
run "$tmp/prompt" --name 'bad name; rm'; check bad-name
run "$tmp/prompt" --name '-leading'; check bad-name
run "$tmp/prompt" --agent pi --name liaison/x; check pi-name
run "$tmp/prompt" --session-id 123e4567-e89b-42d3-a456-426614174000; check session-id
run "$tmp/prompt" --session-id 123e4567-e89b-42d3-a456-426614174000 --name liaison/x; check session-id-name
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
import json, pathlib, sys
p, case, code = pathlib.Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
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
require(launch[3].startswith("exec claude --permission-mode auto"), "exec launch: " + str(launch))
require(calls.index(on) < calls.index(launch), "remain-on-exit on before launch")
if case == "close-idle":
    require(code == 0 and v["ok"] and v["prompt_sent"] and v["ready"] == "idle", "success")
    i_off, i_paste = calls.index(off), names.index("paste-buffer")
    require(names.index("load-buffer") < i_off and i_paste == i_off + 1, "off right before paste")
    require(events.count("curl") == 2, "two tcm checks")
    require("pane_tail" not in v, "no tail on success")
else:
    require(code != 0 and not v["ok"] and not v["prompt_sent"], "failure")
    require(off not in calls and "paste-buffer" not in names, "no paste, setting stays on")
    require("Claude input" in v.get("pane_tail", ""), "pane tail")
    require("remains running" not in v["error"], "no running claim")
    expect = {"close-down": ("timeout", "tcm gave no answer"),
              "close-busy": ("timeout", "no idle conversation"),
              "close-changed": ("unconfirmed", "'running'"),
              "close-gone": ("unconfirmed", "tcm gave no answer"),
              "close-dead": ("exited", "kept for reading"),
              "close-dialog": ("dialog", "trust dialog")}[case]
    require(v["ready"] == expect[0] and expect[1] in v["error"], "details: " + str(v))
    if v["ready"] == "unconfirmed":
        load = calls[names.index("load-buffer")]
        require(["delete-buffer", "-b", load[load.index("-b") + 1]] in calls, "buffer deleted")
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
export DOWN_AT=2
run "$tmp/prompt" --close-on-exit; close_check close-gone
DOWN_AT=
SERVER_DOWN=1
run "$tmp/prompt" --close-on-exit; close_check close-down
SERVER_DOWN=0
printf '%s\n' "${idle_state/\"idle\"/\"running\"}" > "$FAKE_STATE"
run "$tmp/prompt" --close-on-exit; close_check close-busy
printf '%s\n' "$idle_state" > "$FAKE_STATE.1"
run "$tmp/prompt" --close-on-exit; close_check close-changed
rm "$FAKE_STATE.1"
printf '1\n' > "$FAKE_PANE.dead"
run "$tmp/prompt" --close-on-exit; close_check close-dead
rm "$FAKE_PANE.dead"
printf 'Claude input\nDo you trust this folder? Yes / No\n' > "$FAKE_PANE"
run "$tmp/prompt" --close-on-exit; close_check close-dialog
printf 'Claude input\n' > "$FAKE_PANE"
printf '%s\n' "$idle_state" > "$FAKE_STATE"
export FAIL_CMD=paste-buffer
run "$tmp/prompt" --close-on-exit
FAIL_CMD=
python3 - "$tmp/out" "$code" <<'PY'
import json, sys
v = json.loads(open(sys.argv[1]).read())
if sys.argv[2] == "0" or v["error"] != "Command failed; tmux session 'peer' remains running." or "pane_tail" in v:
    sys.exit("FAIL close-paste-fails: " + str(v))
print("PASS close-paste-fails")
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
        sed -E -e "s#peer-session-[0-9]+-[0-9]+#peer-session-BUFFER#g" -e "s#$tmp#TMP#g" "$CALL_LOG" >> "$out"
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
    one name-id --name liaison/x --session-id 123e4567-e89b-42d3-a456-426614174000
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
