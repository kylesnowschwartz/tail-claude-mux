# Spike 3 — `~/.claude/sessions/*.json` `status` field

**Question:** Is the `status` field worth using as a fallback in TCM when hooks
aren't registered for a Claude Code instance?

**Verdict (TL;DR):** **Wire it up.** Vocabulary is richer than `{busy, absent}`
— at minimum `{busy, idle, waiting, absent}` plus a `(status × updatedAt
freshness)` axis that distinguishes "alive and ticking" from "stuck or stale".

---

## Half 1 — Methodology

### Run the logger

```sh
# self-check (one snapshot, parses files, exits 0/non-zero)
bun run scripts/spike/cc-sessions-status-logger.ts --self-check

# long-running (Ctrl-C to stop)
bun run scripts/spike/cc-sessions-status-logger.ts &
echo $! > /tmp/cc-sessions-logger.pid
# ...do work...
kill "$(cat /tmp/cc-sessions-logger.pid)"
```

The logger polls `~/.claude/sessions/*.json` every 2 s and appends NDJSON to
`/tmp/cc-sessions-trace.ndjson`. Each loop emits:

- one `snap` line per file currently present;
- a `transition: true` line whenever a file's `status` or `updatedAt` changed
  since the previous snapshot;
- an `appeared` line the first time a pid is seen;
- a `vanished` line when a previously-seen pid file is gone.

Fields recorded per line: `ts, pid, sessionId (first 8 chars), status,
updatedAt, agent, cwd`.

### Analyse the trace

```sh
# 1. distinct status values
jq -r '.status' /tmp/cc-sessions-trace.ndjson | sort -u

# 2. cross-tab status vs entrypoint (peek at live files)
for f in ~/.claude/sessions/*.json; do
  jq -r '"\(.pid)\t\(.entrypoint)\t\(.status // "ABSENT")"' "$f"
done

# 3. observed transitions (the meat of it)
jq -r 'select(.transition == true) |
       "\(.ts) pid=\(.pid) \(.prevStatus // "-") -> \(.status // "-")"' \
   /tmp/cc-sessions-trace.ndjson

# 4. file lifecycle events (CC start/stop)
jq -r 'select(.event) |
       "\(.ts) pid=\(.pid) \(.event) status=\(.status // .lastStatus // .initialStatus // "-")"' \
   /tmp/cc-sessions-trace.ndjson

# 5. updatedAt freshness vs now (detect stuck sessions)
NOW=$(($(date +%s) * 1000))
for f in ~/.claude/sessions/*.json; do
  jq -r --arg now "$NOW" \
    '"\(.pid)\t\(.status // "ABSENT")\tage_s=\((($now | tonumber) - .updatedAt) / 1000 | floor)"' \
    "$f"
done | sort -k3
```

---

## Half 2 — Initial findings (5 min sample)

- **Trace window:** `2026-05-16T12:25:05Z` → `2026-05-16T12:30:09Z` (≈ 304 s).
- **Lines emitted:** 2 007 across **14 distinct pids** during the window.
- **Activity during the window:**
  - Multiple `Bash sleep N` tool calls from this very subagent (busy bursts).
  - One fresh `claude -p` spawn-and-exit (pid 4341) → file appeared,
    persisted ~8 s, vanished.
  - A second `claude -p` (pid 5787) inspected mid-flight to capture the raw
    JSON shape.
  - Natural turn boundaries on the team-lead session (29421) during the spike.

### Distinct `status` values

| value | snapshots | seen on |
|---|---|---|
| `idle` | 1 020 | most steady-state CC instances |
| `busy` | 387 | actively processing a turn |
| `waiting` | 235 | blocked on something (see below) |
| *ABSENT* (`null` in trace) | 14 | only the two `entrypoint: "sdk-cli"` print-mode CCs |

So the live vocabulary is **`{busy, idle, waiting, absent}`** — strictly
richer than the `{busy, absent}` we assumed.

### Cross-tab with `entrypoint`

Every interactive `entrypoint: "cli"` file had a populated `status`. Both
short-lived `entrypoint: "sdk-cli"` instances (`claude -p ...`) wrote the
file but **never set `status` or `updatedAt`** during their entire ~8 s
lifecycle. So:

- `entrypoint: "cli"` → status always present, transitions observable.
- `entrypoint: "sdk-cli"` → status always absent; treat like the existing
  pane-scanner "process exists, no detail" inference.

### Observed transitions

Pasted from the trace (`prev -> next`):

```
12:25:19 pid=29421 idle    -> busy     (team-lead received a message)
12:25:39 pid=29421 busy    -> idle     (turn ended)
12:25:41 pid=89519 busy    -> waiting  (this subagent yielded for a tool result)
12:25:47 pid=29421 idle    -> busy     (next message arrived)
```

Real, useful transitions on the parent CC are visible in real time.

### Does status change *during* a turn?

Yes — but not in a high-frequency way. A turn looks like:

```
idle ──(message arrives)──▶ busy ──(work)──▶ idle
            or
busy ──(awaiting tool/subagent/user)──▶ waiting ──▶ busy ──▶ idle
```

I did **not** see status flap busy↔something↔busy *within* a single Bash tool
call — `status` reflects the high-level CC state machine, not per-tool-call
state. It changes on the order of seconds, not milliseconds.

### Freshness of `updatedAt`

- `updatedAt` advances monotonically and ticks **on status changes** (and
  presumably on internal turn events). It is *not* a heartbeat — it stalls
  whenever CC is genuinely idle and whenever CC is hung.
- This produces a useful composite signal: pids `74053` and `79415` both
  showed `status: busy` with `updatedAt` ≥ 25 h old at the end of the spike,
  yet their processes were still alive. That's almost certainly a stuck or
  abandoned turn — distinguishable from a real `busy` by `now - updatedAt`.

### File lifecycle

- File **appears** when CC starts (caught the appearance of pid 4341 and
  5787, both `claude -p` invocations).
- File **disappears** on CC process exit — verified by polling
  `ls ~/.claude/sessions/` before/during/after a `claude -p` invocation
  (count went `13 → 14 → 13`).
- The two `sdk-cli` files lived their entire lifetime with `status: absent`
  and `updatedAt: absent`. They appear and vanish but never tick.
- All 12 long-lived files in the trace correspond to **still-alive** pids
  (verified with `ps -p`). No orphan files were observed during the spike;
  CC cleans up on exit.

---

## Verdict — Wire it up

The `status` field is a real signal worth surfacing in TCM as a hook-less
fallback. Specifically:

### Proposed mapping to `AgentStatus`

| sessions/ snapshot | proposed `AgentStatus` | rationale |
|---|---|---|
| `status: "busy"`, `updatedAt` fresh (< ~5 min) | `Working` | actively processing a turn |
| `status: "busy"`, `updatedAt` stale (> ~10 min) | `Stuck` / `Unknown` | hung or abandoned — distinct from healthy busy |
| `status: "waiting"` | `AwaitingInput` (or `Blocked`) | CC has paused for user / permission / subagent — actionable |
| `status: "idle"` | `Idle` | not processing |
| file present, `status` absent (sdk-cli) | existing pane-scanner default ("process exists, no detail") |
| file absent, process alive in pane | existing pane-scanner default |
| file absent, no process | `Offline` |

The `waiting` state in particular is **new useful signal** that the existing
pane-scanner cannot infer: it reliably distinguishes "CC is computing" from
"CC has stopped and is waiting for *you* to act," which is exactly the kind
of distinction a status HUD wants to surface.

The stale-busy detection (busy + old `updatedAt`) is a free bonus — it
catches the hung-CC failure mode that pure process liveness misses.

### Implementation notes for the wire-up spike

- TCM should poll/watch `~/.claude/sessions/*.json` only when **no hook
  payload has been received** for a given pid (hook is authoritative).
- File is read-cheap (small JSON, fewer than 20 files in practice). A
  2-second poll matches what the logger used and is plenty.
- Source of truth precedence:
  1. Hook events (existing)
  2. `sessions/<pid>.json` `status` (new)
  3. Pane scanner inference (existing)
- Be defensive: `sdk-cli` entrypoints write the file without `status`. Treat
  `status` absent the same as "no signal from this layer."
- Consider gating on `updatedAt` freshness when mapping `busy` → `Working`
  to avoid the stale-busy ambiguity.

### Followup worth doing (out of scope for this spike)

- Run the logger for a full working day to confirm no other status values
  appear (e.g. `error`, `compacting`, `waiting-for-approval`). 5 min is enough
  to recommend wiring up, not enough to enumerate every edge case.
- Check whether subagents have their own `sessions/<pid>.json` or share the
  parent's. The 89519 subagent stuck at `waiting` for the whole spike
  suggests subagent tool calls do **not** flip the parent file's status.
