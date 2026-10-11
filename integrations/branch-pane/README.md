# branch-pane

`/branch` opens a branch of the current conversation in a new tmux window of
the same tmux session, and switches to it. The session you typed `/branch` in
keeps running unchanged.

The branch is `claude --resume <session id> --fork-session`: a new session
that starts with a copy of this conversation so far.

## Install

```
/plugin install branch-pane --marketplace kylesnowschwartz/tail-claude-mux
```

Needs tmux and Claude Code 2.1.296 or later.

## Usage

```
/branch                  open the branch in a new tmux window named "branch"
/branch <name>           name the window and the new session <name>
/branch here [name]      run Claude Code's own /branch in this session instead
```

With no name, the new session is named after this one: `<this session's name>
(branch)`, or `(branch 2)`, `(branch 3)` and so on when a running session
already has that name. When this session's name cannot be found, it is
`branch`.

Outside tmux (no `TMUX_PANE`) or without a local terminal, `/branch` runs
Claude Code's own `/branch` unchanged.

If you changed the model with `/model` during the session, the branch starts
on that model (`--model`); otherwise it starts the way a resumed session does.

## What it does on your machine

- **Programs it starts:** `tmux display -p -t $TMUX_PANE '#{session_id}'` to find this
  pane's tmux session, then `tmux new-window -P -F '#{window_index} #{pane_id}' -t
  <session>: -c <project root> -n <name> claude --resume <id> --fork-session --name
  <name> [--model <model>]`. The reply shows the name, window, pane and claude command line.
- **Hooks:** the `/branch` command, and session start (to note the starting model).
- **What it reads:** `TMUX_PANE`, `HOME`, the session id, project root, model and surfaces,
  and, when no name is given, the `~/.claude/sessions/*.json` files Claude Code keeps for
  running sessions (only their `sessionId` and `name`).
- **What it stores:** nothing.
- **What it sends:** nothing. No network calls.
