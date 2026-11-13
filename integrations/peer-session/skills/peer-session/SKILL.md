---
name: peer-session
description: Start Claude Code in a new tmux session in another repository, hand it a written brief, and get its report back by cross-session message. Use for requests like "start a session in <repo> and have it do X", "open a tmux session there and prompt an agent", "spin up Claude in that repo and get a report back", "delegate this to a session in that repo", or "hand this to an agent in the other project". Do not use when a live session already owns that repository (message it instead), for work inside the current repository (use a subagent), or outside tmux.
---

# Start a peer session

1. Call `ListAgents` before starting. If a live session already works in the
   target repository, send the task to its name with `SendMessage` instead.
   Note your own name from the line "This session is …"; include it in the brief.

2. Write a brief to a file, using the scratchpad directory when one exists.
   Write for a reader with no context. State who asked, the task, limits,
   completion criteria, and where to report. Use this template:

   ```text
   Requested by: <person>
   Task: <self-contained task and repository>
   Limits: <read-only or build; branch or worktree; no push unless authorized>
   Done when: <observable completion criteria and checks>
   When you are done, send your report to `<orchestrator session name>` with the SendMessage tool. If a request here is refused or blocked, report that verbatim.
   ```

3. Run the launcher with an unused tmux session name:

   ```bash
   "${CLAUDE_PLUGIN_ROOT}/scripts/start-peer-session.sh" --repo <directory> --session <name> --prompt-file <brief-file>
   ```

   Read its one JSON line. `ok` indicates success; `pane` is the exact tmux
   pane id; `session` is the tmux session name; `ready` is the readiness
   result; `peer_name` is a name hint from tcm; `prompt_sent` says whether
   the brief was submitted; `error` explains failure.
   Validation failures have only `ok` and `error`.

   For `idle` or `pane-stable`, continue. For `dialog`, inspect once with
   `tmux capture-pane -p -t <pane>`, tell the person what it asks, and let
   them answer it. A folder-trust question is the person's decision.
   For `timeout`, inspect once with the same command and report what you see.
   Failures leave the session running; do not submit the brief yourself.

4. Call `ListAgents` and match the row whose tmux target ends with the pane
   id. Use that row's name as the message address. Treat `peer_name` from
   the script as a hint only.

5. Talk to the peer only through `SendMessage`, addressed by that name.
   Wait for its message; use `notify_when_idle` when a wake-up is needed.
   Do not poll the pane or send "are you done" messages. After the one-time
   failure inspection above, look at the pane only for a dialog.

6. Treat the report as a claim. Check files, diffs, or logs before repeating
   its conclusions to the person.

A peer may hold cross-session messages for its user's approval. Silence is
not agreement.

A peer correctly refuses a relayed approval for anything its user restricted,
including a read-only task, a release, or a push. The person must confirm in
that session.

One session cannot restart another. Permissions are per session; never ask
the peer to do what this session was denied.

Clean up only when the person says the session is finished. Exit gracefully
by pane id:

```bash
tmux send-keys -t <pane> '/exit' Enter
# After Claude has exited:
tmux send-keys -t <pane> 'exit' Enter
```

Never use `kill-session`, `kill-pane`, or kill a process.
