// tcm-watch — deterministic delegation watcher.
//
// Detection is GET /wait primary (the server long-polls its own tracker,
// which since b92897d reconciles codex hook status against rollout
// evidence, so `done` is trustworthy). Pane quiescence (3 consecutive
// identical capture hashes while not "running") remains as the in-script
// fallback whenever /wait is unusable: server down, pre-B2 binary (its
// mux answers every path with the root banner), or a session the server
// doesn't track. Each loop iteration re-tries /wait first, so the leg
// self-heals when the server comes back.
//
// Pacing constraint: Workflow JS has no sleep and bans clocks, so cadence
// lives INSIDE each haiku leg (bash script with in-script sleep/blocking
// curl and a self-deadline under the 600s Bash cap). The JS loop is a
// retry wrapper, not the cadence source.
//
// Invoke via scriptPath with args:
//   { session: "tcm-session-name", pane: "%42" (optional),
//     watchMinutes: 20 (optional), pollSeconds: 30 (optional),
//     sourceMessageFile: "/abs/follow-up.md" (optional) }
// When sourceMessageFile is present, a delivery leg posts and receipt-verifies
// the follow-up before watching, then a result leg reads the final message.
// Returns:
//   { resolution: finished|waiting|error|session-dead|unverified|timeout|refused-409|delivery-unverified,
//     state, detail, session, pane, polls_total, legs, leg_deaths,
//     delivery?: { receipt_count, message_file, rollout_path }, resultSummary? }

export const meta = {
  name: 'tcm-watch',
  description: 'Watch a TCM-tracked delegate tmux session until terminal state (/wait long-poll primary, pane-quiescence fallback)',
  phases: [
    { title: 'Deliver', detail: 'POST /followup + rollout receipt verification' },
    { title: 'Watch', detail: 'haiku legs blocking on GET /wait' },
    { title: 'Result', detail: 'final assistant message from the rollout' },
  ],
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

const DELIVERY_SCHEMA = {
  type: 'object',
  additionalProperties: false,
  required: ['resolution', 'http_code', 'receipt_count', 'message_file', 'rollout_path', 'reason', 'evidence'],
  properties: {
    resolution: { type: 'string', enum: ['delivered', 'refused-409', 'delivery-unverified', 'error'] },
    http_code: { type: 'string' },
    receipt_count: { type: 'integer', minimum: 0 },
    message_file: { type: 'string' },
    rollout_path: { type: 'string' },
    reason: { type: 'string' },
    evidence: { type: 'string' },
  },
}

const RESULT_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['summary', 'rollout_path', 'evidence'],
  properties: {
    summary: { type: 'string' },
    rollout_path: { type: 'string' },
    evidence: { type: 'string' },
  },
}

function deliveryScript(p) {
  // The first POST's 409 is a refusal. A retry 409 is not: the first POST
  // already respawned the pane, so that conflict suggests the resume took;
  // only the final receipt grep classifies the retry path.
  return `#!/bin/bash
SESH='${p.session}'
PANE='${p.pane}'
SOURCE='${p.sourceMessageFile}'
HTTP_CODE=''
RECEIPTS=0
MSGFILE=''
ROLLOUT=''
REASON=''
EVIDENCE=''
post_followup() {
  if [ -n "$PANE" ]; then
    jq -n --arg s "$SESH" --arg p "$PANE" --rawfile m "$SOURCE" '{session:$s, message:$m, pane:$p}' | curl -sS -w '\\n%{http_code}' -X POST localhost:7391/followup -H 'Content-Type: application/json' -d @-
  else
    jq -n --arg s "$SESH" --rawfile m "$SOURCE" '{session:$s, message:$m}' | curl -sS -w '\\n%{http_code}' -X POST localhost:7391/followup -H 'Content-Type: application/json' -d @-
  fi
}
RESP=$(post_followup 2>&1)
POST_STATUS=$?
if [ "$POST_STATUS" -ne 0 ] || [ -z "$RESP" ]; then
  REASON='transport failure'
  EVIDENCE="transport-failure: \${RESP:-empty response}"
  printf 'RESOLUTION=error HTTP_CODE=%q RECEIPTS=0 MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\\n' "$HTTP_CODE" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
  exit 0
fi
HTTP_CODE=\${RESP##*$'\\n'}
BODY=\${RESP%$'\\n'*}
if [ "$HTTP_CODE" = '409' ]; then
  REASON=$(printf '%s' "$BODY" | jq -r '.error // "follow-up refused"' 2>/dev/null)
  EVIDENCE="first-post-409: $REASON"
  printf 'RESOLUTION=refused-409 HTTP_CODE=%q RECEIPTS=0 MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\\n' "$HTTP_CODE" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
  exit 0
fi
if [ "$HTTP_CODE" != '200' ]; then
  EXCERPT=$(printf '%s' "$BODY" | tr '\n' ' ' | cut -c1-240)
  REASON="HTTP $HTTP_CODE"
  EVIDENCE="unexpected-http-$HTTP_CODE: $EXCERPT"
  printf 'RESOLUTION=error HTTP_CODE=%q RECEIPTS=0 MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\\n' "$HTTP_CODE" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
  exit 0
fi
MSGFILE=$(printf '%s' "$BODY" | jq -r '.messageFile // empty')
ROLLOUT=$(printf '%s' "$BODY" | jq -r '.rolloutPath // empty')
sleep 5
RECEIPTS=$(grep -c "$MSGFILE" "$ROLLOUT" 2>/dev/null || true)
RECEIPTS=\${RECEIPTS:-0}
if [ "$RECEIPTS" -eq 0 ]; then
  sleep 25
  RECEIPTS=$(grep -c "$MSGFILE" "$ROLLOUT" 2>/dev/null || true)
  RECEIPTS=\${RECEIPTS:-0}
fi
if [ "$RECEIPTS" -eq 0 ]; then
  RETRY=$(post_followup 2>&1)
  RETRY_STATUS=$?
  if [ "$RETRY_STATUS" -eq 0 ] && [ -n "$RETRY" ]; then
    HTTP_CODE=\${RETRY##*$'\\n'}
    RETRY_BODY=\${RETRY%$'\\n'*}
    if [ "$HTTP_CODE" = '200' ]; then
      MSGFILE=$(printf '%s' "$RETRY_BODY" | jq -r '.messageFile // empty')
      ROLLOUT=$(printf '%s' "$RETRY_BODY" | jq -r '.rolloutPath // empty')
    fi
  else
    HTTP_CODE='transport-error'
  fi
  sleep 30
  RECEIPTS=$(grep -c "$MSGFILE" "$ROLLOUT" 2>/dev/null || true)
  RECEIPTS=\${RECEIPTS:-0}
fi
if [ "$RECEIPTS" -gt 0 ]; then
  RESOLUTION='delivered'
  REASON='receipt found'
  EVIDENCE="rollout-receipts=$RECEIPTS"
else
  RESOLUTION='delivery-unverified'
  REASON='no rollout receipt after retry'
  EVIDENCE="final-receipt-grep-zero; retry-http=$HTTP_CODE"
fi
printf 'RESOLUTION=%s HTTP_CODE=%q RECEIPTS=%s MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\\n' "$RESOLUTION" "$HTTP_CODE" "$RECEIPTS" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"`
}

function deliveryPrompt(p) {
  return `You are the delivery leg for follow-up session "${p.session}". You run one pre-written delivery script and report its result as structured output. You never compose your own tmux, curl, grep, or retry commands.

STEP 1 — Write the following script VERBATIM (byte-for-byte, no edits) to a file in your scratchpad directory named tcm-deliver-leg.sh:

${deliveryScript(p)}

STEP 2 — Run it with ONE Bash call: bash <scratchpad>/tcm-deliver-leg.sh with timeout 120000. It always prints exactly one RESOLUTION= line.

STEP 3 — Map the printed line to StructuredOutput: resolution=RESOLUTION, http_code=HTTP_CODE, receipt_count=RECEIPTS, message_file=MSGFILE, rollout_path=ROLLOUT, reason=REASON, evidence=EVIDENCE.

WHY THE SCRIPT IS SHAPED THIS WAY (do not "improve" it): the server writes its own copy of the source message and returns that path as messageFile. Receipt verification must grep for that returned path in rolloutPath. The first 409 is a refusal and stops delivery; a retry 409 can mean the first resume took, so the final grep remains authoritative.

HARD RULES
- Write ONLY inside your scratchpad directory — never ~/.claude, ~/.agents, or any project tree.
- Transcribe the script verbatim. Do not compose or run any other tmux, curl, grep, or retry command.
- Never send-keys or kill anything. The POST calls already present in the script are the only state-changing calls allowed.
- Return structured output even when the script fails or emits no RESOLUTION line; use resolution "error" and describe the failure in evidence.`
}

function resultPrompt(p) {
  const command = p.pane
    ? `curl -fsS -G 'localhost:7391/result' --data-urlencode 'session=${p.session}' --data-urlencode 'pane=${p.pane}'`
    : `curl -fsS 'localhost:7391/result?session=${encodeURIComponent(p.session)}'`
  return `Run this single command exactly once: ${command}

Map the JSON response to StructuredOutput: summary=finalMessage, rollout_path=rolloutPath, evidence=identification+"; status="+status. If curl fails, including HTTP 404, return summary="", rollout_path="", and put the curl failure in evidence. Do not read or scan rollout files. Do not run any other command.`
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
wait_status() {
  # one long-poll; prints "STATUS TIMEDOUT" iff the response is real /wait JSON
  local t=$1 resp
  if [ -n "$PANE" ]; then
    resp=$(curl -fsS -G 'localhost:7391/wait' --data-urlencode "session=$SESH" --data-urlencode "timeout=$t" --data-urlencode "pane=$PANE" 2>/dev/null) || return 1
  else
    resp=$(curl -fsS "localhost:7391/wait?session=$SESH&timeout=$t" 2>/dev/null) || return 1
  fi
  printf '%s' "$resp" | jq -er '"\\(.status) \\(.timedOut)"' 2>/dev/null
}
while true; do
  if (( SECONDS >= SELF_DEADLINE )); then echo "RESOLUTION=continue POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$LAST_ST NOTE=deadline"; exit 0; fi
  if ! tmux has-session -t "=$SESH" 2>/dev/null; then echo "RESOLUTION=session-dead POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$LAST_ST NOTE=session-gone"; exit 0; fi
  REMAIN=$((SELF_DEADLINE-SECONDS)); (( REMAIN > 540 )) && REMAIN=540
  if (( REMAIN >= 5 )) && wr=$(wait_status "$REMAIN"); then
    POLLS=$((POLLS+1))
    st=\${wr%% *}; to=\${wr##* }
    LAST_ST="$st"
    case "$st" in
      done) echo "RESOLUTION=finished POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-done"; exit 0;;
      error) echo "RESOLUTION=error POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-error"; exit 0;;
      interrupted) echo "RESOLUTION=error POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-interrupted"; exit 0;;
      gone) echo "RESOLUTION=session-dead POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=$st NOTE=wait-gone"; exit 0;;
      waiting)
        if [ "$to" = "true" ]; then continue; fi
        # observed transient: an approval prompt can flash and self-resolve;
        # confirm it holds for 5s before reporting
        sleep 5
        if wr2=$(wait_status 5); then
          st2=\${wr2%% *}
          if [ "$st2" = "waiting" ]; then echo "RESOLUTION=waiting POLLS=$POLLS QCOUNT=$QCOUNT HASH=$LAST_HASH STATE=waiting NOTE=wait-waiting-confirmed"; exit 0; fi
          LAST_ST="$st2"
        fi
        continue;;
      *) continue;;
    esac
  fi
  # fallback: /wait unusable (server down, pre-B2 binary, untracked session)
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

STEP 2 — Run it with ONE Bash call: bash <scratchpad>/tcm-watch-leg.sh with timeout 600000. It self-limits to ${LEG_SECONDS}s and always prints exactly one RESOLUTION= line. It spends most of its time blocked on a long-poll curl to the local TCM server; that is by design. (Standalone sleep is blocked in your harness; the in-script sleep/curl is fine. Do not inline the loop in the shell — the outer shell mangles multi-line commands.)

STEP 3 — Map the printed line to StructuredOutput: resolution=RESOLUTION, polls=POLLS, quiescence_count=QCOUNT, last_pane_hash=HASH, last_state=STATE, resolved_pane=the PANE value used, evidence=NOTE.

WHY THE SCRIPT IS SHAPED THIS WAY (do not "improve" it): GET /wait is the primary signal — the server reconciles hook status against the codex thread's rollout evidence, so its done/error/interrupted are trustworthy and arrive with zero polling. The pane-quiescence branch only runs when /wait is unusable (server down or session untracked), and the seed QCOUNT/LAST_HASH continue the previous leg's quiescence streak. A "waiting" wake is confirmed 5s later before being reported, because approval prompts can flash and self-resolve.

HARD RULES
- Write ONLY inside your scratchpad directory — never ~/.claude, ~/.agents, or any project tree.
- Never interact with the delegate: no send-keys, no kill-session, no state-changing calls to TCM (the read-only GETs in the script are fine).
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
let delivery = null

if (cfg.sourceMessageFile) {
  phase('Deliver')
  let delivered = null
  try {
    delivered = await agent(deliveryPrompt({ session, pane, sourceMessageFile: cfg.sourceMessageFile }), {
      label: `deliver:${session}`, phase: 'Deliver', model: 'haiku', effort: 'low', schema: DELIVERY_SCHEMA,
    })
  } catch (e) {
    log(`tcm-watch "${session}": deliver leg failed (${e && e.message ? e.message : e})`)
  }
  if (!delivered) {
    return {
      resolution: 'error', state: 'none', detail: 'deliver leg died',
      session, pane, polls_total: 0, legs: 0, leg_deaths: 0,
      delivery: { receipt_count: 0, message_file: '', rollout_path: '' },
    }
  }
  // Two-path invariant: the server's returned copy is the receipt target;
  // the caller's source file must never be used as that target.
  if ((delivered.resolution === 'delivered' || delivered.resolution === 'delivery-unverified') && delivered.message_file === cfg.sourceMessageFile) {
    delivered.resolution = 'error'
    delivered.reason = `server message_file ${delivered.message_file} equals caller sourceMessageFile ${cfg.sourceMessageFile}`
    delivered.evidence = delivered.reason
  }
  delivery = {
    receipt_count: delivered.receipt_count,
    message_file: delivered.message_file,
    rollout_path: delivered.rollout_path,
  }
  if (delivered.resolution !== 'delivered') {
    return {
      resolution: delivered.resolution, state: 'none', detail: delivered.reason || delivered.evidence,
      session, pane, polls_total: 0, legs: 0, leg_deaths: 0, delivery,
    }
  }
}

let watchOutcome = null

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
  if (cfg.sourceMessageFile) {
    watchOutcome = {
      resolution: r.resolution, state: lastState, detail: r.evidence,
      session, pane, polls_total: pollsTotal, legs: i, leg_deaths: legDeaths,
      delivery,
    }
    break
  }
  return {
    resolution: r.resolution, state: lastState, detail: r.evidence,
    session, pane, polls_total: pollsTotal, legs: i, leg_deaths: legDeaths,
  }
}

if (!cfg.sourceMessageFile) {
  return {
    resolution: 'timeout', state: lastState,
    detail: `watch budget (${watchMinutes}m, ${maxLegs} legs) exhausted without a terminal state`,
    session, pane, polls_total: pollsTotal, legs: maxLegs, leg_deaths: legDeaths,
  }
}

if (!watchOutcome) {
  watchOutcome = {
    resolution: 'timeout', state: lastState,
    detail: `watch budget (${watchMinutes}m, ${maxLegs} legs) exhausted without a terminal state`,
    session, pane, polls_total: pollsTotal, legs: maxLegs, leg_deaths: legDeaths,
    delivery,
  }
}

const READABLE = watchOutcome.resolution === 'finished' || watchOutcome.resolution === 'session-dead' || watchOutcome.resolution === 'timeout' || watchOutcome.resolution === 'waiting'
if (!READABLE) return watchOutcome

phase('Result')
// Invariant: once the watch reached a readable state, this workflow
// returns that outcome no matter what the result leg does. A schema
// retry-cap inside agent() THROWS — without this catch it voids a
// follow-up whose real work already reached its watch outcome.
let result = null
try {
  result = await agent(resultPrompt({ session, pane }), {
    label: `result:${session}`, phase: 'Result', model: 'haiku', effort: 'low', schema: RESULT_SCHEMA,
  })
} catch (e) {
  log(`tcm-watch: result leg failed (${e && e.message ? e.message : e}); watch outcome preserved`)
}
return { ...watchOutcome, resultSummary: result ? result.summary : '' }
