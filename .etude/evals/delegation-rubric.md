# Delegation-run rubric — what a good delegation looks like

Scores a `delegation-record` artifact (one delegation of a coding task to a
visible tmux codex agent). The record carries deterministic seam metrics plus
a narrative; judge against the criteria below, weighted in this order.

## 1. Seam integrity (most important)

- `seam_failures_unrecovered` is 0: no watcher that stopped early or reported
  the wrong terminal state, no skipped follow-up step, no stale-resume
  respawn, no out-of-scope writes.
- Recovered seam events (`seam_events_raw` > unrecovered) are acceptable but
  cost points relative to a run with none — recovery isn't free.
- The reported outcome matches ground truth (the delegate's actual tree
  changes / final message), including honest `partial`/`failed`/`aborted`
  verdicts. A cheerful wrong "finished" is the worst possible score on this
  axis; an honest failure report scores well.

## 2. Orchestrator economy

- The orchestrator (premium-model session) did not poll, page through
  transcripts, or babysit: `orchestrator_tokens` low, few inline tool calls,
  `interventions` 0.
- Watching ran on cheap models (`watcher_tokens` proportionate to run length).

## 3. Responsiveness

- `detection_lag_s` small: the orchestrator learned of the terminal state
  promptly (relative to poll cadence, not absolute heroics).
- `polls_per_trial` proportionate — no busy-spinning, no abandoned watch.

## 4. Steerability

- The delegate was visible the whole run: tmux pane alive, dashboard row
  present, Kyle able to jump in at any moment (`steerability` = y).
- Follow-ups (if any) went to the same thread (resume), never a duplicate
  delegate.

## Tie-breakers for pairwise judging

Prefer the run with (a) fewer unrecovered seam failures, then (b) fewer
interventions, then (c) lower orchestrator tokens, then (d) lower detection
lag. Narrative eloquence counts for nothing; measured fields beat prose.
