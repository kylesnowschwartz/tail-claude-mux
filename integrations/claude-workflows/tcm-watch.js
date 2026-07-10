// tcm-watch — deterministic delegation watcher (experiment arm B1).
//
// Lifts the tcm-status-watcher agent's protocol into Workflow JS: the
// detection SIGNAL is unchanged (pane quiescence primary, TCM /state
// advisory — /state may report "idle" during active codex work), but the
// seams move from prose into code: leg retry, leg cap, and quiescence
// state carried across legs deterministically.
//
// Pacing constraint: Workflow JS has no sleep and bans clocks, so cadence
// lives INSIDE each haiku leg (bash script with in-script sleep and a
// self-deadline under the 600s Bash cap). The JS loop is a retry wrapper,
// not the cadence source.
//
// Invoke via scriptPath with args:
//   { session: "tcm-session-name", pane: "%42" (optional),
//     watchMinutes: 20 (optional), pollSeconds: 30 (optional) }
// Returns:
//   { resolution: finished|waiting|error|session-dead|unverified|timeout,
//     state, detail, session, pane, polls_total, legs, leg_deaths }

export const meta = {
  name: 'tcm-watch',
  description: 'Watch a TCM-tracked delegate tmux session until terminal state (quiescence-primary poll legs)',
  phases: [{ title: 'Watch', detail: 'haiku poll legs, cadence in-leg' }],
}

const LEG_SECONDS = 480 // in-script self-deadline; Bash hard cap is 600s

const LEG_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['resolution', 'polls', 'quiescence_count', 'last_pane_hash', 'last_state', 'resolved_pane', 'evidence'],
  properties: {
    resolution: { type: 'string', enum: ['finished', 'waiting', 'error', 'session-dead', 'unverified', 'continue'] },
    polls: { type: 'integer', minimum: 0 },
    quiescence_count: { type: 'integer', minimum: 0 },
    last_pane_hash: { type: 'string' },
    last_state: { type: 'string' },
    resolved_pane: { type: 'string' },
    evidence: { type: 'string' },
  },
}

function legScript(p) {
  // Concrete values are baked in HERE, by code — the leg transcribes, it does
  // not compose tmux commands (a haiku leg mangled '%334' into an invalid
  // 'session:%334' target when given a placeholder; never again).
  return `#!/bin/bash
PANE='${p.pane}'
SESH='${p.session}'
INTERVAL=${p.pollSeconds}
SELF_DEADLINE=$((SECONDS+${LEG_SECONDS}))
POLLS=0
QCOUNT=${p.seedCount}
LAST_HASH='${p.seedHash || 'none'}'
LAST_ST='none'
UNREADABLE=0
while true; do
  if (( SECONDS >= SELF_DEADLINE )); then echo "RESOLUTION=continue POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$LAST_ST NOTE=deadline"; exit 0; fi
  if ! tmux has-session -t "=$SESH" 2>/dev/null; then echo "RESOLUTION=session-dead POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$LAST_ST NOTE=session-gone"; exit 0; fi
  st=$(curl -fsS localhost:7391/state 2>/dev/null | jq -r --arg s "$SESH" '[.. | objects | select(.session? == $s) | .status] | first // empty' 2>/dev/null)
  [ -n "$st" ] && LAST_ST="$st"
  if [ "$st" = "error" ]; then echo "RESOLUTION=error POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=state-error"; exit 0; fi
  if [ "$st" = "running" ]; then
    QCOUNT=0
  else
    content=$(tmux capture-pane -p -t "$PANE" 2>/dev/null)
    if [ -z "$content" ]; then
      UNREADABLE=$((UNREADABLE+1))
      if (( UNREADABLE >= 3 )); then echo "RESOLUTION=unverified POLLS=$POLLS QCOUNT=$QCOUNT HASH=none STATE=$LAST_ST NOTE=pane-unreadable"; exit 0; fi
    else
      UNREADABLE=0
      h=$(printf '%s' "$content" | md5 -q)
      if [ "$h" = "$LAST_HASH" ]; then
        QCOUNT=$((QCOUNT+1))
        if (( QCOUNT >= 3 )); then
          tl=$(printf '%s' "$content" | grep -v '^[[:space:]]*$' | tail -4)
          if printf '%s' "$tl" | grep -qiE 'press enter|y/n|approve|allow|permission|continue\\?|› *[0-9]\\.'; then
            echo "RESOLUTION=waiting POLLS=$POLLS QCOUNT=$QCOUNT HASH=$h STATE=$LAST_ST NOTE=quiescent-at-prompt"
          else
            echo "RESOLUTION=finished POLLS=$POLLS QCOUNT=$QCOUNT HASH=$h STATE=$LAST_ST NOTE=pane-quiescent"
          fi
          exit 0
        fi
      else
        QCOUNT=0
        LAST_HASH="$h"
      fi
    fi
  fi
  POLLS=$((POLLS+1))
  sleep $INTERVAL
done`
}

function legPrompt(p) {
  const paneStep = p.pane
    ? ''
    : `\nFIRST resolve the pane id with one command: tmux list-panes -s -t "=${p.session}" -F '#{pane_id}' (a spawned delegate session has one pane). Then set the script's PANE= line to that value (e.g. PANE='%42') — that is the ONLY edit you may make.\n`
  return `You are one poll leg of a delegation watcher for tmux session "${p.session}". You run one pre-written poll script and report its result as structured output. You never summarize the delegate's work, never fix anything, and never compose your own tmux commands.
${paneStep}
STEP 1 — Write the following script VERBATIM (byte-for-byte${p.pane ? ', no edits' : ', except the PANE= line as instructed above'}) to a file in your scratchpad directory named tcm-watch-leg.sh:

${legScript(p)}

STEP 2 — Run it with ONE Bash call: bash <scratchpad>/tcm-watch-leg.sh with timeout 600000. It self-limits to ${LEG_SECONDS}s and always prints exactly one RESOLUTION= line. (Standalone sleep is blocked in your harness; the in-script sleep is fine. Do not inline the loop in the shell — the outer shell mangles multi-line commands.)

STEP 3 — Map the printed line to StructuredOutput: resolution=RESOLUTION, polls=POLLS, quiescence_count=QCOUNT, last_pane_hash=HASH, last_state=STATE, resolved_pane=the PANE value used, evidence=NOTE.

WHY THE SCRIPT IS SHAPED THIS WAY (do not "improve" it): TCM /state reports "idle" during active codex work, so /state is advisory — pane quiescence (3 consecutive identical capture hashes while not "running") is the primary completion signal. The seed QCOUNT/LAST_HASH continue the previous leg's quiescence streak.

HARD RULES
- Write ONLY inside your scratchpad directory — never ~/.claude, ~/.agents, or any project tree.
- Never interact with the delegate: no send-keys, no kill-session, no POSTs to TCM.
- Never print pane content; the script reports hashes and one NOTE clause only.
- If the script dies or emits no RESOLUTION line, return resolution "continue" with the failure described in evidence. Ending without structured output is a failed leg.`
}

let cfg = args || {}
if (typeof cfg === 'string') {
  try { cfg = JSON.parse(cfg) } catch (e) { cfg = {} }
}
if (!cfg.session) {
  return { resolution: 'error', detail: 'args.session is required (TCM session name to watch)', polls_total: 0, legs: 0, leg_deaths: 0 }
}

const session = cfg.session
const pollSeconds = cfg.pollSeconds || 30
const watchMinutes = cfg.watchMinutes || 20
const maxLegs = Math.max(1, Math.ceil((watchMinutes * 60) / LEG_SECONDS))

let pane = cfg.pane || ''
let seedHash = ''
let seedCount = 0
let pollsTotal = 0
let lastState = 'none'
let legDeaths = 0

for (let i = 1; i <= maxLegs; i++) {
  log(`tcm-watch "${session}": leg ${i}/${maxLegs} (polls so far: ${pollsTotal}, leg deaths: ${legDeaths})`)
  const r = await agent(
    legPrompt({ session, pane, pollSeconds, seedHash, seedCount }),
    { label: `watch-leg-${i}:${session}`, phase: 'Watch', model: 'haiku', effort: 'low', schema: LEG_SCHEMA }
  )
  if (!r) {
    // leg died at the harness level — a raw seam event; retry costs one leg slot
    legDeaths++
    continue
  }
  pollsTotal += r.polls
  if (r.last_state) lastState = r.last_state
  if (r.resolved_pane) pane = r.resolved_pane
  if (r.resolution === 'continue') {
    seedHash = r.last_pane_hash || seedHash
    seedCount = r.quiescence_count || 0
    continue
  }
  return {
    resolution: r.resolution, state: lastState, detail: r.evidence,
    session, pane, polls_total: pollsTotal, legs: i, leg_deaths: legDeaths,
  }
}

return {
  resolution: 'timeout', state: lastState,
  detail: `watch budget (${watchMinutes}m, ${maxLegs} legs) exhausted without a terminal state`,
  session, pane, polls_total: pollsTotal, legs: maxLegs, leg_deaths: legDeaths,
}
