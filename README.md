# opensessions

opensessions is a terminal sidebar for tmux that keeps session switching, agent activity, and repo context in one place.

It runs inside your existing multiplexer instead of replacing it. The current build focuses on a Bun-powered local server, an OpenTUI sidebar, built-in agent watchers, and a capability-based mux abstraction that can be extended with plugins.

tmux is the only supported mux today. There is older zellij integration code in the repo, but it is not stable enough to document as supported; we are looking for maintainers who want to help bring it back to that bar.

## What You Get

- Live agent state across sessions for Amp, Claude Code, Codex, and OpenCode.
- Per-thread unseen markers for terminal agent states such as `done`, `error`, and `interrupted`.
- Session metadata at a glance: branch name, dirty state, worktree detection, pane count, window count, uptime, and detected localhost ports.
- Fast navigation with arrow keys, `j`/`k`, number keys, `Tab`, session reordering, session creation, session killing, a default tmux command table on `prefix o`, and optional no-prefix tmux shortcuts when you want them.
- Theme switching from inside the sidebar, with built-in theme presets persisted to config.
- A plugin model for additional mux providers and agent watchers.
- A capability-based mux layer that can support additional providers over time.

## Supported Today

### Multiplexers

- `tmux` via `@opensessions/mux-tmux`

### Built-in Agent Watchers

- Amp
- Claude Code
- Codex
- OpenCode

### Runtime

- Bun workspace
- Source-first execution (`bun run src/index.tsx`, `bun run .../start.ts`)

## Quick Start

For a fast smoke test inside an existing tmux session:

```bash
git clone https://github.com/Ataraxy-Labs/opensessions.git
cd opensessions
bun install
bun test
cd packages/tui && bun run start
```

That starts the sidebar client and auto-launches the local server if needed. For a real sidebar workflow with keybindings and automatic pane management, use one of the setup guides below.

## Documentation Map

- [Get started in tmux](./docs/tutorials/get-started-in-tmux.md)
- [Configuration reference](./docs/reference/configuration.md)
- [Features and keybindings reference](./docs/reference/features-and-keybindings.md)
- [Architecture explanation](./docs/explanation/architecture.md)
- [Contracts and extension interfaces](./CONTRACTS.md)
- [Plugin authoring guide](./PLUGINS.md)

## Feature Highlights

### Session Awareness

- Merges sessions from every registered mux provider into one server state.
- Persists custom ordering in `~/.config/opensessions/session-order.json`.
- Tracks the currently focused session separately from the session currently attached to the client.

### Agent Tracking

- Amp watcher reads `~/.local/share/amp/threads/*.json` and clears unseen state from Amp's `session.json` when a thread becomes seen there.
- Claude Code watcher tails appended JSONL content in `~/.claude/projects/`.
- Codex watcher reads transcript JSONL files in `~/.codex/sessions/` (or `$CODEX_HOME/sessions/`) and resolves sessions from `turn_context.cwd`.
- OpenCode watcher polls the SQLite database in `~/.local/share/opencode/opencode.db`.
- The tracker keeps multiple agent instances per terminal session using `threadId` when present.

### Repo Context

- Reads branch name and dirty state from the session working directory.
- Detects Git worktrees.
- Caches Git info for 5 seconds and watches Git `HEAD` files to invalidate cache quickly.
- Scans descendant processes for listening localhost ports and surfaces them in the detail panel.

### Sidebar UX

- Theme picker inside the TUI.
- Session detail panel with agent rows and thread names.
- Mouse support for selecting sessions and opening detected `localhost` ports.
- tmux sidebar stash session (`_os_stash`) so sidebars can be hidden without losing their process.

## Repository Layout

| Path | Purpose |
| --- | --- |
| `packages/core` | Server, shared contracts, config loader, theme registry, agent watchers, session ordering, plugin loader |
| `packages/tui` | OpenTUI sidebar client built with Solid |
| `packages/mux` | Capability-based mux type definitions and type guards |
| `packages/mux-tmux` | tmux provider and tmux client wrapper |
| `packages/tmux-sdk` | Lower-level typed tmux command bindings |
| `tmux-plugin` | tmux-facing scripts and plugin package entrypoint |

## Architecture In One Screen

```text
agent data files / databases
        |
        v
built-in watchers + plugin watchers
        |
        v
AgentTracker + mux providers + git/port/session state
        |
        v
Bun WebSocket server on 127.0.0.1:7391
        |
        v
OpenTUI sidebar clients running inside tmux panes
```

More detail: [docs/explanation/architecture.md](./docs/explanation/architecture.md)

## Current Caveats

- The running app is effectively pinned to `127.0.0.1:7391` today. Helper scripts read `OPENSESSIONS_HOST` and `OPENSESSIONS_PORT`, but the server and TUI still use the fixed constants in `packages/core/src/shared.ts`.
- `~/.config/opensessions/config.json` fields `mux`, `plugins`, `theme`, `sidebarWidth`, and `sidebarPosition` are used by the runtime. The parsed `port` and `keybinding` fields are not currently wired through the running app.
- The core theme utilities support inline partial theme objects, but the server currently persists and broadcasts theme names rather than inline theme objects.
- Experimental mux providers may still exist in the repo, but they are outside the current support promise unless documented otherwise.

## License

MIT
