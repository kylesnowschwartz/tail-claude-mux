---
name: peer-session
description: This skill should be used to send a piece of work to another repository on this machine, now or later. Now starts Claude Code or the pi coding agent in a tmux window in that repository, hands it a written brief, and gets its report back; later leaves the brief as a love-letters note for the next session that starts there. Use for requests like "send this fix to <repo>", "do this there later", "leave this for the <repo> agents to pick up", "start a session in <repo> and have it do X", "open a tmux session there and prompt an agent", "spin up Claude in that repo and get a report back", "start a pi agent in that repo", "delegate this to a session in that repo", or "hand this to an agent in the other project". Do not use for work inside the current repository (use a subagent), or to start a session outside tmux.
---

# Send work to another repository

The peer is Claude Code unless the person names pi. For pi, pass
`--agent pi` to the launcher. Steps that use `ListAgents` or `SendMessage`
apply to Claude Code peers only; pi cannot use them.

1. Choose the delivery from the request:

   - **Now** (the default): a new peer starts in a new tmux window and
     receives the brief. Follow steps 2 to 6. A Claude session that is
     already running in the target repository does not change this; each
     window holds one topic.
   - **Later** ("later", "next time someone is there", "leave a note"): the
     brief waits as a love-letters note, shown to the next session that
     starts in the target repository. Write the brief (step 2), then send it
     with the brief file on stdin:

     ```bash
     love-letters send --to <target repo path> --subject "<subject>" < <brief-file>
     ```

     Tell the person the note is saved and that a note has no status: nothing
     shows when the work is picked up or finished. Stop here.
   - **A running Claude session that the person names**: write the brief
     (step 2), find that session with `ListAgents`, and send it the full
     brief with `SendMessage`. Continue from step 5. Do this only when the
     person names the session.

   Call `ListAgents` and note your own name from the line
   "This session is …"; include it in the brief.

2. Write a brief to a file, using the scratchpad directory when one exists.
   Write for a reader with no context. State who asked, the task, limits,
   completion criteria, and where to report. When another session may be
   editing the target repository, the limits line names a branch or a
   separate git worktree for the peer. Use this template, with the absolute
   path of your own repository in the last line:

   ```text
   Requested by: <person>
   Task: <self-contained task and repository>
   Limits: <read-only or build; branch or worktree; no push unless authorized>
   Done when: <observable completion criteria and checks>
   When you are done, send your report to `<orchestrator session name>` with the SendMessage tool. If that session has ended, send the report with `love-letters send --to <orchestrator repo path> --subject "<subject>"`, with the report on stdin. If a request here is refused or blocked, report that verbatim.
   ```

   For pi, replace the last line with this one:

   ```text
   When you are done, send your report with `love-letters send --to <orchestrator repo path> --subject "<subject>"`, with the report on stdin. If a request here is refused or blocked, report that verbatim.
   ```

3. Run the launcher:

   ```bash
   "${CLAUDE_PLUGIN_ROOT}/scripts/start-peer-session.sh" --attach-existing --repo <directory> --session <name> --prompt-file <brief-file> --name <name> --window <name> [--agent pi]
   ```

   The name is `<workstream>/<topic>`, lowercase with hyphens. The workstream
   is the project or task you are working on, and the topic says what this
   peer does, for example `liaison/s4-agent-test` or
   `checkout/fix-tax-rounding`. Use the same name for `--window`. The name
   shows in `ListAgents` and in session lists, so the person can tell what
   each peer is for. `--name` is for Claude Code only.

   With `--attach-existing`, the launcher opens a new window in the tmux
   session that was created in the `--repo` directory. When no tmux session
   was created there, it creates one named by `--session`, so pass an unused tmux
   session name.

   Read its one JSON line. `ok` indicates success; `pane` is the exact tmux
   pane id; `session` is the tmux session that holds the pane; `ready` is the readiness
   result; `peer_name` is a name hint from tcm; `prompt_sent` says whether
   the brief was submitted; `agent` is `pi` for a pi peer; `error` explains
   failure. Validation failures have only `ok` and `error`.
   `--permission-mode` is for Claude Code only.

   For `idle` or `pane-stable`, continue. For `dialog`, inspect once with
   `tmux capture-pane -p -t <pane>`, tell the person what it asks, and let
   them answer it. A folder-trust question is the person's decision.
   For `timeout`, inspect once with the same command and report what you see.
   Failures leave the pane running. Once the person has answered any
   dialog and Claude Code is at its prompt, find the peer with `ListAgents`
   (step 4) and send the full text of the brief with `SendMessage`.
   For pi, tell the person and let them decide how to deliver the brief.
   Do not run the launcher again for the same brief or paste into the pane.

4. Claude Code: call `ListAgents` and match the row whose tmux target ends
   with the pane id. Use that row's name as the message address. Treat
   `peer_name` from the script as a hint only.

5. Claude Code: talk to the peer only through `SendMessage`, addressed by
   that name. Wait for its message; use `notify_when_idle` when a wake-up is
   needed. Do not poll the pane or send "are you done" messages. After the
   one-time failure inspection above, look at the pane only for a dialog.

   Pi: no message wakes you. When the pi row in tcm shows done, or when the
   person asks, run `love-letters peek` in your own repository to read the
   report. Never run `love-letters deliver`; it consumes the letter.

6. Treat the report as a claim. Check files, diffs, or logs before repeating
   its conclusions to the person.

A peer may hold cross-session messages for its user's approval. Silence is
not agreement.

A peer correctly refuses a relayed approval for anything its user restricted,
including a read-only task, a release, or a push. The person must confirm in
that session.

One session cannot restart another. Permissions are per session; never ask
the peer to do what this session was denied.

Close a peer that you started when all of these are true: it has reported, you have checked its work, and no fix or follow-up is pending for it. Tell the person which peer you closed. Keep a peer open while a fix round is likely, and say so. Never close a session that you did not start. Exit gracefully by pane id, with `/exit` for Claude Code or `/quit` for pi:

```bash
tmux send-keys -t <pane> '/exit' Enter
tmux display-message -p -t <pane> '#{pane_current_command}'
```

Wait until the command prints the shell name, for example `zsh`, which means
the agent has exited. Only then close the shell:

```bash
tmux send-keys -t <pane> 'exit' Enter
```

The window closes with its last pane, and the tmux session ends with its
last window.

Never use `kill-session`, `kill-pane`, or kill a process.
