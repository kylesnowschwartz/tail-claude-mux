---
name: tcm-delegate-codex
description: >
  Delegate a coding task to a visible Codex agent running in a tmux pane via
  TCM, driven end-to-end by workflow scripts (spawn → watch → result as one
  Workflow call), instead of a backgrounded /codex:rescue job. Use for
  implementation work that changes the tree (MO-002 routing: repo-gets-changes
  → visible pane), when Kyle asks to "watch" a delegation, or for parallel
  worktree delegations. NOT for read-only investigation/review/second opinions
  whose output Fable consumes directly — use /codex:rescue for those. Headless
  or tmux-less contexts: fall back to /codex:rescue.
---

# tcm-delegate-codex — visible Codex delegation via TCM workflows

The delegation seams (watching, follow-up delivery, result reading) are
deterministic workflow scripts, not prose steps. A/B-validated 2026-07-10
(3-0 pairwise vs the manual protocol; see
`tail-claude-mux/.agent-history/PLAN-delegation-workflows-experiment.md`).

The workflows ship bundled with this plugin under
`${CLAUDE_PLUGIN_ROOT}/workflows/` (`tcm-delegate.js`, `tcm-watch.js`, plus the
`lib/` shell scripts they run). Invoke by absolute scriptPath — they work from
any project cwd. The Workflow JS sandbox cannot read its own install path, so
every invocation MUST pass the bundled paths in as args: `libDir` (the
`${CLAUDE_PLUGIN_ROOT}/workflows/lib` dir) and, for delegate, `watchScriptPath`
(the `${CLAUDE_PLUGIN_ROOT}/workflows/tcm-watch.js` file). Codex delegates only;
for claude/pi delegates or when the Workflow tool is unavailable, fall back to
/codex:rescue.

## Preconditions (check once, cheaply)

```sh
curl -fsS localhost:7391/state >/dev/null
```

Fails → TCM isn't running; fall back to /codex:rescue and say why.

## 1. Delegate (spawn → watch → result, one call)

For parallel or long work, create the git worktree FIRST (MO-002) and pass
its path — TCM does no git writes. Codex trust follows the worktree's MAIN
repository: a worktree of a trusted repo is trusted anywhere on disk
(spike-verified 2026-07-11). Only a genuinely fresh non-worktree dir hits
the interactive trust prompt and stalls the run as `waiting`.

Workflow tool, scriptPath
`${CLAUDE_PLUGIN_ROOT}/workflows/tcm-delegate.js`, args (`watchMinutes`/`pollSeconds`
optional, default 20/30):

```json
{"dir": "/abs/worktree", "name": "kebab-window-name", "brief": "<context-complete per MO-002>",
 "watchMinutes": 20, "pollSeconds": 30,
 "libDir": "${CLAUDE_PLUGIN_ROOT}/workflows/lib",
 "watchScriptPath": "${CLAUDE_PLUGIN_ROOT}/workflows/tcm-watch.js"}
```

`${CLAUDE_PLUGIN_ROOT}` must reach the Workflow args as a LITERAL absolute path,
not the placeholder text: the Workflow JS sandbox and the sub-agent shell that
runs the lib scripts do NOT expand it. The plugin loader expands it in this
skill body, so the value you read here is already absolute — pass that through.
If you ever see the raw `${CLAUDE_PLUGIN_ROOT}` string, resolve it first
(`echo "$CLAUDE_PLUGIN_ROOT"`); the workflow rejects an unresolved or relative
`libDir`/`watchScriptPath` with a clear error rather than failing downstream.

Posture lives in a named codex profile, NOT a per-spawn CLI flag (reworked
2026-07-13). The spawn leg launches `codex --profile tcm-delegate`; the
profile is a standalone file `~/.codex/tcm-delegate.config.toml` with BARE
keys (`approval_policy = "never"`, `sandbox_mode = "danger-full-access"`) —
NOT a `[profiles.tcm-delegate]` table, which codex 0.134+ hard-errors on. No
`--dangerously-bypass-approvals-and-sandbox` on the command line — that flag
literal in a sub-agent prompt trips Claude's spawn-time safety classifier and
blocks routine delegation. The profile is Kyle's own persistent config, so
the bypass is a self-authored decision, not something the workflow injects.
The full-bypass posture (`sandbox_mode = "danger-full-access"`) is required
only for autonomous `git commit`: codex keeps `.git` read-only under
workspace-write even in trusted worktrees (Codex-verified 2026-07-13). The
old "nested codex spawns need bypass" rationale was an environment-specific
fluke, not a real constraint — do not reinstate it. Containment is the
worktree diff plus the visible pane. Follow-ups inherit the profile (MO-008).

Ownership is HORIZONTAL (SPEC-5, shipped 2026-07-12): the delegate spawns
as a tmux WINDOW (native tab) in the launching session — the spawn leg
auto-discovers the owner via `tmux display-message`; pass
`"ownerSession": "<name>"` to override. No tmux context → legacy detached
session, unchanged. Closing the owner session reaps its delegate windows.

End the brief with: `When complete, print exactly one line starting with
RESULT:, then stop.` — the result leg keys off the final message.

Returns `{outcome, sessionName, paneId, windowId, ownerSession, resultSummary, watch}`:

- `finished` — resultSummary carries the delegate's final message. It is a
  CLAIM: verify per MO-005 (tests/lint/diff scope) before reporting done.
  If it contains a QUESTION, the delegate is blocked awaiting an answer —
  surface it to Kyle, answer via follow-up (§3).
- `waiting` — pane at an approval/trust/input prompt. resultSummary is
  still attempted (GET /result never blocks): populated means the pane
  actually finished and idles at the input prompt (known /wait misread);
  empty means genuinely blocked — tell Kyle which session.
- `session-dead` / `timeout` — resultSummary may still be populated (the
  result leg reads the server's GET /result, which answers from the
  thread's rollout and outlives the pane). Report honestly; never
  silently respawn.
- `spawn-failed` — report; check the server, don't retry blind.
- The delegate's handle is the RETURNED `sessionName` + `paneId` PAIR:
  under horizontal ownership `sessionName` is the LAUNCHING session (shared
  with Kyle's own claude-code row and any sibling delegates), so
  session-only reads on it return 400 "specify pane" — always thread
  `paneId` through watch/follow-up/result (the workflows do this
  automatically). Window names dedupe with `-2` suffixes within the owner.
  The window is Kyle's view into the work — native tab + dashboard row +
  jump target (MO-007).

## 2. Watch only (delegate already running)

scriptPath `${CLAUDE_PLUGIN_ROOT}/workflows/tcm-watch.js`, args
`{"session": "<name>", "pane": "%NN", "watchMinutes": 20, "pollSeconds": 30, "libDir": "${CLAUDE_PLUGIN_ROOT}/workflows/lib"}`.
Detection is `GET /wait` long-poll primary (the server reconciles hook
status against rollout evidence, so done/error/interrupted are
trustworthy); pane quiescence is the in-leg fallback when the server is
down or doesn't track the session. The `pane` arg is forwarded to the
server for disambiguation — required whenever the session hosts more than
one agent (every horizontal delegation). For a single quick check you may
skip the workflow and run one background
`curl -fsS -G localhost:7391/wait --data-urlencode 'session=<name>' --data-urlencode 'pane=%NN' --data-urlencode 'timeout=570'`
yourself (`--data-urlencode` is mandatory — pane ids start with `%`) — but
never hand-roll a polling loop in the orchestrator (MO-001).

## 3. Follow-ups go to the SAME thread (one Workflow call)

The pane session is the thread; a second pane for a follow-up is the
double-spend (MO-005). The resume re-selects the same `--profile
tcm-delegate` the spawn used, so posture is uniform across spawn and
follow-up (MO-008) rather than relying on implicit inheritance. The server
owns the resume-respawn: it pins the
thread's rollout by tracked threadId, refuses `running`/`waiting` with
409 (never kills live work), revalidates before respawning, and returns
receipts.

Write the follow-up message to a file (multiline-safe), then ONE Workflow
call — scriptPath `${CLAUDE_PLUGIN_ROOT}/workflows/tcm-watch.js`, args:

```json
{"session": "<name>", "pane": "%NN", "sourceMessageFile": "/abs/msgfile.md",
 "libDir": "${CLAUDE_PLUGIN_ROOT}/workflows/lib"}
```

The workflow delivers (POST /followup), verifies the receipt (the server's
returned `messageFile` copy landing in `rolloutPath`, one POST retry
built in), watches the follow-up run, and reads the result — no inline
jq/curl/grep. Interpret the return:

- `delivered` never surfaces alone — delivery falls through to the watch,
  so the resolution is the watch outcome (§1 semantics) plus
  `delivery: {receipt_count, message_file, rollout_path}` and
  `resultSummary` carrying the follow-up turn's answer (verify per MO-005).
- `refused-409` — delegate mid-run or at a prompt; the server refused
  rather than interrupt. `detail` carries its reason. Wait or answer the
  prompt, don't force; no watch was performed and the pane is untouched.
- `delivery-unverified` — POST accepted but the receipt grep never
  confirmed (even after the built-in retry). Report honestly to Kyle;
  don't re-send blind.

This is the ONLY follow-up path. Server down or the workflow returning
`error`: restart the server (`just restart` in the TCM repo) and retry
once; still broken → report to Kyle (MO-009) — never hand-roll a
resume-respawn around the seam.

Degraded path (Workflow tool unavailable ONLY): deliver by hand with
`jq -n --arg p '%NN' --rawfile m /abs/msgfile.md '{session:"<name>", pane:$p, message:$m}' | curl -fsS -X POST localhost:7391/followup -H 'Content-Type: application/json' -d @-`,
verify with `grep -c "<returned messageFile>" "<rolloutPath>"` (retry the
POST once if 0 after ~30s), then watch per §2.

## 4. Cleanup — only with Kyle's say-so

Leave the window open when the task completes. On his go:

- Bulk sweep (the usual case): use the `tidy-delegates` skill — it closes
  finished delegate windows deterministically (spawn-signature + terminal
  TCM status), session-scoped by default, preview-before-close. Prefer it
  over hand-rolled kills.
- Single horizontal delegate: `tmux kill-window -t '<windowId>'` (the
  returned `@NN`). NEVER kill-session — `sessionName` is Kyle's OWN session
  under horizontal ownership.
- Legacy detached delegate (no owner): `tmux kill-session -t '=<sessionName>'`
  (quote the `=`).
- Then remove the worktree if one was created:
  `git worktree remove <path> && git branch -d <branch>`. Worktrees are
  created by the orchestrator, so the orchestrator sweeps them — TCM never
  touches git. Crash path (owner session died with delegates in flight):
  the windows are already reaped; run `git worktree list` in the repo and
  remove the stale delegate worktrees, then `git worktree prune`.
