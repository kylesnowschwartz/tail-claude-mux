---
description: Close finished TCM codex delegate windows in this session (safe sweep by spawn signature)
argument-hint: "[--all-sessions] [--keep N] [--all] [--dry-run]"
allowed-tools: Bash(scripts/tidy-delegates.sh:*)
---
Close the finished TCM codex delegate windows left over from `tcm-delegate` runs.

Scope is the current tmux session by default (`--all-sessions` widens it). The
sweep is deterministic and fails safe: it only targets windows whose pane start
command carries the `codex --profile tcm-delegate` spawn signature, and by
default only closes ones whose TCM status is terminal (done/error/interrupted/
gone). Running or approval-waiting delegates, and windows you opened yourself,
are never touched.

Arguments (`$ARGUMENTS`):
- `--all-sessions` — sweep every tmux session, not just the current one
- `--keep N` — preserve the N most-recent delegate windows
- `--all` — close every matched delegate regardless of status (interrupts live work)
- `--dry-run` — print what would close, change nothing

**Step 1 — preview (changes nothing).** This lists exactly what the sweep would close:

!`scripts/tidy-delegates.sh --dry-run $ARGUMENTS`

**Step 2 — review, then act.**
- If any window in the preview is one I likely still need — a delegate whose output hasn't been read yet, or one worth keeping for reference — do NOT close anything. Name that window, say why you're holding fire, and ask me first.
- Otherwise run the sweep for real and report the one-line result (closed / kept / skipped, and the scope):

  `scripts/tidy-delegates.sh $ARGUMENTS`

Do not add `--all` or `--all-sessions` unless I asked for them.
