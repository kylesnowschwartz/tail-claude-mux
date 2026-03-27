# Get Started In tmux

This tutorial gets opensessions running as a real tmux sidebar from a local clone. By the end, you will be able to press `prefix s` to open the sidebar, jump into it with `prefix S`, switch directly with `prefix o 1` through `prefix o 9`, and see agent and Git state update live.

## Prerequisites

- Bun installed and available on `PATH`
- tmux installed
- A local checkout of this repository

## 1. Install workspace dependencies

From the repository root:

```bash
bun install
```

Result: the workspace packages are installed and the TUI can run.

## 2. Add opensessions to your tmux config

Open `~/.tmux.conf` and add these lines, replacing the path with your clone location:

```tmux
set -g @opensessions-key "s"
set -g @opensessions-focus-key "S"
set -g @opensessions-prefix-key "o"
set -g @opensessions-prefix-focus-key "s"
set -g @opensessions-prefix-toggle-key "t"
set -g @opensessions-prefix-index-keys "1 2 3 4 5 6 7 8 9"
set -g @opensessions-width "26"
source-file /absolute/path/to/opensessions/opensessions.tmux
```

Result: tmux knows how to toggle the sidebar, how to reveal and focus it directly, and how to enter an opensessions command table for quick direct switching.

## 3. Reload tmux configuration

Run:

```bash
tmux source-file ~/.tmux.conf
```

Result: the new keybinding is active in your current tmux server.

Recommended shortcut scheme:

- `prefix s` toggles the sidebar.
- `prefix S` reveals and focuses the sidebar pane.
- `prefix o s` reveals and focuses the sidebar pane.
- `prefix o t` toggles the sidebar.
- `prefix o 1` through `prefix o 9` switch directly to the visible session indices.

If you use a terminal or window manager setup where no-prefix bindings are safe, you can also set `@opensessions-focus-global-key` and `@opensessions-index-keys`, but they are left unset by default to avoid conflicts.

## 4. Open the sidebar

Inside tmux, press:

```text
prefix s
```

Result: tmux asks the opensessions server to toggle the sidebar. If the server is not running yet, the helper script starts it first.

## 5. Verify the sidebar is live

Use the sidebar to:

1. Move selection with `j` and `k` or the arrow keys.
2. Press `Enter` to switch sessions.
3. Press `n` or `c` to create a new tmux session.
4. Press `t` to open the theme picker.

Result: you should see the session list update and tmux switch the attached client to the selected session.

## 6. Verify agent detection

In any tmux session whose working directory matches a repo you use with a supported agent, start one of these tools:

- Amp
- Claude Code
- OpenCode

Result: the session row should show a live status marker, and the detail panel should show thread-level information when available.

## Expected Outcome

You now have opensessions wired into tmux as a toggleable sidebar. From here you can move on to:

- [Configuration reference](../reference/configuration.md)
- [Features and keybindings reference](../reference/features-and-keybindings.md)
- [Architecture explanation](../explanation/architecture.md)
