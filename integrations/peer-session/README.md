# peer-session

Start Claude Code in a new tmux session in another repository, submit a written
brief, and receive its report through Claude Code's cross-session messaging.
The skill checks for an existing session before starting another one.

## Install

```text
/plugin install peer-session --marketplace kylesnowschwartz/tail-claude-mux
```

Requires tmux, Claude Code with cross-session messaging, and python3.
The tcm server is optional and improves the readiness check; curl reads its
local state endpoint when available. Without a server response, the launcher
waits for stable pane text.

## Script

```text
start-peer-session.sh --repo <dir> --session <tmux-session-name> --prompt-file <file> [--window <name>] [--permission-mode <mode>] [--ready-timeout <seconds>] [--port <tcm port>]
```

Run `scripts/start-peer-session.sh` from this plugin directory. Defaults are
window `claude`, permission mode `auto`, timeout 60 seconds, and port
`$TCM_PORT` or 7391. The prompt file must be readable and non-empty.

Stdout contains one JSON line; diagnostics go to stderr. Success exits zero:

```json
{"ok":true,"pane":"%128","session":"peer","ready":"idle","peer_name":"project","prompt_sent":true}
```

`pane` identifies the new pane and `session` names its tmux session.
`ready` is `idle` for tcm readiness or `pane-stable` for the fallback.
`peer_name` is a hint; the skill resolves the address with `ListAgents`.

Failure exits nonzero with `ok:false` and `error`. Validation failures create
nothing. After creation, a blocked trust dialog returns `ready:"dialog"`;
a readiness timeout returns `ready:"timeout"`. Both include `pane`,
`session`, and `prompt_sent:false` and leave the session running for inspection.
The launcher never kills a session, pane, or process.

The launcher submits the initial brief through a tmux buffer. After a dialog
or timeout, let the person answer any dialog and wait until Claude Code is at
its prompt. Find the peer with `ListAgents` and send the full brief with
`SendMessage`; do not rerun the launcher for that session name or paste into
the pane. Reports and further messages use `SendMessage`.

## Test

From the repository root:

```bash
bash integrations/peer-session/tests/start-peer-session.test.sh
```

The test uses fake tmux, curl, and sleep commands and starts no real sessions.
