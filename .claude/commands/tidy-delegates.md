---
description: Close finished TCM codex delegate windows (safe sweep by spawn signature)
argument-hint: "[--keep N] [--all] [--dry-run]"
allowed-tools: Bash(scripts/tidy-delegates.sh:*)
---
Close the finished TCM codex delegate windows left over from `tcm-delegate` runs.

The sweep is deterministic and fails safe: it only targets tmux windows whose
pane start command carries the `codex --profile tcm-delegate` spawn signature,
and by default only closes ones where codex has already exited (pane dropped
back to a login shell). A delegate still working — or paused at an approval
prompt — has a non-shell foreground process and is never touched. Windows you
opened yourself are never matched.

Arguments (`$ARGUMENTS`):
- `--keep N` — preserve the N most-recent delegate windows, close the older finished ones
- `--all` — also close delegates that are still active (interrupts live work)
- `--dry-run` — print what would close, change nothing

Running the sweep:

!`scripts/tidy-delegates.sh $ARGUMENTS`

Report the result to me in one line: how many windows closed, kept, and skipped-running.
