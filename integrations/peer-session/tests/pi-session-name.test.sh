#!/usr/bin/env bash
# Runs pi-session-name.sh against a stand-in peers folder. This shell stands
# in for the pi session: the script runs under it, so its process id is the
# one a record must hold.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
script="$root/skills/peer-session/scripts/pi-session-name.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export PI_AGENT_FLEET_PEERS_DIR="$tmp/peers"
mkdir "$tmp/peers"
record() { printf '{"id":"%s","name":"%s","cwd":"/work/%s","sessionId":"s","pid":%s}\n' "$1" "$2" "$1" "$3" > "$tmp/peers/$1.json"; }
check() {
    local label=$1 expect=$2 code=0 got
    got=$(bash "$script") || code=$?
    if [[ "$got" != "$expect" || $(( code == 0 )) != "$3" ]]; then
        echo "FAIL $label: exit $code: $got" >&2
        exit 1
    fi
    echo "PASS $label"
}
missing='{"ok":false,"error":"No record in '"$tmp"'/peers belongs to this process. Run this from an interactive pi session that has the pi-agent-fleet extension."}'
check no-records "$missing" 0
# A process id that no process holds.
gone=99999
while kill -0 "$gone" 2>/dev/null; do gone=$((gone - 1)); done
record aaaaaaaaaaaa other 1
check other-session "$missing" 0
record bbbbbbbbbbbb peer/topic "$$"
check named '{"ok":true,"name":"peer/topic","id":"bbbbbbbbbbbb","cwd":"/work/bbbbbbbbbbbb"}' 1
record cccccccccccc peer/topic "$gone"
check shared-name-ended '{"ok":true,"name":"peer/topic","id":"bbbbbbbbbbbb","cwd":"/work/bbbbbbbbbbbb"}' 1
record cccccccccccc peer/topic 1
check shared-name '{"ok":true,"name":"peer/topic#bbbb","id":"bbbbbbbbbbbb","cwd":"/work/bbbbbbbbbbbb"}' 1
printf 'not json\n' > "$tmp/peers/dddddddddddd.json"
record eeeeeeeeeeee wrong-file "$$"
mv "$tmp/peers/eeeeeeeeeeee.json" "$tmp/peers/ffffffffffff.json"
check bad-files-skipped '{"ok":true,"name":"peer/topic#bbbb","id":"bbbbbbbbbbbb","cwd":"/work/bbbbbbbbbbbb"}' 1
unset PI_AGENT_FLEET_PEERS_DIR
export PI_CODING_AGENT_DIR="$tmp/agent"
mkdir -p "$tmp/agent/pi-agent-fleet-peers"
cp "$tmp/peers/bbbbbbbbbbbb.json" "$tmp/agent/pi-agent-fleet-peers/"
check agent-dir '{"ok":true,"name":"peer/topic","id":"bbbbbbbbbbbb","cwd":"/work/bbbbbbbbbbbb"}' 1
