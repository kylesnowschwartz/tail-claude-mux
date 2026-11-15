# server-go

The Go backend for tcm (work in progress). Scope and staged plan:
`.agent-history/SCOPING-go-backend.md` at the repo root.

## wire

Frozen client↔server JSON contract: a field-for-field port of
`packages/runtime/src/shared.ts`, `contracts/agent.ts`, and
`contracts/parse-hook-payload.ts`. The TypeScript side is the contract of
record while both servers exist; mirror changes here and keep
`wire/testdata/state-live.json` (a live `GET /state` capture) current —
the tests strict-decode it, so an unmirrored field fails loudly.

```sh
cd apps/server-go && go test ./...
```

## Activity record

The server appends one line for every hook event it receives (`POST /hook`),
including events it cannot match to a tmux pane, and one line each time the
status it derives for a pane changes. The state the server rebuilds at start
is not recorded; a pane's first status from a hook event after start is. The lines are kept in
`~/.local/state/tcm/events/YYYY-MM-DD.jsonl`, one JSON Lines file per local
day. The directory has mode 0700 and each file mode 0600. The server is the
only writer.

Every line has these fields:

- `seq`: a sequence number that rises by 1 for each line, across day files
  and server restarts.
- `kind`: `hook` or `status`.
- `received_ms`: the time the server received or derived it, in Unix
  milliseconds.

A `hook` line also has `event`, `session_id` (the Claude session ID), `cwd`,
`tool_name`, `notification_type`, `stop_reason`, `pid`, and
`transcript_path`. It holds no conversation content: no prompt text and no
tool input. A reader that needs content opens the transcript.

A `status` line also has `tmux_session`, `pane_id`, `session_id`, `status`,
and `tool_verb`.

Empty fields are left out. The same event can appear more than once; a reader
that needs to ignore repeats does so itself.

`GET /events?since=<seq>&limit=<count>` returns the lines with a sequence
number above `since` (default 0), oldest first, at most `limit` of them
(default 100, at most 1000):

```json
{"oldest_seq":1,"events":[{"seq":2,"kind":"hook","received_ms":1794740340000,"event":"Stop","session_id":"…","cwd":"/project"}]}
```

`oldest_seq` is the oldest line still kept. When `since` is older than that,
the answer starts at the oldest kept line, so a reader whose `since` is below
`oldest_seq - 1` knows lines are missing. A last line with no newline is still
being written and is left out.

Day files older than 30 days are deleted when the server starts and once a
day. Files are never rewritten, so `tail -f` on a day file keeps working.
