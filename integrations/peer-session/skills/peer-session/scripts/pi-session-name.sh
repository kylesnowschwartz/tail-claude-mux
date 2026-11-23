#!/usr/bin/env bash
# Prints the name and id under which the pi-agent-fleet extension lists the pi
# session that runs this script. The extension keeps one record file for each
# running pi session, and the record holds the process id of that session.
set -euo pipefail
exec python3 - "$$" <<'PY'
import json, os, pathlib, subprocess, sys

def out(value, code):
    print(json.dumps(value, separators=(",", ":")))
    sys.exit(code)

def alive(pid):
    try:
        os.kill(pid, 0)
    except PermissionError:
        return True
    except OSError:
        return False
    return True

agent_dir = os.environ.get("PI_CODING_AGENT_DIR") or "~/.pi/agent"
folder = pathlib.Path(os.environ.get("PI_AGENT_FLEET_PEERS_DIR")
                      or os.path.join(os.path.expanduser(agent_dir), "pi-agent-fleet-peers"))
# This process and each process above it, nearest first.
ancestors, pid = [], int(sys.argv[1])
while pid > 1 and pid not in ancestors:
    ancestors.append(pid)
    try:
        pid = int(subprocess.run(["ps", "-o", "ppid=", "-p", str(pid)], capture_output=True,
                                 text=True, check=True).stdout.strip())
    except (subprocess.CalledProcessError, ValueError):
        break
records = []
for file in sorted(folder.glob("*.json")):
    try:
        record = json.loads(file.read_text())
        if (record.get("id") == file.stem and isinstance(record.get("name"), str)
                and isinstance(record.get("pid"), int) and record["pid"] > 0):
            records.append(record)
    except (OSError, ValueError, AttributeError):
        pass
own = min((r for r in records if r["pid"] in ancestors),
          key=lambda r: ancestors.index(r["pid"]), default=None)
if own is None:
    out({"ok": False, "error": "No record in " + str(folder) + " belongs to this process. "
         "Run this from an interactive pi session that has the pi-agent-fleet extension."}, 1)
# Running sessions that share a name are each listed as name#<first 4 of id>.
shared = [r for r in records if r["name"] == own["name"] and alive(r["pid"])]
name = own["name"] + "#" + own["id"][:4] if len(shared) > 1 else own["name"]
out({"ok": True, "name": name, "id": own["id"], "cwd": own.get("cwd", "")}, 0)
PY
