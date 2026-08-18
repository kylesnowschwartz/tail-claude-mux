---
name: tcm-delegate-codex
description: >
  Delegate a coding task to a visible Codex agent running in a tmux pane via
  TCM, driven end-to-end by the `tcm` CLI (delegate → wait → followup →
  result), instead of a backgrounded /codex:rescue job. Use for
  implementation work that changes the tree (MO-002 routing: repo-gets-changes
  → visible pane), when Kyle asks to "watch" a delegation, or for parallel
  worktree delegations. NOT for read-only investigation/review/second opinions
  whose output the orchestrator consumes directly — use /codex:rescue for
  those. Headless or tmux-less contexts: fall back to /codex:rescue.
---

# tcm-delegate-codex — visible Codex delegation via the tcm CLI

Every step below is a call to the `tcm` binary. Set this once at the top of
the session:

```sh
TCM="${CLAUDE_PLUGIN_ROOT}/../../apps/server-go/bin/tcm"
```

`${CLAUDE_PLUGIN_ROOT}` is `integrations/claude-plugin` inside the tcm repo
checkout; the binary lives at `apps/server-go/bin/tcm`, two levels up.
`scripts/restart.sh` builds it on every server restart. Codex delegates only;
for claude delegates, or when TCM isn't running, fall back to /codex:rescue.

## Preconditions (check once, cheaply)

```sh
curl -fsS localhost:7391/state >/dev/null
```

Fails → TCM isn't running; fall back to /codex:rescue and say why.

## 1. Delegate

For parallel or long work, create the git worktree FIRST (MO-002) and pass
its path — TCM does no git writes; the orchestrator owns all git. Codex trust
follows the worktree's MAIN repository: a worktree of a trusted repo is
trusted anywhere on disk. Only a genuinely fresh non-worktree dir hits the
interactive trust prompt and stalls the run (see the idle gotcha below).

End the brief with: `When complete, print exactly one line starting with
RESULT:, then stop.` — the result you read back keys off that final message.

```sh
"$TCM" delegate -dir /abs/worktree -name kebab-window-name -brief "<context-complete per MO-002>"
```

Any multiline brief goes in a file instead: `-brief-file /abs/brief.md`. Claude
Code's shell wrapper can join embedded newlines, so `-brief` is only safe for
a genuine one-liner.

Output:

```json
{"outcome":"spawned","sessionName":"kyle","paneId":"%42","windowId":"@7","ownerSession":"kyle","alive":true,"delegationId":"6f2c…-uuid"}
```

or, with no tmux context:

```json
{"outcome":"spawn-failed","detail":"no-tmux-owner"}
```

or, when the pane dies within the 2s post-spawn survival check:

```json
{"outcome":"spawn-failed","detail":"pane-died-after-spawn","sessionName":"kyle","paneId":"%42","windowId":"@7","ownerSession":"kyle","alive":false}
```

Codex posture (approval policy, sandbox mode) lives in the `tcm-delegate`
codex profile (`~/.codex/tcm-delegate.config.toml`), not in any flag here —
it is Kyle's own persistent config, applied uniformly to every spawn and
follow-up.

Ownership is HORIZONTAL: the delegate spawns as a tmux WINDOW (native tab) in
the launching session, auto-detected from `$TMUX` (override with `-owner
<name>`). The delegate's durable handle is the returned `delegationId` — a
server-persisted record that survives server restarts and, once the delegate's
first turn binds it to a thread, pane death too. Prefer it for wait/followup/
result. The `sessionName` + `paneId` PAIR addresses the transient pane
attachment — `sessionName` is the LAUNCHING session (shared with Kyle's
own claude-code row and any sibling delegates), so when using it always thread
`paneId` through. NEVER kill-session; closing the owner session reaps its
delegate windows as a side effect, not a thing you do on purpose.

## 2. Watch it to a terminal outcome

Run `tcm wait` in the background — it long-polls and can take up to the
full budget:

```sh
"$TCM" wait -delegation <delegationId> -minutes 20
```

(`-session kyle -pane %42` addresses the delegate by pane attachment. With
`-delegation`, the wait keys off the durable record: it tolerates the first
identity-less seconds after spawn and, once bound, survives pane death —
termination is judged by thread.)

Use Bash `run_in_background`, then read stdout once the process exits.
Output:

```json
{"outcome":"finished","polls":3,"state":"done"}
```

Outcomes: `finished`, `waiting` (pane sitting at an approval/input prompt),
`error`, `session-dead` (pane or session gone), `timeout` (budget exceeded
with no terminal state). None of these are a broken CLI — a nonzero exit
from `tcm wait` means a usage error, not an "error" outcome.

## 3. Read the result

```sh
"$TCM" result -delegation <delegationId>
```

(or `-session kyle -pane %42`, or `-thread <uuid>`.) Prints the server's
`/result` JSON verbatim, including `finalMessage`, `rolloutPath`, `threadId`,
and `status`. By delegation or thread id the answer comes from the durable
record and the rollout on disk, so it works after a server restart and after
the pane is gone. `finalMessage` is a CLAIM: verify it
per MO-005 (tests/lint/diff scope) before reporting done. If it contains a
QUESTION, the delegate is blocked awaiting an answer — surface it to Kyle,
answer via follow-up (§4).

## 4. Follow-ups go to the SAME session and pane

The pane is the thread; a second pane for a follow-up is the double-spend
(MO-005). Write the message to a file (multiline-safe), then:

```sh
"$TCM" followup -delegation <delegationId> -message-file /abs/msgfile.md
```

(or `-session kyle -pane %42`.) Delivery needs the delegate's live
pane: a dead pane answers 409 naming the pane rather than respawning
anywhere else.

Output:

```json
{"outcome":"delivered","httpCode":"200","receipts":1,"messageFile":"...","rolloutPath":"..."}
```

The server pins the thread's rollout by tracked threadId, refuses to
interrupt a running or prompt-waiting delegate with 409, and revalidates
before resuming. `tcm followup` posts, then greps the rollout for a delivery
receipt at 5s and 25s, retries the POST once if neither grep found it, and
grep again 30s later — that ladder is why the call can take close to a
minute. After it returns `delivered`, run `tcm wait` again to watch the
follow-up turn and `tcm result` to read its answer.

## Failure modes

- `spawn-failed` / `no-tmux-owner` — no `$TMUX` to detect an owner session;
  use /codex:rescue instead of retrying.
- `refused-409` — the delegate is mid-run or at a prompt; the server
  declined to interrupt live work. `detail` carries the reason. Wait or
  answer the prompt; the pane is untouched.
- `session-dead` — the pane or its tmux session is gone. `tcm result` may
  still return the final message (it reads the thread's rollout, which
  outlives the pane) — report honestly, never silently respawn.
- `delivery-unverified` — the follow-up POST was accepted but no receipt
  ever showed up in the rollout, even after the built-in retry. Report to
  Kyle; don't re-send blind.
- Codex directory trust: a delegate spawned in a directory codex has not
  trusted stalls at an interactive trust prompt, and `tcm wait` reports
  `waiting` or an `idle`-after-spawn timeout rather than a distinct
  "blocked" state — because the turn never actually started. Delegating
  into a worktree of an already-trusted repo avoids this; a genuinely fresh,
  non-worktree directory is the case that hits it.

## Cleanup — only with Kyle's say-so

Leave the window open when the task completes. On his go:

- Bulk sweep (the usual case): use the `tidy-delegates` skill — it runs
  `tcm tidy` and closes finished delegate windows deterministically
  (spawn-signature + terminal TCM status), session-scoped by default,
  preview-before-close.
- Single horizontal delegate: `tmux kill-window -t '<windowId>'` (the
  returned `@NN`). NEVER kill-session — `sessionName` is Kyle's OWN session
  under horizontal ownership.
- Then remove the worktree if one was created:
  `git worktree remove <path> && git branch -d <branch>`. Worktrees are
  created by the orchestrator, so the orchestrator sweeps them — TCM never
  touches git. Crash path (owner session died with delegates in flight): the
  windows are already reaped; run `git worktree list` in the repo and remove
  the stale delegate worktrees, then `git worktree prune`.
