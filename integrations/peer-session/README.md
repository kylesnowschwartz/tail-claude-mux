# peer-session

Send a piece of work to another repository on this machine as a written
brief. The skill delivers it in one of two ways:

- **Now:** the launcher starts Claude Code or the pi coding agent in a new
  window of the tmux session that serves the repository, or in a new tmux
  session when none does, and submits the brief.
- **Later:** `love-letters send --to <repo>` saves the brief as a note, shown
  to the next session that starts in that repository. A note has no status.

When the person names a running Claude session, the skill sends the brief to
it with `SendMessage` and starts nothing.

A Claude Code peer reports through Claude Code's cross-session messaging, or
with a `love-letters` note to the asking repository when the asking session
has ended. A pi peer reports with the `love-letters` CLI, which the
orchestrator reads with `love-letters peek`.

A live session whose name starts with `liaison` is told about each hand-out
with one `SendMessage` line. The peer sends that session a one-line result
when it finishes.

A peer that the skill started is closed when it has reported, its work is
checked, and no follow-up is pending. The skill never closes a session it did
not start.

## Install

```text
/plugin install peer-session --marketplace kylesnowschwartz/tail-claude-mux
```

Requires tmux, Claude Code with cross-session messaging, and python3.
The later delivery and the report fallback require the `love-letters` command.
The tcm server is optional and improves the readiness check; curl reads its
local state endpoint when available. Without a server response, the launcher
waits for stable pane text.

## Script

```text
start-peer-session.sh --repo <dir> --session <tmux-session-name> --prompt-file <file> [--attach-existing] [--agent claude|pi] [--window <name>] [--name <name>] [--session-id <uuid>] [--permission-mode <mode>] [--ready-timeout <seconds>] [--port <tcm port>]
```

Run `scripts/start-peer-session.sh` from this plugin directory. Defaults are
agent `claude`, window named after the agent, permission mode `auto`, timeout
60 seconds, and port `$TCM_PORT` or 7391. The prompt file must be readable
and non-empty.

Without `--attach-existing`, the launcher creates the tmux session named by
`--session` and fails when that session exists. With `--attach-existing`, it
opens a new window in the tmux session whose directory is `--repo` and leaves
the current window selected. The directory of a tmux session is tmux's
`#{session_path}`, the directory the session was created in. When no tmux
session has that directory, the launcher creates the one named by `--session`.

`--name <name>` sets a Claude Code peer's session name with `claude -n`. The
name matches `^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`, and `--agent pi` with
`--name` is a validation error. The skill names peers `<workstream>/<topic>`.

`--session-id <uuid>` starts a Claude Code peer with that conversation ID, using
`claude --session-id`. The value is a UUID in 8-4-4-4-12 lower-case hexadecimal form, and
`--agent pi` with `--session-id` is a validation error.

`--agent pi` runs `pi` in the pane and waits for the pi row in
tcm state; `--permission-mode` with `--agent pi` is a validation error.
Both agents start in a shell pane, so the tmux session outlives the agent.
Pi requires the `pi` and `love-letters` commands on PATH.

Stdout contains one JSON line; diagnostics go to stderr. Success exits zero:

```json
{"ok":true,"pane":"%128","session":"peer","ready":"idle","peer_name":"project","prompt_sent":true}
```

`pane` identifies the new pane and `session` names the tmux session that
holds it, which with `--attach-existing` can differ from `--session`.
`ready` is `idle` for tcm readiness or `pane-stable` for the fallback.
`peer_name` is a hint; the skill resolves the address with `ListAgents`.
A pi launch adds `"agent":"pi"`.

Failure exits nonzero with `ok:false` and `error`. Validation failures create
nothing. After creation, a blocked trust dialog returns `ready:"dialog"`;
a readiness timeout returns `ready:"timeout"`. Both include `pane`,
`session`, and `prompt_sent:false` and leave the pane running for inspection.
The launcher never kills a session, pane, or process.

The launcher submits the initial brief through a tmux buffer. After a dialog
or timeout, let the person answer any dialog and wait until Claude Code is at
its prompt. Find the peer with `ListAgents` and send the full brief with
`SendMessage`; do not rerun the launcher for that brief or paste into
the pane. Reports and further messages use `SendMessage`.

## Test

From the repository root:

```bash
bash integrations/peer-session/tests/start-peer-session.test.sh
```

The test uses fake tmux, curl, and sleep commands and starts no real sessions.
