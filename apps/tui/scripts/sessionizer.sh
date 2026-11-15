#!/usr/bin/env bash
# tcm sessionizer — fuzzy directory picker for new tmux sessions
# Requires: fzf, find
# Supports colon-separated paths in SESSIONIZER_DIR (e.g. "$HOME/Code:$HOME/.config")

# Check env first, then tmux global environment, then default
if [ -z "$SESSIONIZER_DIR" ] && command -v tmux &>/dev/null; then
  SESSIONIZER_DIR=$(tmux show-environment -g SESSIONIZER_DIR 2>/dev/null | sed 's/^SESSIONIZER_DIR=//')
fi
SEARCH_DIRS="${SESSIONIZER_DIR:-$HOME/Documents}"

# Resolve fzf colors: tcm theme > the-themer > shell defaults
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

# Print each root, then its subdirectories and the .git entries among them.
# Walk each root to completion before the next so SESSIONIZER_DIR order is the
# list order; a single multi-root find interleaves depth levels across roots.
walk_roots() {
  local dir
  for dir in "${valid_dirs[@]}"; do
    printf '%s\n' "${dir%/}"
    # Print .git entries so project_roots can tell which directories are
    # repositories. Prune other hidden dirs (.bin, ...) and their subtrees;
    # mindepth keeps the prune off the root, so a hidden root like ~/.config
    # still descends.
    find "${dir%/}" -mindepth 1 -maxdepth 3 \
      -name .git -print -prune -o \
      -name '.*' -prune -o \
      -type d -print 2>/dev/null
  done
}

# Keep the places a session starts from: each root, each repository, and each
# directory that is not inside a repository. Drop the directories inside a
# repository and the .git entries themselves.
project_roots() {
  awk '
    { paths[NR] = $0 }
    /\/\.git$/ { is_repo[substr($0, 1, length($0) - 5)] = 1 }
    END {
      for (i = 1; i <= NR; i++) {
        path = paths[i]
        if (path ~ /\/\.git$/) continue
        ancestor = path
        inside_repo = 0
        while (sub(/\/[^\/]*$/, "", ancestor) && ancestor != "") {
          if (ancestor in is_repo) { inside_repo = 1; break }
        }
        if (!inside_repo) print path
      }
    }'
}

list_dirs() {
  walk_roots | project_roots
}

selected=$(list_dirs | fzf \
  --reverse \
  --scheme=path \
  --tiebreak=index \
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
