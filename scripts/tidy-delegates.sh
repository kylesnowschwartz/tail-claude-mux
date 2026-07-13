#!/usr/bin/env bash
# tidy-delegates.sh — close finished TCM codex delegate windows.
#
# A delegate window is identified by the DURABLE spawn signature: its pane's
# start command runs `codex --profile tcm-delegate` (set by
# .claude/workflows/lib/tcm-spawn.sh). This never matches a codex shell you
# opened yourself, so the sweep is safe to run at any time.
#
# Scope: the CURRENT tmux session by default; pass --all-sessions to sweep every
# session. "Finished" is the TCM server's tracker status, NOT the pane's
# process: a delegate's codex keeps idling interactively after finishing a turn,
# so process state can't tell "done" from "still working". GET /result?session&
# pane returns the authoritative status; a delegate is closeable when that
# status is terminal — done, error, interrupted, or gone. Running or waiting
# (paused at an approval prompt) delegates are left alone. Fails safe: an
# unreadable status (server down, untracked) is skipped, never killed, unless
# --all is given. Requires the TCM server on localhost:7391.
#
# Usage:
#   tidy-delegates.sh                 close terminal-status delegates in this session
#   tidy-delegates.sh --all-sessions  sweep every tmux session, not just this one
#   tidy-delegates.sh --keep N        keep the N most-recent delegate windows
#   tidy-delegates.sh --all           close EVERY matched delegate regardless of
#                                     status (interrupts running/waiting work)
#   tidy-delegates.sh --dry-run       print what would close, change nothing
#
# Exit status: 0 on success (including "nothing to close").
set -euo pipefail

BASE='http://localhost:7391'

KEEP=0
DRY=0
ALL=0
ALL_SESSIONS=0
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
  --all-sessions)
    ALL_SESSIONS=1
    shift
    ;;
  -h | --help)
    sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
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

# Default scope is the current session. Resolve it the same way tcm-spawn.sh
# resolves the owning session; if we can't (not attached to tmux), the caller
# must opt into a global sweep explicitly.
CURRENT=""
if [ "$ALL_SESSIONS" -eq 0 ]; then
  CURRENT=$(tmux display-message -p '#{session_name}' 2>/dev/null || true)
  if [ -z "$CURRENT" ]; then
    echo "tidy-delegates: can't determine the current tmux session; pass --all-sessions to sweep every session" >&2
    exit 2
  fi
fi

# The spawn builds `sh -c` with each token individually quoted, so the
# invocation reads as 'codex' '--profile' 'tcm-delegate' — the tokens never
# appear space-separated in pane_start_command. Match with a regex that
# tolerates the interleaved quotes rather than a literal substring.
SIG_RE='codex.*--profile.*tcm-delegate'
# TCM statuses that mean the delegate's turn is over and it is safe to close,
# even if its codex process is still idling at the prompt.
CLOSEABLE_RE='^(done|error|interrupted|gone)$'

# For each delegate pane (in scope), ask the server for its authoritative status
# and record it against the window. STATUS_OF is the pane's status ("unknown" if
# unreadable); CLOSE_OF[wid]=1 once any delegate pane in the window is terminal.
declare -A ACT_OF CLOSE_OF STATUS_OF
UNKNOWN=0
while IFS=$'\t' read -r wid wact sess pane start; do
  [ -z "$wid" ] && continue
  [[ "$start" =~ $SIG_RE ]] || continue
  if [ "$ALL_SESSIONS" -eq 0 ] && [ "$sess" != "$CURRENT" ]; then
    continue
  fi
  if [ -z "${ACT_OF[$wid]:-}" ] || [ "$wact" -gt "${ACT_OF[$wid]}" ]; then
    ACT_OF[$wid]="$wact"
  fi
  : "${CLOSE_OF[$wid]:=0}"
  status=$(curl -fsS -G "$BASE/result" --data-urlencode "session=$sess" --data-urlencode "pane=$pane" 2>/dev/null | jq -r '.status // empty' 2>/dev/null || true)
  STATUS_OF[$wid]="${status:-unknown}"
  if [ -z "$status" ]; then
    UNKNOWN=$((UNKNOWN + 1))
  elif [[ "$status" =~ $CLOSEABLE_RE ]]; then
    CLOSE_OF[$wid]=1
  fi
done < <(tmux list-panes -a -F '#{window_id}	#{window_activity}	#{session_name}	#{pane_id}	#{pane_start_command}' 2>/dev/null)

# One line per delegate window, "<activity-epoch> <window_id> <closeable>",
# newest first so --keep preserves the most-recent windows.
WINDOWS=()
for wid in "${!ACT_OF[@]}"; do
  WINDOWS+=("${ACT_OF[$wid]} $wid ${CLOSE_OF[$wid]}")
done
if [ "${#WINDOWS[@]}" -gt 0 ]; then
  mapfile -t WINDOWS < <(printf '%s\n' "${WINDOWS[@]}" | sort -rn)
fi

SCOPE="session '$CURRENT'"
[ "$ALL_SESSIONS" -eq 1 ] && SCOPE="all sessions"
if [ "${#WINDOWS[@]}" -eq 0 ]; then
  echo "tidy-delegates: no TCM codex delegate windows found in $SCOPE."
  exit 0
fi

CLOSED=0
KEPT=0
SKIPPED=0
IDX=0
for row in "${WINDOWS[@]}"; do
  wid="${row#* }"
  wid="${wid%% *}"
  closeable="${row##* }"
  status="${STATUS_OF[$wid]:-unknown}"
  IDX=$((IDX + 1))

  # --keep N: preserve the N most-recent delegate windows (already sorted newest-first).
  if [ "$KEEP" -gt 0 ] && [ "$IDX" -le "$KEEP" ]; then
    KEPT=$((KEPT + 1))
    echo "keep    $wid (most-recent #$IDX, status=$status)"
    continue
  fi

  if [ "$closeable" -eq 0 ] && [ "$ALL" -eq 0 ]; then
    SKIPPED=$((SKIPPED + 1))
    echo "skip    $wid (status=$status; not terminal — pass --all to force)"
    continue
  fi

  if [ "$DRY" -eq 1 ]; then
    echo "would close $wid (status=$status)"
    CLOSED=$((CLOSED + 1))
    continue
  fi
  if tmux kill-window -t "$wid" 2>/dev/null; then
    echo "closed  $wid (status=$status)"
    CLOSED=$((CLOSED + 1))
  else
    echo "gone    $wid (already closed)"
  fi
done

verb="closed"
[ "$DRY" -eq 1 ] && verb="would close"
echo "tidy-delegates: $verb $CLOSED, kept $KEPT, skipped $SKIPPED (scope: $SCOPE)."
if [ "$UNKNOWN" -gt 0 ] && [ "$ALL" -eq 0 ]; then
  echo "tidy-delegates: $UNKNOWN pane(s) had no readable status (TCM server on ${BASE}?); left untouched."
fi
