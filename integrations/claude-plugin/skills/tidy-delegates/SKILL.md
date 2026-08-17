---
name: tidy-delegates
description: >
  Close the finished TCM codex delegate tmux windows left over from
  tcm-delegate-codex runs. Use ONLY when Kyle explicitly asks to tidy up / clean up /
  close the delegate windows (or "TCM codex shells"). Do NOT invoke proactively
  just because a delegation finished — tcm-delegate-codex leaves windows open until
  Kyle says otherwise. Scoped to the current tmux session by default;
  -all-sessions widens it. Requires the TCM server on localhost:7391.
---

# tidy-delegates — sweep finished delegate windows

Close the finished TCM codex delegate windows left over from
`tcm-delegate-codex` runs, via `tcm tidy`:

```sh
TCM="${CLAUDE_PLUGIN_ROOT}/../../apps/server-go/bin/tcm"
```

Scope is the current tmux session by default (`-all-sessions` widens it).
The sweep is deterministic and fails safe: it only matches windows whose
pane start command carries the `codex --profile tcm-delegate` spawn
signature, and by default only closes ones whose TCM status is terminal
(done/error/interrupted/gone). Running or approval-waiting delegates, and
windows Kyle opened himself, are never touched.

Flags:

- `-all-sessions` — sweep every tmux session, not just the current one
- `-keep N` — preserve the N most-recently-active delegate windows
- `-all` — close every matched delegate regardless of status (interrupts live work)
- `-dry-run` — print what would close, change nothing
- `-port N` — tcm server port (defaults to 7391)

Pass only the flags Kyle actually asked for, each as its own literal token.
Never splice raw free text, shell operators, `;`, `|`, `$(...)`, or globs
into the command line; if his request contains anything else, drop it and,
if it looks intentional, ask him. `-keep`'s value must be a non-negative
integer.

## Step 1 — preview first (changes nothing)

ALWAYS run the dry-run before anything else, and read its output. Never run
the real sweep before inspecting this preview — the preview is the only
thing standing between a done-but-unread delegate and a closed window.

```sh
"$TCM" tidy -dry-run [only the recognized flags Kyle supplied]
```

Output:

```json
{"dryRun":true,"candidates":[{"windowId":"@7","sessionName":"kyle","paneId":"%42","status":"done","action":"would-close"}],"closed":0}
```

`action` per candidate is one of `keep` (inside `-keep N`'s window), `skip`
(not yet terminal and `-all` wasn't passed), `would-close` (dry run), or —
on a real run — `closed`/`gone`.

## Step 2 — review, then act

- Default to holding fire. If you cannot positively confirm that every
  window in the preview has already had its output consumed — or if any is
  worth keeping for reference — do NOT close anything. Name those windows,
  say why you're holding fire, and ask Kyle first. A fresh tidy invocation
  usually lacks the context to know what's been read, so uncertainty means
  ask.
- Only when every previewed window is confirmed safe to close, run the
  sweep for real and report the one-line result (closed / kept / skipped,
  and the scope):

  ```sh
  "$TCM" tidy [only the recognized flags Kyle supplied]
  ```

Do not add `-all` or `-all-sessions` unless Kyle asked for them.

This closes windows only — it does not remove any worktree the delegate ran
in. Sweep those separately per `tcm-delegate-codex`'s cleanup section
(`git worktree remove` / `prune`).
