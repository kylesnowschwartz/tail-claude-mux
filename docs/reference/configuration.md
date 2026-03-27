# Configuration Reference

This page documents the configuration inputs that opensessions reads today.

## Config File Location

User config is loaded from:

```text
~/.config/opensessions/config.json
```

If the file does not exist, opensessions falls back to defaults.

## Recommended Config Shape

```json
{
  "mux": "tmux",
  "plugins": [],
  "theme": "tokyo-night",
  "sidebarWidth": 30,
  "sidebarPosition": "right"
}
```

## Config Fields

| Field | Type | Default | Runtime status | Description |
| --- | --- | --- | --- | --- |
| `mux` | `string` | auto-detect | active | Selects the preferred registered mux provider by name |
| `plugins` | `string[]` | `[]` | active | Package names to load through the plugin loader |
| `theme` | `string` | `catppuccin-mocha` | active | Built-in theme name persisted by the TUI |
| `sidebarWidth` | `number` | `26` | active | Sidebar width in columns |
| `sidebarPosition` | `"left" | "right"` | `"left"` | active | Sidebar placement |
| `port` | `number` | none | parsed only | Present in the config type, but the current server and TUI still use the fixed `7391` constant |
| `keybinding` | `string` | none | parsed only | Present in the config type, but keybindings are configured outside this file today |

## Built-In Themes

These theme names resolve in the running app today:

- `catppuccin-mocha`
- `catppuccin-latte`
- `catppuccin-frappe`
- `catppuccin-macchiato`
- `tokyo-night`
- `gruvbox-dark`
- `nord`
- `dracula`
- `github-dark`
- `one-dark`
- `kanagawa`
- `everforest`
- `material`
- `cobalt2`
- `flexoki`
- `ayu`
- `aura`
- `matrix`

## Inline Theme Objects

The core config type and theme resolver also support partial inline theme objects such as:

```json
{
  "theme": {
    "palette": {
      "base": "#000000",
      "text": "#ffffff"
    }
  }
}
```

That shape is valid for the core APIs, but the current server startup path only applies string theme names end-to-end.

## tmux Plugin Options

The tmux integration reads these tmux options instead of `config.json`:

| tmux option | Default | Used by |
| --- | --- | --- |
| `@opensessions-key` | `s` | tmux keybinding in `opensessions.tmux` |
| `@opensessions-focus-key` | `S` | Prefix keybinding that reveals and focuses the sidebar pane |
| `@opensessions-prefix-key` | `o` | Prefix key that opens the `opensessions` command table for the next keypress |
| `@opensessions-prefix-focus-key` | `s` | Command-table key that reveals and focuses the sidebar pane |
| `@opensessions-prefix-toggle-key` | `t` | Command-table key that toggles the sidebar |
| `@opensessions-prefix-index-keys` | `1 2 3 4 5 6 7 8 9` | Space-separated command-table keys mapped in order to visible sessions `1` through `9` |
| `@opensessions-focus-global-key` | unset | Optional no-prefix tmux keybinding that reveals and focuses the sidebar pane |
| `@opensessions-index-keys` | unset | Optional space-separated no-prefix tmux keys mapped in order to visible sessions `1` through `9` |
| `@opensessions-width` | `26` | exported as `OPENSESSIONS_WIDTH` by the tmux bootstrap script |

Example:

```tmux
set -g @opensessions-key "s"
set -g @opensessions-focus-key "S"
set -g @opensessions-prefix-key "o"
set -g @opensessions-prefix-focus-key "s"
set -g @opensessions-prefix-toggle-key "t"
set -g @opensessions-prefix-index-keys "1 2 3 4 5 6 7 8 9"
set -g @opensessions-width "30"
source-file /absolute/path/to/opensessions/opensessions.tmux
```

Recommended mapping:

- Keep `@opensessions-key` on `s` for the familiar toggle.
- Use `@opensessions-focus-key` on `S` so `prefix S` jumps straight into the sidebar.
- Keep `@opensessions-prefix-key` on `o` so `prefix o` becomes an opensessions mini-leader.
- Keep `@opensessions-prefix-focus-key` on `s` and `@opensessions-prefix-toggle-key` on `t` so the defaults read as `prefix o s` and `prefix o t`.
- Keep `@opensessions-prefix-index-keys` on `1` through `9` so `prefix o 1` through `prefix o 9` jump to the same visible indices shown in the sidebar.
- Use `@opensessions-focus-global-key` and `@opensessions-index-keys` only when you explicitly want no-prefix tmux bindings and know they do not conflict with your window manager or terminal.

If you rely on tmux's built-in `prefix o` pane-cycling command, set `@opensessions-prefix-key` to another key such as `g` or set it to an empty string to disable the opensessions command table.

## Environment Variables

| Variable | Used by | Notes |
| --- | --- | --- |
| `OPENCODE_DB_PATH` | OpenCode watcher | Overrides the default SQLite path |
| `OPENSESSIONS_DIR` | tmux helper scripts and server | Helps helper scripts find the repo checkout |
| `OPENSESSIONS_HOST` | helper shell scripts | Script-level override only; the app runtime still uses `127.0.0.1` |
| `OPENSESSIONS_PORT` | helper shell scripts | Script-level override only; the app runtime still uses `7391` |
| `SESSIONIZER_DIR` | tmux sessionizer popup | Root directory searched for new-session candidates |
| `BUN_PATH` | helper scripts | Explicit Bun binary path for helper scripts |

## Related Files Written By The Runtime

| Path | Purpose |
| --- | --- |
| `~/.config/opensessions/session-order.json` | Persisted custom session ordering |
| `/tmp/opensessions.pid` | PID file used by server bootstrap logic |
| `/tmp/opensessions-debug.log` | Best-effort debug log written by the server and providers |

## Mux Detection Rules

If `mux` is unset, the supported built-in auto-detection path is:

1. `$TMUX` -> provider named `tmux`
2. no supported match -> `null`

Experimental or plugin-provided providers may implement their own detection behavior, but tmux is the only supported built-in mux today.
