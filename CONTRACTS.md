# Contracts And Extension Interfaces

This document is the reference for extending tcm. It describes the agent event model, watcher interfaces, mux provider capabilities, and the runtime behaviors extension authors need to match.

For end-user setup, start with the docs linked from [README.md](./README.md).

## Built-In Watchers

tcm registers two built-in watchers at server startup: Claude Code
and pi. Both share the single `POST /hook` endpoint and are distinguished
by an optional `agent` field on the payload.

### Shared Hook Transport

`POST /hook` delivers a `HookPayload` to every registered `HookReceiver`.
Adapters filter on the `agent` discriminator:

- `agent` missing or `"claude-code"` → handled by `ClaudeCodeHookAdapter`.
- `agent: "pi"` → handled by `PiHookAdapter`.
- Other values are ignored by both built-in adapters.

A single hook body can in principle be observed by multiple watchers, but
built-in adapters claim payloads exclusively via the discriminator.

### Claude Code (Hook-Based)

- Receives lifecycle hooks (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PostToolUse`, `Stop`, `Notification`, `SessionEnd`) via `POST /hook`.
- Claude Code pushes events through `scripts/hook.sh`, registered in `~/.claude/settings.json`.
- Maps hooks to status: `SessionStart` → `idle`, `UserPromptSubmit`/`PreToolUse`/`PostToolUse` → `running`, `PermissionRequest` → `waiting`, `Stop`/`SessionEnd` → `done`.
- `Notification` branches on `notification_type`: `permission_prompt` → `waiting`, `idle_prompt` → `done`, others ignored.
- `PreToolUse` and `PermissionRequest` extract `tool_name` + `tool_input` to generate human-readable descriptions (e.g. "Reading config.ts", "Running git status") emitted as `AgentEvent.toolDescription`.
- `SessionEnd` cleans up thread state for immediate cleanup instead of waiting for pane scanner pruning.
- On cold start, performs a one-time seed scan of `~/.claude/projects/` JSONL files to bootstrap state for sessions already running.
- On first hook for an unknown session, reads the JSONL file once to resolve the thread name.
- No polling, no `fs.watch`, no intervals after startup.

#### Claude Code Hook Setup

Run `bun run scripts/setup-hooks.ts` to register hooks in `~/.claude/settings.json`. The setup is idempotent.

### Pi (Hook-Based)

- Receives pi-native snake_case events (`session_start`, `agent_start`, `agent_end`, `tool_execution_start`, `tool_execution_end`, `session_shutdown`) via `POST /hook` with `agent: "pi"` on the payload.
- A pi extension (`integrations/pi-extension/`) subscribes to pi's lifecycle events and POSTs to `http://127.0.0.1:${TCM_PORT:-7391}/hook`. Every POST is fire-and-forget with a 2s request timeout so hook failures never block the agent.
- Status mapping:
  - `session_start` → `idle` (optional `session_name` becomes `threadName`).
  - `agent_start` → `running`, clears `toolDescription`.
  - `tool_execution_start` → `running` with a description like `Reading foo.ts` / `Running git status`.
  - `tool_execution_end` → `running`; tool-level errors (`tool_is_error: true`) are surfaced in logs but do not change status. The LLM routinely recovers from tool failures.
  - `agent_end` with `stop_reason: "stop" | "length" | "toolUse"` → `done`; `"aborted"` → `interrupted`; `"error"` → `error` with the truncated `error_message` as `toolDescription`.
  - `session_shutdown` → emits `done` with `ended: true` and drops thread state immediately so the tracker does not hold the instance through the terminal-prune window.
- `threadId` is pi's session UUID (`ctx.sessionManager.getSessionId()`), so multiple concurrent pi instances in the same mux session render as separate sidebar rows.
- On cold start, scans `~/.pi/agent/sessions/` for files modified within the last 5 minutes and reads `SessionHeader.cwd` from each to avoid pi's lossy directory-name encoding. Hooks always win if a thread is known from both sources.

#### Pi Extension Setup

Run `bun run scripts/setup-pi-extension.ts` to symlink `integrations/pi-extension/` into `~/.pi/agent/extensions/tcm`. The setup is idempotent. See [`integrations/pi-extension/README.md`](./integrations/pi-extension/README.md) for details.

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
import type { AgentWatcher, AgentWatcherContext } from "@tcm/runtime";

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

tcm uses the capability model exported from `@tcm/mux`. A provider must implement the required `MuxProviderV1` contract and may opt into extra capabilities.

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

The server narrows providers with the runtime type guards exported from `@tcm/mux`:

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
- Understand the end-to-end runtime: see [docs/explanation/architecture.md](./docs/explanation/architecture.md).
