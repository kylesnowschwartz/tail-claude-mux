# Message an agent in a tmux pane

Use this to put one line into the input of an agent that already runs in
another tmux pane, when no message tool reaches it. It works for Claude Code,
pi, Codex, and any other agent with a line input.

`scripts/send-to-pane.sh` does the typing and the checks. Its path is
relative to the skill folder, which is the parent of the `references` folder
that holds this file. Run it by its absolute path.

When no tmux server runs or no pane matches, say so. Do not create a pane or
a session for this.

## 1. Locate the pane

Skip this step when you already hold the pane id, for example from a brief
or from the launcher's `pane` field.

```bash
tmux list-panes -a -F '#{pane_id} #{session_name}:#{window_index}.#{pane_index} #{pane_current_command} #{pane_current_path}'
```

Pick the pane from what the person said: the project, the tool, or
"the neighbouring pane".

- An agent pane reports `claude`, `codex`, `pi`, `node`, or a version string
  such as `2.1.201` as its command.
- Match the path against the repository the person named.
- Your own pane is `$TMUX_PANE`; leave it out.

When more than one pane fits, run step 2 on each to see what runs there, or
ask the person.

## 2. Check the pane before you send

```bash
scripts/send-to-pane.sh --pane <pane id> --check
```

This sends nothing. It prints one JSON line: `command` is the program in the
pane, `path` its directory, and `pane_tail` the last lines on screen. Read
`pane_tail`:

- It shows an agent's input, and the input line is empty: continue.
- The input line holds text that someone has typed: do not send. Report
  that to whoever asked.
- The agent is in the middle of a task: continue. Claude Code, pi, and Codex
  queue a message that arrives while they work.
- A dialog or a question blocks the input: do not send. Tell the person.

## 3. Write the message

- **One line.** A newline submits early in an agent's input, and the script
  refuses a message with one. The limit is 4000 characters.
- **Longer content goes in a file.** Write it to a file and send a line that
  names the file, for example
  `Report from <session>: the tax fix passes its tests. Full report: /tmp/x/report.md`.
- **No shared context.** The reader has not seen your conversation. Name the
  repository, branch, file, or version, say who you are, and say what you
  want back and where.
- Do not start the line with `/` or `!`; an agent reads those as a command.

## 4. Send and verify

```bash
scripts/send-to-pane.sh --pane <pane id> --message '<one line>'
```

Wrap the message in single quotes and leave apostrophes out of it, or write
the line to a file and pass `--message-file <file>`.

The script types the text, waits until the pane shows it, sends Enter as a
separate key, and waits until the pane changes. It prints one JSON line:

| Field | Meaning |
| --- | --- |
| `ok` | The text showed in the pane and the pane changed after Enter |
| `typed` | The text showed in the pane; when false, Enter was not sent |
| `enter_count` | How often Enter was sent; a second Enter follows when the pane shows no change after the first |
| `pane_changed` | The pane changed after Enter |
| `pane_tail` | The last lines of the pane after the send |
| `error` | Why the script refused or failed |

The script refuses a pane that is your own, that does not exist, or whose
program is a shell, because then no agent runs there.

`ok` does not prove that the agent took the message. Read `pane_tail` and
confirm that the message shows as submitted or queued, and no longer sits in
the input line. Report that verified state, not only "sent". When `ok` is
false, report `error` and what `pane_tail` shows; do not send the same
message again without reading the pane.

## Follow-up

- To read a reply, capture the pane again:
  `tmux capture-pane -p -S -100 -t <pane id>`. Do not wait in a loop for it.
- For each further message, repeat steps 2 to 4.
