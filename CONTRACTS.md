# Contracts And Extension Interfaces

This document is the reference for extending opensessions. It describes the agent event model, watcher interfaces, mux provider capabilities, and the runtime behaviors extension authors need to match.

For end-user setup, start with the docs linked from [README.md](./README.md). For plugin packaging workflow, see [PLUGINS.md](./PLUGINS.md).

## Built-In Watchers

opensessions currently registers four built-in watchers at server startup.

### Amp

- Watches `~/.local/share/amp/threads/T-*.json`.
- Also watches `~/.local/share/amp/session.json` to clear unseen state when a terminal Amp thread becomes seen there.
- Uses `fs.watch` plus a 2 second polling pass.
- Skips stale thread files older than 5 minutes.
- Resolves the project directory from `env.initial.trees[0].uri`.

### Claude Code

- Watches `~/.claude/projects/<encoded-path>/*.jsonl`.
- Uses `fs.watch` plus a 2 second polling pass.
- Reads only appended bytes after the last observed file size.
- Decodes project directories from folder names such as `-Users-me-project`.

### Codex

- Watches `~/.codex/sessions/**/*.jsonl` or `$CODEX_HOME/sessions/**/*.jsonl`.
- Reads `$CODEX_HOME/session_index.jsonl` for recent thread titles when available.
- Uses recursive `fs.watch` plus a 2 second polling pass.
- Skips stale transcript files older than 5 minutes.
- Resolves mux sessions from `turn_context.cwd` inside the transcript.
- Treats `user_message`, tool activity, and assistant `commentary` as `running`, assistant `final_answer` and `task_complete` as `done`, and `turn_aborted` as `interrupted`.

### OpenCode

- Polls `~/.local/share/opencode/opencode.db` or `$OPENCODE_DB_PATH`.
- Uses `bun:sqlite` in read-only mode.
- Polls every 3 seconds.
- Resolves mux sessions from the OpenCode session row's `directory` field.

## Agent Model

### `AgentStatus`

```ts
type AgentStatus =
  | "idle"
  | "running"
  | "done"
  | "error"
  | "waiting"
  | "interrupted";
```

Terminal states are `done`, `error`, and `interrupted`. The tracker uses those states to decide unseen behavior.

### `AgentEvent`

```ts
interface AgentEvent {
  agent: string;
  session: string;
  status: AgentStatus;
  ts: number;
  threadId?: string;
  threadName?: string;
  unseen?: boolean;
}
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `agent` | `string` | yes | Stable watcher identifier such as `amp`, `claude-code`, `codex`, or `opencode` |
| `session` | `string` | yes | Resolved mux session name |
| `status` | `AgentStatus` | yes | Current agent state |
| `ts` | `number` | yes | Millisecond timestamp |
| `threadId` | `string` | no | Instance key used to track multiple threads in one session |
| `threadName` | `string` | no | Human-readable label shown in the detail panel |
| `unseen` | `boolean` | no | Added by the tracker when serializing to the TUI |

### Tracker Semantics

- The tracker keys instances by `agent:threadId` when `threadId` exists, otherwise by `agent`.
- A session can have multiple active agent instances.
- Unseen state is tracked per instance, then derived to the session level.
- Non-terminal updates clear unseen state for that instance.
- Stale `running` events are pruned after 3 minutes.
- Seen terminal instances are pruned after 5 minutes.

## `AgentWatcher`

```ts
interface AgentWatcher {
  readonly name: string;
  start(ctx: AgentWatcherContext): void;
  stop(): void;
}
```

### `AgentWatcherContext`

```ts
interface AgentWatcherContext {
  resolveSession(projectDir: string): string | null;
  emit(event: AgentEvent): void;
}
```

`resolveSession(projectDir)` first checks for an exact directory match across registered mux sessions. If there is no exact match, the server falls back to parent-child prefix matching so nested project paths can still resolve.

### Minimal Watcher Example

```ts
import type { AgentWatcher, AgentWatcherContext } from "@opensessions/core";

export class MyAgentWatcher implements AgentWatcher {
  readonly name = "my-agent";

  start(ctx: AgentWatcherContext): void {
    const projectDir = "/path/to/project";
    const session = ctx.resolveSession(projectDir);
    if (!session) return;

    ctx.emit({
      agent: this.name,
      session,
      status: "running",
      ts: Date.now(),
      threadId: "thread-1",
      threadName: "Example task",
    });
  }

  stop(): void {
  }
}
```

## Mux Contracts

opensessions uses the capability model exported from `@opensessions/mux`. A provider must implement the required `MuxProviderV1` contract and may opt into extra capabilities.

### Core Types

```ts
interface MuxSessionInfo {
  readonly name: string;
  readonly createdAt: number;
  readonly dir: string;
  readonly windows: number;
}

interface ActiveWindow {
  readonly id: string;
  readonly sessionName: string;
  readonly active: boolean;
}

interface SidebarPane {
  readonly paneId: string;
  readonly sessionName: string;
  readonly windowId: string;
}

type SidebarPosition = "left" | "right";
```

### Required Provider Interface

```ts
interface MuxProviderV1 {
  readonly specificationVersion: "v1";
  readonly name: string;

  listSessions(): MuxSessionInfo[];
  switchSession(name: string, clientTty?: string): void;
  getCurrentSession(): string | null;
  getSessionDir(name: string): string;
  getPaneCount(name: string): number;
  getClientTty(): string;
  createSession(name?: string, dir?: string): void;
  killSession(name: string): void;
  setupHooks(serverHost: string, serverPort: number): void;
  cleanupHooks(): void;
}
```

### Optional Capabilities

```ts
interface WindowCapable {
  listActiveWindows(): ActiveWindow[];
  getCurrentWindowId(): string | null;
}

interface SidebarCapable {
  listSidebarPanes(sessionName?: string): SidebarPane[];
  spawnSidebar(
    sessionName: string,
    windowId: string,
    width: number,
    position: SidebarPosition,
    scriptsDir: string,
  ): string | null;
  hideSidebar(paneId: string): void;
  killSidebarPane(paneId: string): void;
  resizeSidebarPane(paneId: string, width: number): void;
  cleanupSidebar(): void;
}

interface BatchCapable {
  getAllPaneCounts(): Map<string, number>;
}
```

The server narrows providers with the runtime type guards exported from `@opensessions/mux`:

- `isWindowCapable()`
- `isSidebarCapable()`
- `isBatchCapable()`
- `isFullSidebarCapable()`

### Provider Expectations

- `listSessions()` should return enough information for the server to sort and render sessions.
- `getCurrentSession()` should reflect the session attached to the current client when possible.
- `setupHooks()` should install mux-native hooks if the mux supports them. If it does not, a no-op implementation is acceptable.
- `createSession()` and `killSession()` power the TUI's new-session and kill-session flows.

## `PluginAPI`

Plugins are loaded as default-exported factory functions that receive this API:

```ts
interface PluginAPI {
  registerMux(provider: MuxProvider): void;
  registerWatcher(watcher: AgentWatcher): void;
  readonly serverPort: number;
  readonly serverHost: string;
}
```

The current runtime passes `127.0.0.1:7391` here.

## Built-In Runtime Behaviors To Know About

- The server merges sessions from all registered providers into one state payload.
- Session ordering is persisted separately from mux ordering.
- tmux sidebars can be hidden into a stash session instead of being killed.
- tmux is the only supported built-in mux today. Other providers can still target these contracts, but they are currently outside the support bar unless documented otherwise.
- The TUI expects a WebSocket server on `127.0.0.1:7391`.

## Where To Start

- Build a custom watcher: see the `AgentWatcher` section above.
- Build a plugin package or local plugin: see [PLUGINS.md](./PLUGINS.md).
- Understand the end-to-end runtime: see [docs/explanation/architecture.md](./docs/explanation/architecture.md).
