#!/bin/bash
# Committed counterpart of tcm-watch.js's legScript(p). Behavior-frozen:
# see .agent-history or the workflow's git log for the extraction commit.
# Args: PANE SESH INTERVAL LEG_SECONDS QCOUNT LAST_HASH
#   PANE       - pane id, or "" if unknown (falls back to /state polling)
#   SESH       - TCM session name to watch
#   INTERVAL   - fallback poll interval in seconds
#   LEG_SECONDS- self-deadline in seconds (this leg exits and lets the
#                caller start a fresh leg once SECONDS reaches this budget)
#   QCOUNT     - seed quiescence count carried over from the previous leg
#   LAST_HASH  - seed pane-content hash carried over from the previous leg
#                ("none" if there is no prior hash)
#
# GET /wait is the primary signal — the server reconciles hook status
# against the codex thread's rollout evidence, so its done/error/interrupted
# are trustworthy and arrive with zero polling. The pane-quiescence branch
# only runs when /wait is unusable (server down or session untracked), and
# the seed QCOUNT/LAST_HASH continue the previous leg's quiescence streak.
# A "waiting" wake is confirmed 5s later before being reported, because
# approval prompts can flash and self-resolve.
PANE="$1"
SESH="$2"
INTERVAL="$3"
LEG_SECONDS="$4"
QCOUNT="$5"
LAST_HASH="${6:-none}"
SELF_DEADLINE=$((SECONDS + LEG_SECONDS))
POLLS=0
LAST_ST='none'
UNREADABLE=0
wait_status() {
  # one long-poll; prints "STATUS TIMEDOUT" iff the response is real /wait JSON
  local t=$1 resp
  if [ -n "$PANE" ]; then
    resp=$(curl -fsS -G 'localhost:7391/wait' --data-urlencode "session=$SESH" --data-urlencode "timeout=$t" --data-urlencode "pane=$PANE" 2>/dev/null) || return 1
  else
    resp=$(curl -fsS "localhost:7391/wait?session=$SESH&timeout=$t" 2>/dev/null) || return 1
  fi
  printf '%s' "$resp" | jq -er '"\(.status) \(.timedOut)"' 2>/dev/null
}
while true; do
  if ((SECONDS >= SELF_DEADLINE)); then
    echo "RESOLUTION=continue POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$LAST_ST NOTE=deadline"
    exit 0
  fi
  if ! tmux has-session -t "=$SESH" 2>/dev/null; then
    echo "RESOLUTION=session-dead POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$LAST_ST NOTE=session-gone"
    exit 0
  fi
  REMAIN=$((SELF_DEADLINE - SECONDS))
  ((REMAIN > 540)) && REMAIN=540
  if ((REMAIN >= 5)) && wr=$(wait_status "$REMAIN"); then
    POLLS=$((POLLS + 1))
    st=${wr%% *}
    to=${wr##* }
    LAST_ST="$st"
    case "$st" in
    done)
      echo "RESOLUTION=finished POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-done"
      exit 0
      ;;
    error)
      echo "RESOLUTION=error POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-error"
      exit 0
      ;;
    interrupted)
      echo "RESOLUTION=error POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-interrupted"
      exit 0
      ;;
    gone)
      echo "RESOLUTION=session-dead POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-gone"
      exit 0
      ;;
    waiting)
      if [ "$to" = "true" ]; then continue; fi
      # observed transient: an approval prompt can flash and self-resolve;
      # confirm it holds for 5s before reporting
      sleep 5
      if wr2=$(wait_status 5); then
        st2=${wr2%% *}
        if [ "$st2" = "waiting" ]; then
          echo "RESOLUTION=waiting POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=waiting NOTE=wait-waiting-confirmed"
          exit 0
        fi
        LAST_ST="$st2"
      fi
      continue
      ;;
    *) continue ;;
    esac
  fi
  # fallback: /wait unusable (server down, pre-B2 binary, untracked session)
  st=$(curl -fsS localhost:7391/state 2>/dev/null | jq -r --arg s "$SESH" '[.. | objects | select(.session? == $s) | .status] | first // empty' 2>/dev/null)
  [ -n "$st" ] && LAST_ST="$st"
  if [ "$st" = "error" ]; then
    echo "RESOLUTION=error POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=state-error"
    exit 0
  fi
  if [ "$st" = "running" ]; then
    QCOUNT=0
  else
    content=$(tmux capture-pane -p -t "$PANE" 2>/dev/null)
    if [ -z "$content" ]; then
      UNREADABLE=$((UNREADABLE + 1))
      if ((UNREADABLE >= 3)); then
        echo "RESOLUTION=unverified POLLS=$POLLS QCOUNT=$QCOUNT HASH=none STATE=$LAST_ST NOTE=pane-unreadable"
        exit 0
      fi
    else
      UNREADABLE=0
      h=$(printf '%s' "$content" | md5 -q)
      if [ "$h" = "$LAST_HASH" ]; then
        QCOUNT=$((QCOUNT + 1))
        if ((QCOUNT >= 3)); then
          tl=$(printf '%s' "$content" | grep -v '^[[:space:]]*$' | tail -4)
          if printf '%s' "$tl" | grep -qiE 'press enter|y/n|approve|allow|permission|continue\?|› *[0-9]\.'; then
            echo "RESOLUTION=waiting POLLS=$POLLS QCOUNT=$QCOUNT HASH=$h STATE=$LAST_ST NOTE=quiescent-at-prompt"
          else
            echo "RESOLUTION=finished POLLS=$POLLS QCOUNT=$QCOUNT HASH=$h STATE=$LAST_ST NOTE=pane-quiescent"
          fi
          exit 0
        fi
      else
        QCOUNT=0
        LAST_HASH="$h"
      fi
    fi
  fi
  POLLS=$((POLLS + 1))
  sleep "$INTERVAL"
done
