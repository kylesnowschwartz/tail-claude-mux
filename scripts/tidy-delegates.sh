#!/usr/bin/env bash
# tidy-delegates.sh — close finished TCM codex delegate windows.
#
# A delegate window is identified by the DURABLE spawn signature: its pane's
# start command contains `codex --profile tcm-delegate` (set by
# .claude/workflows/lib/tcm-spawn.sh). This never matches a codex shell you
# opened yourself, so the sweep is safe to run at any time.
#
# "Finished" is decided locally, no server needed, and fails SAFE: the spawn
# wrapper (tcm-spawn.sh) runs `... codex ...; exec "${SHELL:-sh}"`, so a
# delegate is finished ONLY once its pane has dropped back to a login shell
# (bash/zsh/sh/fish). While codex works the foreground process is `bun`/`node`
# (codex ships as a node/bun wrapper, NOT a process literally named `codex`) or
# whatever tool it spawned (git, etc.) — anything that is not a recognised login
# shell is treated as still-active and left alone. So the default sweep closes a
# window only when it is provably idle at a shell prompt; when in doubt it skips.
#
# Usage:
#   tidy-delegates.sh                 close every FINISHED delegate window
#   tidy-delegates.sh --keep N        keep the N most-recent delegate windows,
#                                     close the rest of the finished ones
#   tidy-delegates.sh --all           also close delegates still active
#                                     (destructive — interrupts live work)
#   tidy-delegates.sh --dry-run       print what would close, change nothing
#
# Exit status: 0 on success (including "nothing to close").
set -euo pipefail

KEEP=0
DRY=0
ALL=0
while [ $# -gt 0 ]; do
  case "$1" in
  --keep)
    KEEP="${2:-0}"
    shift 2
    ;;
  --dry-run)
    DRY=1
    shift
    ;;
  --all)
    ALL=1
    shift
    ;;
  -h | --help)
    sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "tidy-delegates: unknown argument '$1'" >&2
    exit 2
    ;;
  esac
done

case "$KEEP" in
'' | *[!0-9]*)
  echo "tidy-delegates: --keep needs a non-negative integer, got '$KEEP'" >&2
  exit 2
  ;;
esac

if ! command -v tmux >/dev/null 2>&1; then
  echo "tidy-delegates: tmux not found" >&2
  exit 1
fi
if ! tmux info >/dev/null 2>&1; then
  echo "tidy-delegates: no tmux server running; nothing to tidy" >&2
  exit 0
fi

SIG='codex --profile tcm-delegate'

# Classify each delegate window as active or finished.
#
# We can't trust `pane_current_command`: tmux reports the wrapper shell for a
# child process, so a running codex can read as `sh`/`bash` (verified) — a
# name-based heuristic would misclassify live work as finished and kill it.
#
# Instead we read the wrapper contract directly. tcm-spawn.sh runs
# `... codex ...; exec "${SHELL:-sh}"`, so while codex works the DELEGATE pane's
# own tty carries a codex runtime process (codex ships as a bun/node wrapper);
# once it exits, exec replaces the process with the bare login shell and no
# runtime remains. So: active iff the delegate pane's tty has a bun/node/codex
# process. We inspect ONLY the signature-matching pane's tty — a companion pane
# in the same window runs the TCM TUI (also bun) and would otherwise pin every
# window as active. This fails safe: if ps can't read the tty we assume active.
declare -A ACT_OF ACTIVE_OF
while IFS=$'\t' read -r wid wact ptty start; do
  [ -z "$wid" ] && continue
  case "$start" in
  *"$SIG"*) ;;
  *) continue ;;
  esac
  if [ -z "${ACT_OF[$wid]:-}" ] || [ "$wact" -gt "${ACT_OF[$wid]}" ]; then
    ACT_OF[$wid]="$wact"
  fi
  : "${ACTIVE_OF[$wid]:=0}"
  # Default this pane to active; only prove it finished when ps positively
  # reads the tty and finds no codex runtime. An empty tty or unreadable ps
  # leaves it active — never kill on missing evidence.
  active=1
  tty=${ptty#/dev/}
  if [ -n "$tty" ]; then
    # pgrep -t is unreliable on macOS; ps -t is the portable read here.
    # shellcheck disable=SC2009
    comms=$(ps -t "$tty" -o comm= 2>/dev/null || true)
    if [ -n "$comms" ] && ! printf '%s\n' "$comms" | grep -qiE '(^|/)(bun|node|codex)$'; then
      active=0
    fi
  fi
  [ "$active" -eq 1 ] && ACTIVE_OF[$wid]=1
done < <(tmux list-panes -a -F '#{window_id}	#{window_activity}	#{pane_tty}	#{pane_start_command}' 2>/dev/null)

# One line per delegate window, "<activity-epoch> <window_id> <active>", newest
# first so --keep preserves the most-recent windows.
WINDOWS=()
for wid in "${!ACT_OF[@]}"; do
  WINDOWS+=("${ACT_OF[$wid]} $wid ${ACTIVE_OF[$wid]}")
done
if [ "${#WINDOWS[@]}" -gt 0 ]; then
  mapfile -t WINDOWS < <(printf '%s\n' "${WINDOWS[@]}" | sort -rn)
fi

if [ "${#WINDOWS[@]}" -eq 0 ]; then
  echo "tidy-delegates: no TCM codex delegate windows found."
  exit 0
fi

CLOSED=0
KEPT=0
SKIPPED_ACTIVE=0
IDX=0
for row in "${WINDOWS[@]}"; do
  wid="${row#* }"
  wid="${wid%% *}"
  active="${row##* }"
  IDX=$((IDX + 1))

  # --keep N: preserve the N most-recent delegate windows (already sorted newest-first).
  if [ "$KEEP" -gt 0 ] && [ "$IDX" -le "$KEEP" ]; then
    KEPT=$((KEPT + 1))
    echo "keep    $wid (most-recent #$IDX)"
    continue
  fi

  if [ "$active" -eq 1 ] && [ "$ALL" -eq 0 ]; then
    SKIPPED_ACTIVE=$((SKIPPED_ACTIVE + 1))
    echo "skip    $wid (not at a shell prompt — still active; pass --all to force)"
    continue
  fi

  label="finished"
  [ "$active" -eq 1 ] && label="ACTIVE"
  if [ "$DRY" -eq 1 ]; then
    echo "would close $wid ($label)"
    CLOSED=$((CLOSED + 1))
    continue
  fi
  if tmux kill-window -t "$wid" 2>/dev/null; then
    echo "closed  $wid ($label)"
    CLOSED=$((CLOSED + 1))
  else
    echo "gone    $wid (already closed)"
  fi
done

verb="closed"
[ "$DRY" -eq 1 ] && verb="would close"
echo "tidy-delegates: $verb $CLOSED, kept $KEPT, skipped-active $SKIPPED_ACTIVE."
