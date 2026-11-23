#!/usr/bin/env bash
# Checks that the skill folder holds what SKILL.md and its reference files
# name, so that a path relative to the skill folder resolves in Claude Code
# and in pi.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
python3 - "$root" <<'PY'
import json, os, pathlib, re, sys
root = pathlib.Path(sys.argv[1])
skill = root / "skills" / "peer-session"
def require(ok, case, message):
    if not ok:
        sys.exit("FAIL " + case + ": " + message)
    print("PASS " + case)
docs = [skill / "SKILL.md", *sorted((skill / "references").glob("*.md"))]
text = {doc: doc.read_text() for doc in docs}
front = text[docs[0]].split("---")[1]
name = re.search(r"^name: (.+)$", front, re.M).group(1)
description = re.search(r"^description: (.+)$", front, re.M).group(1)
require(name == skill.name == json.loads((root / ".claude-plugin" / "plugin.json").read_text())["name"],
        "name", "the skill, its folder, and the plugin share one name")
require(0 < len(description) <= 1024, "description-length", str(len(description)) + " characters; pi loads at most 1024")
scripts = sorted({m for body in text.values() for m in re.findall(r"\bscripts/[A-Za-z0-9._-]+\.sh\b", body)})
require(scripts == sorted("scripts/" + f.name for f in (skill / "scripts").glob("*.sh")),
        "scripts-named", "the skill names exactly the scripts in its folder: " + str(scripts))
require(all(os.access(skill / s, os.X_OK) for s in scripts), "scripts-executable", str(scripts))
links = sorted({m for body in text.values() for m in re.findall(r"\]\((references/[^)#]+)", body)})
require(links and all((skill / link).is_file() for link in links), "references-exist", str(links))
env = [str(doc) for doc, body in text.items() if re.search(r"\$\{?CLAUDE_[A-Z_]+", body)]
require(not env, "no-harness-variable", "a path depends on a Claude Code variable: " + str(env))
PY
