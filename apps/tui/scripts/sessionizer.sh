#!/usr/bin/env bash
# opensessions sessionizer — fuzzy directory picker for new tmux sessions
# Requires: fzf, find
# Supports colon-separated paths in SESSIONIZER_DIR (e.g. "$HOME/Code:$HOME/.config")

# Check env first, then tmux global environment, then default
if [ -z "$SESSIONIZER_DIR" ] && command -v tmux &>/dev/null; then
  SESSIONIZER_DIR=$(tmux show-environment -g SESSIONIZER_DIR 2>/dev/null | sed 's/^SESSIONIZER_DIR=//')
fi
SEARCH_DIRS="${SESSIONIZER_DIR:-$HOME/Documents}"

# Resolve fzf colors: opensessions theme > the-themer > shell defaults
if [ -n "$TCM_FZF_COLORS" ]; then
  export FZF_DEFAULT_OPTS="$TCM_FZF_COLORS"
else
  THEMER_FZF="${HOME}/.config/the-themer/fzf/current.zsh"
  # shellcheck source=/dev/null
  [ -f "$THEMER_FZF" ] && source "$THEMER_FZF"
fi

if ! command -v fzf &>/dev/null; then
  echo "fzf is required for the sessionizer. Install it: https://github.com/junegunn/fzf"
  exit 1
fi

# Split colon-separated paths and validate each one
IFS=: read -ra dirs <<<"$SEARCH_DIRS"
valid_dirs=()
for dir in "${dirs[@]}"; do
  [ -d "$dir" ] && valid_dirs+=("$dir")
done

if [ ${#valid_dirs[@]} -eq 0 ]; then
  echo "No valid directories found in: $SEARCH_DIRS"
  exit 1
fi

selected=$(find "${valid_dirs[@]}" -mindepth 1 -maxdepth 3 -type d 2>/dev/null | fzf \
  --reverse \
  --header="Pick a directory for new session" \
  --preview=':' \
  --preview-window=hidden \
  --bind='ctrl-c:abort')

[ -z "$selected" ] && exit 0

# Derive session name from directory basename, replacing dots with underscores
session_name=$(basename "$selected" | tr '.' '_')

# If session already exists, just switch to it
if tmux has-session -t "=$session_name" 2>/dev/null; then
  tmux switch-client -t "$session_name"
  exit 0
fi

tmux new-session -d -s "$session_name" -c "$selected"
tmux switch-client -t "$session_name"
