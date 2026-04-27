# How opensessions Works

opensessions is a local coordination layer between your multiplexer, your agent tools, and a terminal sidebar UI.

It is easiest to think about it as four pieces:

1. mux providers that know how to inspect and control the active multiplexer
2. agent watchers that translate external agent data into `AgentEvent`s
3. a Bun server that merges state and broadcasts it
4. an OpenTUI client that renders the sidebar and sends user commands back

## Startup Flow

When the TUI starts, it first calls `ensureServer()` from `@opensessions/runtime`.

If no healthy server is listening on `127.0.0.1:7391`, `ensureServer()` launches `apps/server/src/main.ts` in the background. The server then:

1. loads config from `~/.config/tcm/config.json`
2. dynamically registers the built-in mux providers from `@opensessions/mux-tmux` and `@opensessions/mux-zellij`
3. loads local plugins and configured package plugins
4. resolves the primary mux provider
5. registers the built-in Amp, Claude Code, Codex, and OpenCode watchers
6. starts the WebSocket and HTTP control server

## State Assembly

The server computes a single `ServerState` payload for every connected sidebar client.

That state is assembled from several sources:

- session lists from every registered mux provider
- custom session order stored on disk
- Git branch and dirty information from each session directory
- pane counts and window counts from providers
- tracked agent instances and unseen state from `AgentTracker`

The result is one merged view even when multiple mux providers are active.

## Agent Tracking Model

Watchers do not know about the TUI. They only know how to emit `AgentEvent`s through the watcher context.

The `AgentTracker` is where those raw events become UI-friendly state:

- it keeps instances separate with `threadId` when available
- it derives the most important session-level state from all instances
- it tracks unseen status per instance for terminal states
- it prunes stale or no-longer-relevant state over time

This separation is why the built-in watchers can be simple and agent-specific while the unseen logic stays consistent across agents.

## Why The Mux Interface Is Capability-Based

The provider model is split into required core operations and optional capabilities instead of one large interface.

That matters because different multiplexers do not expose the same control surface:

- session listing and switching are common needs
- session creation, window awareness, and sidebar management vary by provider
- tmux has hook support and more direct client targeting
- other providers may need to lean more on CLI actions or polling

The capability model lets the server ask for only what a feature needs. For example, sidebar spawning requires both window awareness and sidebar management, so the server narrows providers with `isFullSidebarCapable()`.

## tmux Design

The tmux provider is the more feature-complete reference implementation.

Notable design choices:

- tmux global hooks notify the server about focus changes, session creation, window changes, and resize events
- hidden sidebars are moved into a dedicated stash session named `_os_stash` instead of being destroyed
- the TUI refocuses the main pane after capability detection to avoid escape-sequence leakage into the main pane
- a small typed tmux SDK exists under `packages/mux/tmux-sdk` for lower-level command work
- the tmux integration scripts live under `integrations/tmux-plugin`, while the sidebar launcher itself lives with the TUI app in `apps/tui/scripts/start.sh`

## Experimental Providers

The mux contract is intentionally extensible, and the repository still contains older experimental provider code beyond tmux.

That code is not part of the current support promise. In particular, the zellij path is not stable enough to recommend today, and we are looking for maintainers who want to help bring it back to a supported state.

## Why The Server Owns Session Switching

The TUI does not switch sessions directly. It always sends a command to the server.

That centralization matters for three reasons:

1. the server knows which provider owns each session
2. the server can use authoritative client TTY information gathered from hooks or identify messages
3. provider-specific switching logic belongs in one place rather than being duplicated in every client

## Files The Runtime Writes

The runtime keeps a small set of operational files:

- `/tmp/tcm.pid` for server bootstrap health checks
- `/tmp/tcm-debug.log` for best-effort debug logging
- `~/.config/tcm/session-order.json` for user-controlled session ordering
- `~/.config/tcm/config.json` for user configuration

## Current Constraints

Some pieces are intentionally still narrow in scope:

- the server and TUI are effectively pinned to `127.0.0.1:7391`
- parsed config fields `port` and `keybinding` are not yet wired through the runtime
- inline theme objects exist in the core API surface, but the running server currently uses theme names
- tmux is the only supported mux today

## Why The Codebase Is Split This Way

The repository now follows a clearer monorepo boundary model:

- `apps/*` contains runnable entrypoints such as the server bootstrap and the TUI
- `packages/runtime` contains reusable runtime logic that both apps depend on
- `packages/mux/*` groups the mux contract, concrete mux providers, and the lower-level tmux SDK in one place
- `integrations/tmux-plugin` contains host-specific tmux glue instead of runtime library code

That keeps entrypoints, reusable libraries, mux adapters, and host integrations separate enough that new contributors can tell what owns what at a glance.
