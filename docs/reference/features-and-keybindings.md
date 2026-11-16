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

## Liaison Block

While a Claude Code conversation named exactly `liaison`, or starting with `liaison/`, is open, a block is pinned at the top of the sidebar, one per liaison (`liaison/work` and `liaison/personal` give two). With no liaison open, the block is absent. The block stays grey and still while all is normal, and takes colour only for an item that needs the engineer.

- Title row: the liaison glyph and `LIAISON` plus the rest of the name, both dim. While conversations that this liaison started are open, `↳N` at the right gives their count. With items present, the title takes the colour of the highest priority among them.
- Item rows: one row per item, at most three: a glyph in a fixed column, the item's title cut to the width, and its age at the right, such as `12m`. When more items exist, the last row ends with `+K`, the count of items not shown. Items run by priority, then oldest first.

| Priority | Glyph | Colour | Kinds |
|----------|-------|--------|-------|
| 1 | `■` | red | `asks`, `blocked` |
| 2 | `▲` | yellow | `no_report`, `answer` |
| 3 | `●` | blue | `reminder` |

The `asks` item comes from the liaison's own conversation: while it waits on a permission or a question, the block shows one `asks` item titled `permission` or `question`, aged from when that reason started. Every other item comes from `POST /set-liaison-items`; see [programmatic-api.md](./programmatic-api.md).

A conversation that an open liaison started shows a blue arrow mark before its name in its session card. The row stays in its place in the card. The mark and the count come from the `liaisonParentThreadId` field in `GET /state`, an unverified claim for display; see [programmatic-api.md](./programmatic-api.md). When the liaison reports that conversation as finished and still open, a grey check mark follows the arrow.

## Needs You Block

Under the liaison block, and also with no liaison open, a block headed `NEEDS YOU (n)` lists every live conversation with an attention reason, oldest first; a conversation that has ended or whose process is gone is left out, as it is from the liaison block: reason glyph, conversation name, reason, and age. Reasons are `permission`, `question`, `input`, `error`, and `finished`; `permission` and `question` show the name in bold. Sidebars narrower than 25 columns leave out the reason word. No timer removes a row. When the rows need more than half of the free height, the block shows `▲ N more` / `▼ N more` rows; click one to scroll the block. Click a row to jump to that conversation.

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
| `a` | Jump to the oldest conversation in the "needs you" block: switch to its session and focus its pane; does nothing when the block is empty |
| `Alt+Up` | Move focused session up in persisted order |
| `Alt+Down` | Move focused session down in persisted order |
| `n`, `c` | Create a new session |
| `d`, `x` | Open kill-session confirmation for focused session |
| `t` | Open theme picker |
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
