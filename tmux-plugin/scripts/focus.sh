#!/usr/bin/env sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/server-common.sh"

find_sidebar_pane() {
  tmux list-panes -t "$1" -F '#{pane_id} #{pane_title}' 2>/dev/null | awk '$2 == "opensessions" { print $1; exit }'
}

sidebar_visible() {
  tmux list-panes -a -F '#{session_name} #{pane_title}' 2>/dev/null | awk '$1 != "_os_stash" && $2 == "opensessions" { found = 1; exit } END { exit(found ? 0 : 1) }'
}

WINDOW_ID="$(tmux display-message -p '#{window_id}' 2>/dev/null)"
[ -n "$WINDOW_ID" ] || exit 0

PANE_ID="$(find_sidebar_pane "$WINDOW_ID")"
if [ -n "$PANE_ID" ]; then
  tmux select-pane -t "$PANE_ID" >/dev/null 2>&1
  tmux switch-client -T root >/dev/null 2>&1
  exit 0
fi

ensure_server || exit 0

CTX="$(tmux display-message -p '#{client_tty}|#{session_name}|#{window_id}' 2>/dev/null)"

if sidebar_visible; then
  curl -s -o /dev/null -X POST "http://${HOST}:${PORT}/ensure-sidebar" -d "$CTX"
else
  curl -s -o /dev/null -X POST "http://${HOST}:${PORT}/toggle" -d "$CTX"
fi

attempt=0
while [ "$attempt" -lt 20 ]; do
  PANE_ID="$(find_sidebar_pane "$WINDOW_ID")"
  if [ -n "$PANE_ID" ]; then
    tmux select-pane -t "$PANE_ID" >/dev/null 2>&1
    tmux switch-client -T root >/dev/null 2>&1
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep 0.05
done

tmux switch-client -T root >/dev/null 2>&1
