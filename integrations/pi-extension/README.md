# opensessions pi extension

Pushes pi lifecycle events to the local opensessions server so pi sessions
show up in the sidebar HUD alongside Claude Code.

## Install

One-shot install (symlinks this directory into `~/.pi/agent/extensions/`):

```bash
bun run scripts/setup-pi-extension.ts
```

The setup script is idempotent — rerun safely.

For quick iteration without touching the extensions dir:

```bash
pi -e /Users/you/path/to/opensessions/integrations/pi-extension
```

## What it does

The extension subscribes to six pi events and POSTs a JSON payload to
`http://127.0.0.1:7391/hook` (configurable via `OPENSESSIONS_PORT` and
`OPENSESSIONS_HOST`). Each payload carries `agent: "pi"` so the server
can route it to the pi adapter.

| Pi event | Status effect |
| --- | --- |
| `session_start` | idle row appears |
| `agent_start` | running |
| `tool_execution_start` | running + tool description (e.g. "Reading foo.ts") |
| `tool_execution_end` | running (description retained; tool errors do not change status) |
| `agent_end` | done / interrupted / error depending on stopReason |
| `session_shutdown` | row removed |

## Design notes

- **Fire-and-forget.** Every POST is never awaited. All errors are swallowed.
  If the opensessions server is down the agent is unaffected.
- **Timeout budget.** Requests are bounded by a 2s `AbortController`; a
  missing server drops the event silently rather than stalling the agent.
- **Session identity.** `threadId` is pi's session UUID
  (`ctx.sessionManager.getSessionId()`), so multiple concurrent pi
  instances in the same tmux session render as separate sidebar rows.
- **StopReason capture.** Pi's `agent_end` does not carry `stopReason`
  directly, so the extension listens on `message_end` and captures the
  trailing assistant stopReason / errorMessage to forward on `agent_end`.

## See also

- [`CONTRACTS.md`](../../CONTRACTS.md) — built-in watcher reference and
  full wire format.
- [`packages/runtime/src/agents/watchers/pi-hooks.ts`](../../packages/runtime/src/agents/watchers/pi-hooks.ts)
  — the server-side adapter this extension talks to.
