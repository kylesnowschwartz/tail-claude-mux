#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
export CALL_LOG="$tmp/calls" FAKE_STATE="$tmp/state" FAKE_PANE="$tmp/pane"
export EXISTING=0 SERVER_DOWN=0 FAKE_COUNT="$tmp/count"
cat > "$tmp/bin/tmux" <<'FAKE'
#!/usr/bin/env bash
set -eu
python3 - "$@" >> "$CALL_LOG" <<'PY'
import json, sys
print(json.dumps(sys.argv[1:]))
PY
case "$1" in
    has-session) [[ "$EXISTING" == 1 ]] ;;
    new-session) printf '%%128\n' ;;
    capture-pane) cat "$FAKE_PANE" ;;
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
if [[ -f "$FAKE_STATE.$count" ]]; then cat "$FAKE_STATE.$count"; else cat "$FAKE_STATE"; fi
FAKE
cat > "$tmp/bin/sleep" <<'FAKE'
#!/usr/bin/env bash
printf 'sleep %s\n' "$*" >> "$CALL_LOG.extra"
FAKE
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"
printf 'Task line one\nTask line two\n' > "$tmp/prompt"
run() {
    : > "$CALL_LOG"
    : > "$CALL_LOG.extra"
    printf 0 > "$FAKE_COUNT"
    code=0
    local prompt=$1
    shift
    bash "$root/scripts/start-peer-session.sh" --repo "$tmp" --session peer --prompt-file "$prompt" --ready-timeout 7 "$@" > "$tmp/out" || code=$?
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
if case in ("existing", "missing", "empty", "pi-mode", "bad-agent"):
    require(code != 0 and v["ok"] is False, "validation must fail")
    require("new-session" not in names, "validation created session")
    if case == "pi-mode":
        require("--permission-mode" in v["error"], "pi mode error")
    if case == "bad-agent":
        require("--agent" in v["error"], "agent error")
else:
    require(names[:3] == ["has-session", "new-session", "send-keys"], "startup order")
    require(calls[0] == ["has-session", "-t", "=peer"], "exact session match")
    require(calls[1] == ["new-session", "-d", "-P", "-F", "#{pane_id}", "-s",
                        "peer", "-n", "pi" if pi else "claude", "-x", "200", "-y", "50", "-c", str(p)],
            "session arguments")
    require(calls[2] == ["send-keys", "-t", "%128",
                         "pi" if pi else "claude --permission-mode auto", "Enter"],
            "launch command")
    require(v.get("agent") == ("pi" if pi else None), "agent field")
    for c in calls[2:]:
        if c[0] != "load-buffer":
            require("-t" in c and c[c.index("-t")+1] == "%128", "pane target: " + str(c))
    if case in ("idle", "fallback", "delayed", "unnamed", "pi-idle"):
        require(code == 0 and v["ok"] and v["prompt_sent"], "success")
        require(v["ready"] == ("pane-stable" if case == "fallback" else "idle"), "ready")
        require(v["peer_name"] == ("other-project" if case in ("idle", "delayed", "pi-idle") else ""), "peer")
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
