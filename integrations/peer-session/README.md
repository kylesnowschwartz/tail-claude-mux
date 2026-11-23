# peer-session

One skill for working with another coding agent session in tmux, usable from
Claude Code and from the pi coding agent. It covers two cases:

- **Start a peer.** The launcher starts Claude Code or pi in a new window of
  the tmux session that serves a repository, or in a new tmux session when
  none does, and submits a written brief. The peer reports back when it is
  done.
- **Message a session that already runs.** The skill sends through the
  harness's message tool when one reaches the session, and types one checked
  line into the agent's tmux pane when none does.

The channel depends on both harnesses:

| This session | Other session | Channel |
| --- | --- | --- |
| Claude Code | Claude Code | Claude Code's `SendMessage`, with names from `ListAgents` |
| pi | pi | `SendMessage` and `ListPeers` from the pi-agent-fleet extension |
| Claude Code | pi, Codex, or another agent | tmux pane delivery with `send-to-pane.sh` |
| pi | Claude Code, Codex, or another agent | tmux pane delivery with `send-to-pane.sh` |

The brief carries the address for the report: the orchestrator's session
name for a pair in one harness, and its tmux pane id (`$TMUX_PANE`) for a
mixed pair.

A peer that the skill started is closed when it has reported, its work is
checked, and no follow-up is pending. The skill never closes a session it did
not start.

## Layout

```text
.claude-plugin/plugin.json      Claude Code plugin manifest
skills/peer-session/
  SKILL.md                      routes, brief, launcher steps, closing
  references/message-a-pane.md  locate, check, send to, and verify a pane
  scripts/start-peer-session.sh launcher
  scripts/send-to-pane.sh       one line into an agent pane, with checks
  scripts/pi-session-name.sh    name and id of the calling pi session
tests/                          shell tests for the three scripts
```

The skill folder holds everything the skill runs, and SKILL.md names each
script by a path relative to that folder. Claude Code gives the folder to
the model as "Base directory for this skill" when the skill loads; pi gives
the location of SKILL.md in its skill list.

## Install

Claude Code, as a plugin:

```text
/plugin install peer-session --marketplace kylesnowschwartz/tail-claude-mux
```

pi, as a skill folder: add the absolute path of `skills/peer-session` in a
checkout of this repository to `skills` in pi's settings file
(`~/.pi/agent/settings.json`):

```json
{
  "skills": ["/path/to/tail-claude-mux/integrations/peer-session/skills/peer-session"]
}
```

Requires tmux and python3. Messaging between Claude Code sessions requires
Claude Code with cross-session messaging. Messaging between pi sessions
requires the pi-agent-fleet extension; `--agent pi` requires the `pi`
command on PATH. The tcm server is optional and improves the readiness
check; curl reads its local state endpoint when available. Without a server
response, the launcher waits for stable pane text.

## Launcher

```text
start-peer-session.sh --repo <dir> --session <tmux-session-name> --prompt-file <file> [--attach-existing] [--close-on-exit] [--agent claude|pi] [--window <name>] [--name <name>] [--session-id <uuid>] [--permission-mode <mode>] [--ready-timeout <seconds>] [--port <tcm port>]
```

Run `skills/peer-session/scripts/start-peer-session.sh`. Defaults are
agent `claude`, window named after the agent, permission mode `auto`, timeout
60 seconds, and port `$TCM_PORT` or 7391. The prompt file must be readable
and non-empty. The launcher does not delete the prompt file; it must stay
readable until the agent has started. For Claude Code, the full absolute path
of the prompt file must not hold a single quote, a backslash, or a control
character, and the task must hold text other than white space and be at most
262144 bytes (256 KB), well under the macOS argument limit of 1 MB.

Claude Code gets the task as its start argument. The command typed into the
pane reads the task from the prompt file, so the task text is never typed:

```text
peer_task="$(cat -- '<absolute prompt path>')" && [ -n "$peer_task" ] && claude --permission-mode auto [--session-id <uuid>] [-n <name>] -- "$peer_task"
```

A failed or empty read stops the command before Claude Code starts.

Nothing is pasted into a Claude Code pane and no Enter follows the start
command. pi gets the task pasted through a tmux buffer and submitted with
Enter once tcm lists pi as idle.

Without `--attach-existing`, the launcher creates the tmux session named by
`--session` and fails when that session exists. With `--attach-existing`, it
opens a new window in the tmux session whose directory is `--repo` and leaves
the current window selected. The directory of a tmux session is tmux's
`#{session_path}`, the directory the session was created in. When no tmux
session has that directory, the launcher creates the one named by `--session`.

`--name <name>` sets the peer's session name, with `claude -n` for Claude
Code and `pi --name` for pi. The name matches
`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`. The skill names peers
`<workstream>/<topic>`. A pi session is addressed by that name in
`SendMessage` and listed under it in `ListPeers`.

`--session-id <uuid>` starts a Claude Code peer with that conversation ID, using
`claude --session-id`. The value is a UUID in 8-4-4-4-12 lower-case hexadecimal form, and
`--agent pi` with `--session-id` is a validation error.

`--close-on-exit` makes the pane end when Claude Code ends:

| Part | Behavior with `--close-on-exit` |
| --- | --- |
| Start command | `exec claude ...`, so Claude Code replaces the shell |
| `remain-on-exit` | Set `on` for the pane before the start command; set `off` once tcm lists the Claude Code conversation in the pane and the pane is checked again after the wait for its name |
| Readiness | tcm must list a Claude Code conversation in the pane with status idle, running, working, waiting, or done; stable pane text does not count |
| Task already finished | With status done, the result has `ready:"done"`, and a pane whose program has ended is closed |
| Conversation lost during the wait for its name | The launcher fails with `ready:"lost"` |
| Failure after the pane exists | The pane stays open for reading and the result adds `pane_tail`, the last 20 non-empty lines of the pane, when the pane's text could be read |
| Claude Code ends before it is ready | The launcher fails at once with `ready:"exited"` |

- `--agent pi` with `--close-on-exit` is a validation error.
- `claude` must be an executable on PATH: `exec` does not use a shell alias
  or function named `claude`.
- A timeout error says whether tcm gave no answer or listed no ready or
  working conversation in the pane.

`--agent pi` runs `pi`, or `pi --name <name>`, in the pane and waits for the
pi row in tcm state; `--permission-mode` with `--agent pi` is a validation
error.
Without `--close-on-exit`, both agents start in a shell pane, so the tmux
session outlives the agent.

Stdout contains one JSON line; diagnostics go to stderr. Success exits zero:

```json
{"ok":true,"pane":"%128","session":"peer","ready":"idle","peer_name":"project","prompt_sent":true}
```

`pane` identifies the new pane and `session` names the tmux session that
holds it, which with `--attach-existing` can differ from `--session`.
`ready` is `idle`, `working`, or `done` for tcm readiness, or `pane-stable`
for the fallback. Without tcm, a Claude Code pane whose current program is
still the shell fails with `ready:"not-started"` and `pane_tail`, for example
after `command not found: claude`. `prompt_sent` is true when the task reached the agent: for Claude
Code, the task was in the start command and Claude Code was seen ready or
working; for pi, the task was pasted and submitted.
With `--close-on-exit`, failures add `pane_tail` and `ready` can be `exited`
or `lost`.
`peer_name` is a hint; the skill resolves a Claude Code peer's address with
`ListAgents`.
A pi launch adds `"agent":"pi"`.

Failure exits nonzero with `ok:false` and `error`. Validation failures create
nothing. After creation, a blocked trust dialog returns `ready:"dialog"`;
a readiness timeout returns `ready:"timeout"`. Both include `pane`,
`session`, and `prompt_sent:false` and leave the pane open for inspection.
After `ready:"exited"`, the program in the pane has ended.
The launcher never kills a session or process. It closes a pane only when the
program in it has ended after a finished task, with `--close-on-exit`.

The launcher answers no dialog. After a dialog or timeout, let the person
answer any dialog. A Claude Code peer holds the brief in its start command, so
find the peer with `ListAgents` and check whether its conversation shows the
brief before anything else is sent; send the full brief with `SendMessage`
only when it does not. A pi peer has not received the brief: a pi
orchestrator sends it with `SendMessage`, and in a mixed pair the person
decides how to deliver it. Do not rerun the launcher for that brief or paste
into the pane.

## Pane delivery

```text
send-to-pane.sh --pane <pane id> (--check | --message <text> | --message-file <file>) [--wait <seconds>]
```

`skills/peer-session/scripts/send-to-pane.sh` puts one line into the input of
an agent that runs in another tmux pane. `--pane` takes a tmux pane id such
as `%12`. Stdout contains one JSON line.

`--check` sends nothing and reports the pane: `command`, `path`, and
`pane_tail`, the last 15 non-empty lines.

With a message, the script:

1. Refuses a message that is empty, holds a newline or another control
   character, or is longer than 4000 characters.
2. Refuses a pane that is the caller's own (`$TMUX_PANE`), that does not
   exist, whose program has ended, or whose program is a shell.
3. Types the text with `tmux send-keys -l` and polls until the pane shows it,
   for at most `--wait` seconds (default 5). When the text does not show,
   it fails with `typed:false` and sends no Enter.
4. Sends Enter as a separate key and polls until the pane changes. When the
   pane shows no change, it sends Enter once more, then fails with
   `pane_changed:false`.

```json
{"ok":true,"pane":"%12","command":"claude","path":"/work/repo","typed":true,"enter_count":1,"pane_changed":true,"pane_tail":"..."}
```

`ok` means the text showed and the pane changed after Enter. The caller
reads `pane_tail` to confirm that the agent took or queued the message.

## pi session name

`skills/peer-session/scripts/pi-session-name.sh` prints the name and id of
the pi session that runs it:

```json
{"ok":true,"name":"peer/topic","id":"0123456789ab","cwd":"/work/repo"}
```

The pi-agent-fleet extension keeps one record file for each running
interactive pi session in `~/.pi/agent/pi-agent-fleet-peers/`
(`PI_AGENT_FLEET_PEERS_DIR` names another folder). The script finds the
record whose process id is the script's own process or one above it. `name`
is the name that `ListPeers` shows to other sessions, with `#` and four
characters of the id when another running session has the same name. `id`
stays the same when the name changes, and `SendMessage` accepts either.

## Test

From the repository root:

```bash
for t in integrations/peer-session/tests/*.test.sh; do bash "$t" || break; done
```

- `start-peer-session.test.sh`: most cases use fake tmux, curl, and sleep
  commands. When tmux and zsh are on PATH, further cases run on a throwaway
  tmux server (`tmux -L`) with zsh, a stand-in `claude` that records its
  arguments, and a stand-in tcm.
- `send-to-pane.test.sh`: runs on a throwaway tmux server, with `cat` as the
  stand-in agent.
- `pi-session-name.test.sh`: uses a stand-in peers folder.
- `skill-files.test.sh`: checks that every script and reference file that
  the skill names exists in the skill folder.

No test uses the default tmux server or a real agent.
