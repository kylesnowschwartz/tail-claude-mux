# Features And Keybindings Reference

This page lists the user-visible features currently implemented in the sidebar.

## Session List

Each session row can show:

- Session index for number-key switching
- Session name
- Branch name, truncated when needed
- Running spinner or terminal-state marker
- Focused state highlight
- Current client session emphasis
- Unseen accent for `done`, `error`, or `interrupted` states

The list has two layouts. `v` toggles between them, and the choice is saved as `displayMode` in `config.json` and applies to every sidebar.

| Layout | Session rows | Agent rows |
| --- | --- | --- |
| Cards (default) | A bordered card: name, live-agent count, branch row | One row each inside the card |
| Compact list | One row: name, a rule to the right edge, live-agent count | One row each: window index, agent glyph, conversation name, status icon |

- The compact list has no border and no branch row. Overlong names clip at the right edge.
- The focused session's rule in the compact list is drawn in the focus colour.
- The compact list is centred vertically in the space below the header and the blocks above it. Session cards sit at the bottom of the column.

## Needs You Block

A block headed `NEEDS YOU (n)` lists every live conversation with an attention reason, oldest first; a conversation that has ended or whose process is gone is left out. With no rows the block is not drawn. Each row shows: reason glyph, conversation name, reason, and age. Reasons are `permission`, `question`, `input`, `error`, and `finished`; `permission` and `question` show the name in bold. Sidebars narrower than 25 columns leave out the reason word. No timer removes a row. When the rows need more than half of the free height, the block shows `▲ N more` / `▼ N more` rows; click one to scroll the block. Click a row to jump to that conversation.

## Detail Panel

The focused session detail panel can show:

- Truncated working directory
- Agent instances in that session
- Thread names when watchers provide them

## Agent Features

- Multiple agent instances per session when a watcher emits `threadId`
- Per-instance unseen tracking
- Amp thread seen-state integration via Amp's `session.json`
- Status values: `idle`, `running`, `done`, `error`, `waiting`, `interrupted`
- Automatic pruning of stale running and seen terminal states

## Session Metadata Features

- Git branch lookup from the session directory
- Dirty-worktree detection
- Git worktree detection
- Pane count and window count per session
- Session uptime display
- Session ordering persisted across restarts

## Sidebar Management Features

### tmux-specific

- Global hooks for session changes, window selection, window creation, resize, and refresh
- Sidebar stash session named `_tcm_stash` so hidden sidebars can be restored
- Session creation popup using the bundled `sessionizer.sh` script

## Keyboard Shortcuts

### Inside the sidebar

| Key | Action |
| --- | --- |
| `j`, `Down` | Move focus down |
| `k`, `Up` | Move focus up |
| `Enter` | Switch to focused session |
| `Tab` | Cycle to next session |
| `Shift+Tab` | Cycle to previous session |
| `1`-`9` | Switch to session by visible index |
| `a` | Jump to the conversation that has waited longest among the "needs you" rows: switch to its session and focus its pane; does nothing when none waits |
| `Alt+Up` | Move focused session up in persisted order |
| `Alt+Down` | Move focused session down in persisted order |
| `n`, `c` | Create a new session |
| `d`, `x` | Open kill-session confirmation for focused session |
| `t` | Open theme picker |
| `v` | Toggle between session cards and the compact list |
| `r` | Refresh state |
| `q` | Quit the server and all sidebar panes |
| `Esc` | Close only the current sidebar client |

### tmux plugin shortcuts

| Key | Action |
| --- | --- |
| `prefix o → s` | Reveal and focus the sidebar pane |
| `prefix o → t` | Toggle the sidebar |
| `prefix o → 1` through `prefix o → 9` | Switch directly to the visible session indices |
| Configurable `@tcm-focus-global-key` such as `Alt-s` | Reveal and focus the sidebar pane from any tmux pane |
| Configurable `@tcm-index-keys` such as `Alt-1` through `Alt-9` | Switch directly to the visible session indices from any tmux pane |

## Session Creation Behavior

- The server exposes `new-session` through the mux provider interface.
- The TUI uses a tmux popup sessionizer when it is running inside tmux.
- The bundled sessionizer searches directories listed in `SESSIONIZER_DIR` (colon-separated, e.g. `$HOME/Code:$HOME/.config`) or `$HOME/Documents` if unset. The variable is also read from the tmux global environment (`tmux set-environment -g`) as a fallback.
- The list holds each listed directory, each Git repository below it, and each directory that is not inside a repository, to a depth of three. Directories inside a repository and hidden directories are left out.
- If `fzf` is unavailable, the tmux sessionizer exits with a prompt explaining that dependency.

## Session Switching Behavior

- Switching routes through the server so the server can use authoritative client TTY information.
- tmux is the only supported mux today.

## Refresh And Discovery Behavior

- Git info is cached for 5 seconds.
- Amp and Claude Code watchers poll every 2 seconds in addition to file watching.
- OpenCode polls every 3 seconds.

## Files And Paths The UI Depends On

- `~/.local/share/amp/threads/`
- `~/.local/share/amp/session.json`
- `~/.claude/projects/`
- `~/.local/share/opencode/opencode.db`
- `~/.config/tcm/config.json`

## Related Docs

- [Configuration reference](./configuration.md)
- [Contracts](../../CONTRACTS.md)
