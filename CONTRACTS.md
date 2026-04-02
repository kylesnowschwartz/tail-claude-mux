# Contracts And Extension Interfaces

This document is the reference for extending opensessions. It describes the agent event model, watcher interfaces, mux provider capabilities, and the runtime behaviors extension authors need to match.

For end-user setup, start with the docs linked from [README.md](./README.md). For plugin packaging workflow, see [PLUGINS.md](./PLUGINS.md).

## Built-In Watchers

opensessions registers one built-in watcher at server startup.

### Claude Code (Hook-Based)

- Receives lifecycle hooks (`UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PostToolUse`, `Stop`, `Notification`) via `POST /hook`.
- Claude Code pushes events through `scripts/hook.sh`, registered in `~/.claude/settings.json`.
- Maps hooks to status: `UserPromptSubmit`/`PreToolUse`/`PostToolUse` → `running`, `PermissionRequest` → `waiting`, `Stop` → `done`.
- `Notification` branches on `notification_type`: `permission_prompt`/`idle_prompt` → `waiting`, others ignored.
- On cold start, performs a one-time seed scan of `~/.claude/projects/` JSONL files to bootstrap state for sessions already running.
- On first hook for an unknown session, reads the JSONL file once to resolve the thread name.
- No polling, no `fs.watch`, no intervals after startup.

#### Hook Setup

Run `bun run scripts/setup-hooks.ts` to register hooks in `~/.claude/settings.json`. The setup is idempotent.

### Adding Other Agents

Other agents (Amp, Codex, OpenCode, etc.) can be added via the `AgentWatcher` plugin interface. See the plugin API below.

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
import type { AgentWatcher, AgentWatcherContext } from "@opensessions/runtime";

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
- The server exposes HTTP POST endpoints for programmatic metadata (status, progress, logs, notifications). See [docs/reference/programmatic-api.md](./docs/reference/programmatic-api.md).

## Where To Start

- Build a custom watcher: see the `AgentWatcher` section above.
- Push metadata from scripts: see [docs/reference/programmatic-api.md](./docs/reference/programmatic-api.md).
- Build a plugin package or local plugin: see [PLUGINS.md](./PLUGINS.md).
- Understand the end-to-end runtime: see [docs/explanation/architecture.md](./docs/explanation/architecture.md).
