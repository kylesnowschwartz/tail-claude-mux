# Contracts

The behavioral contract for tcm's backend (`apps/server-go`): what an
integration, watcher, or port must match. Wire types are NOT restated
here — the normative definitions live in code, kept in lockstep by the
golden fixtures in the Go suite:

- `packages/runtime/src/shared.ts` — server↔TUI WebSocket messages
  (`ServerMessage`, `ClientCommand`) and session metadata shapes.
- `apps/server-go/wire` — the same contract on the Go side, plus
  `AgentEvent`.
- `packages/runtime/src/contracts/agent.ts` — the TUI's view of
  `AgentEvent` and `AgentStatus`.

For end-user setup, start with the docs linked from
[README.md](./README.md).

## Hook Transport

`POST /hook` (default `127.0.0.1:7391`) is the single ingestion
endpoint. Adapters claim payloads by the optional `agent` discriminator:

- `agent` missing or `"claude-code"` → Claude Code adapter.
- `agent: "pi"` → pi adapter.
- Other values are ignored.

Every hook POST is fire-and-forget: a failure must never block the
agent that fired it.

## Built-In Watchers

### Claude Code (Hook-Based)

- `scripts/hook.sh <Event>` is registered in `~/.claude/settings.json`
  for `SessionStart`, `UserPromptSubmit`, `PreToolUse`,
  `PermissionRequest`, `PostToolUse`, `Stop`, `StopFailure`,
  `Notification`, `SessionEnd`. It injects `event`, `pid`, and
  `process_snapshot` into the hook's stdin JSON and POSTs it.
  Register with `tcm-server -register-hooks` (idempotent; entries are
  async so hooks never block a turn).
- Status mapping: `SessionStart` → `idle`;
  `UserPromptSubmit`/`PreToolUse`/`PostToolUse` → `running`;
  `PermissionRequest` → `waiting`; `Stop`/`SessionEnd` → `done`;
  `StopFailure` → `error` (a turn killed by an API error fires
  `StopFailure`, not `Stop` — without it the row sticks on `running`).
  `Notification` branches on `notification_type`: `permission_prompt` →
  `waiting`, `idle_prompt` → `done`, others ignored.
- Routing is pid-first: resolve the long-lived `claude` pid from
  `process_snapshot` (ancestor walk against the tmux pane table), then
  pid → session. If the pid resolves but no pane matches, the event is
  dropped — no silent cwd fallback. cwd resolution is used only when no
  pid is available (cold-start seed).
- Cold start seeds from `~/.claude/projects/` JSONL plus
  `~/.claude/sessions/` pids, through the same pid-first channel as
  live hooks.
- `probeLiveStatus(pid, threadId)` reads `~/.claude/sessions/<pid>.json`
  for the tracker's reconcile pass: `working` (status `busy`, fresh
  `updatedAt`), `ended` (status `idle`/`waiting`, stale `busy`, or pid
  reused), or `null` (file absent / no status). `ended` also drops the
  cached thread so a resumed session re-emits cleanly.
- No polling or file watching after startup.

### Pi (Hook-Based)

- The pi extension (`integrations/pi-extension/`, installed by
  `bun run scripts/setup-pi-extension.ts`) POSTs pi's snake_case
  lifecycle events with `agent: "pi"` and a 2s timeout.
- Status mapping: `session_start` → `idle` (optional `session_name`
  becomes `threadName`); `agent_start` → `running`;
  `tool_execution_start` → `running` with a tool description;
  `tool_execution_end` → `running` (tool-level errors don't change
  status — the LLM routinely recovers); `agent_end` → `done`, or
  `interrupted` (`stop_reason: "aborted"`), or `error` with the
  truncated message; `session_shutdown` → `done` with `ended: true`
  and immediate thread-state drop.
- `threadId` is pi's session UUID, so concurrent pi instances in one
  mux session render as separate rows.
- Cold start scans `~/.pi/agent/sessions/` files modified in the last
  5 minutes, reading `SessionHeader.cwd` (pi's directory-name encoding
  is lossy). Hooks win over the seed.
- No `probeLiveStatus` yet: a stale pi `running` entry rides the
  tracker's 30-minute ceiling. Follow-up: probe against
  `~/.pi/agent/sessions/`.

## Agent Model

`AgentStatus`: `idle`, `running`, `done`, `error`, `waiting`,
`interrupted`. Terminal: `done`, `error`, `interrupted`.

### Tracker Semantics

- Instances are keyed `agent:threadId` when `threadId` exists,
  otherwise `agent`. A session can hold multiple active instances.
- Unseen state is tracked per instance, derived to the session level;
  non-terminal updates clear it for that instance.
- Stale `running`: an `exited` instance is pruned after 3 minutes. An
  `alive` instance (interactive agents stay alive between turns, so
  liveness ≠ working) is reconciled via `probeLiveStatus` after ~60s
  without a hook — `ended` → marked `done`, `working` → staleness clock
  reset. Only an alive instance that reaches the 30-minute ceiling with
  no event and no `working` confirmation is pruned.
- Any non-`running` instance whose process has `exited` is pruned
  immediately (a dead process has no pane to navigate to). Unknown
  liveness is never pruned — no scan has confirmed the exit.

## Runtime Notes

- tmux is the only supported mux. The Go server shells out to tmux
  directly (`apps/server-go/internal/tmux`); `@tcm/mux` remains as the
  TUI's tmux client seam.
- Session ordering, sidebar width, and theme persist under
  `~/.config/tcm/`; sidebars hide into a stash session rather than
  being killed.
- Programmatic metadata (status, progress, logs, notifications) is
  plain HTTP POST — see
  [docs/reference/programmatic-api.md](./docs/reference/programmatic-api.md).
