# How To Build Plugins For tcm

tcm loads extension code as default-exported factory functions. Those factories can register mux providers, agent watchers, or both.

This guide is intentionally task-oriented. For the exact TypeScript contracts, see [CONTRACTS.md](./CONTRACTS.md).

## Before You Start

The easiest way to iterate is with a local plugin in `~/.config/tcm/plugins/`. Package-based plugins are supported too, but local files remove package resolution friction while you are developing. Amp, Claude Code, Codex, and OpenCode already ship as built-in watchers, so plugins are only needed for additional agents or mux providers.

Every plugin exports a default function:

```ts
import type { PluginAPI } from "@tcm/runtime";

export default function (api: PluginAPI) {
}
```

The runtime passes:

| Property | Meaning |
| --- | --- |
| `registerMux(provider)` | Register a mux provider |
| `registerWatcher(watcher)` | Register an agent watcher |
| `serverHost` | Current server host, currently `127.0.0.1` |
| `serverPort` | Current server port, currently `7391` |

## How Plugins Are Loaded

The server loads extensions in this order:

1. Built-in mux providers from `@tcm/mux-tmux` and `@tcm/mux-zellij`
2. Local plugins from `~/.config/tcm/plugins/`
3. Package names listed in `~/.config/tcm/config.json`

Local plugin discovery supports:

```text
~/.config/tcm/
  config.json
  plugins/
    my-plugin.ts
    another-plugin.js
    custom-provider/
      index.ts
```

Package-based plugins are loaded through `require()`, so they must be resolvable from the tcm runtime environment.

## How To Create A Local Mux Plugin

### 1. Create the plugin file

Create `~/.config/tcm/plugins/my-mux.ts`:

```ts
import type { PluginAPI, MuxProvider, MuxSessionInfo } from "@tcm/runtime";

class MyMuxProvider implements MuxProvider {
  readonly specificationVersion = "v1" as const;
  readonly name = "my-mux";

  listSessions(): MuxSessionInfo[] {
    return [];
  }

  switchSession(name: string, clientTty?: string): void {
  }

  getCurrentSession(): string | null {
    return null;
  }

  getSessionDir(name: string): string {
    return "";
  }

  getPaneCount(name: string): number {
    return 1;
  }

  getClientTty(): string {
    return "";
  }

  createSession(name?: string, dir?: string): void {
  }

  killSession(name: string): void {
  }

  setupHooks(serverHost: string, serverPort: number): void {
  }

  cleanupHooks(): void {
  }
}

export default function (api: PluginAPI) {
  api.registerMux(new MyMuxProvider());
}
```

### 2. Select it in config if needed

If your runtime could detect multiple providers, pin your choice in `~/.config/tcm/config.json`:

```json
{
  "mux": "my-mux",
  "plugins": []
}
```

### 3. Start tcm and verify registration

Run the TUI or server. If your provider resolves successfully, it becomes part of the combined session list.

## How To Create An Agent Watcher Plugin

### 1. Create the watcher file

Create `~/.config/tcm/plugins/my-agent.ts`:

```ts
import type { PluginAPI, AgentWatcher, AgentWatcherContext } from "@tcm/runtime";
import { watch } from "fs";

class MyAgentWatcher implements AgentWatcher {
  readonly name = "my-agent";
  private watcher: ReturnType<typeof watch> | null = null;

  start(ctx: AgentWatcherContext): void {
    this.watcher = watch("/tmp/my-agent", () => {
      const session = ctx.resolveSession("/path/to/project");
      if (!session) return;

      ctx.emit({
        agent: this.name,
        session,
        status: "running",
        ts: Date.now(),
        threadId: "example-thread",
        threadName: "Example task",
      });
    });
  }

  stop(): void {
    this.watcher?.close();
    this.watcher = null;
  }
}

export default function (api: PluginAPI) {
  api.registerWatcher(new MyAgentWatcher());
}
```

### 2. Map agent state to session directories

Watchers should emit only after they can resolve a real project directory to a mux session with `ctx.resolveSession(projectDir)`.

### 3. Emit stable instance identifiers

If your agent can run multiple threads in one repo, include `threadId`. The tracker uses it to keep instances separate.

## How To Package A Plugin

Once the local version works, package it like any other Bun or TypeScript module.

Suggested `package.json` shape:

```json
{
  "name": "tcm-mux-my-mux",
  "version": "0.1.0",
  "type": "module",
  "main": "index.ts",
  "peerDependencies": {
    "@tcm/runtime": ">=0.1.0"
  }
}
```

Then add it to config:

```json
{
  "mux": "my-mux",
  "plugins": ["tcm-mux-my-mux"]
}
```

## How To Test Plugin Loading Quickly

### Local file plugin

1. Write the file under `~/.config/tcm/plugins/`.
2. Start `cd /path/to/tcm/apps/tui && bun run start` or run `bun run start:tui` from the repo root.
3. Confirm your provider or watcher behavior in the sidebar.

### Linked package plugin

1. Create the package elsewhere.
2. Link or install it so Bun can resolve it from the tcm runtime.
3. Add the package name to `plugins` in config.

## Naming Conventions

These are conventions, not runtime requirements:

| Plugin type | Common name pattern |
| --- | --- |
| Mux provider | `tcm-mux-<name>` |
| Agent watcher | `tcm-agent-<name>` |

## Practical Notes

- Local plugins can be `.ts` or `.js` files.
- Directory plugins need an `index.ts` or `index.js` entrypoint.
- The current runtime passes fixed server host and port values through `PluginAPI`.
- Built-in watchers already cover Amp, Claude Code, and OpenCode, so new watcher work is usually for unsupported agents.

## Related Docs

- Exact contracts: [CONTRACTS.md](./CONTRACTS.md)
- Runtime behavior: [docs/explanation/architecture.md](./docs/explanation/architecture.md)
- User configuration: [docs/reference/configuration.md](./docs/reference/configuration.md)
