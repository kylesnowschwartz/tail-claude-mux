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

// Committed, argument-driven shell scripts the delegate agents run verbatim
// instead of transcribing a generated script to scratchpad. Sibling to
// tcm-delegate.js's DEFAULT_WATCH constant (same main-checkout path shape).
const LIB_DIR = '/Users/kyle/Code/my-projects/tail-claude-mux/.claude/workflows/lib'

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

function deliveryPrompt(p) {
  return `You are the delivery leg for follow-up session "${p.session}". You run one pre-written, already-committed delivery script and report its result as structured output. You never compose your own tmux, curl, grep, or retry commands.

STEP 1 — Run this single command exactly (timeout 120000): bash ${LIB_DIR}/tcm-deliver.sh '${p.session}' '${p.pane}' '${p.sourceMessageFile}'
It always prints exactly one RESOLUTION= line.

STEP 2 — Map the printed line to StructuredOutput: resolution=RESOLUTION, http_code=HTTP_CODE, receipt_count=RECEIPTS, message_file=MSGFILE, rollout_path=ROLLOUT, reason=REASON, evidence=EVIDENCE.

WHY THE SCRIPT IS SHAPED THIS WAY (do not "improve" it): the server writes its own copy of the source message and returns that path as messageFile. Receipt verification must grep for that returned path in rolloutPath. The first 409 is a refusal and stops delivery; a retry 409 can mean the first resume took, so the final grep remains authoritative.

HARD RULES
- Write ONLY inside your scratchpad directory — never ~/.claude, ~/.agents, or any project tree.
- Do not edit, copy, or "improve" ${LIB_DIR}/tcm-deliver.sh. Do not compose or run any other tmux, curl, grep, or retry command.
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

function legPrompt(p) {
  // Concrete values are baked in HERE, by code — the leg transcribes, it does
  // not compose tmux commands (a haiku leg mangled '%334' into an invalid
  // 'session:%334' target when given a placeholder; never again).
  const seedHashArg = p.seedHash || 'none'
  const paneArg = p.pane || '<RESOLVED_PANE>'
  const paneStep = p.pane
    ? ''
    : `\nFIRST resolve the pane id with one command: tmux list-panes -s -t "=${p.session}" -F '#{pane_id}' (a spawned delegate session has one pane). Substitute that value for <RESOLVED_PANE> in the command below — that is the ONLY value you determine yourself.\n`
  return `You are one poll leg of a delegation watcher for tmux session "${p.session}". You run one pre-written, already-committed poll script and report its result as structured output. You never summarize the delegate's work, never fix anything, and never compose your own tmux commands.
${paneStep}
STEP 1 — Run this single command exactly (timeout 600000)${p.pane ? '' : ', with <RESOLVED_PANE> replaced by the pane id you resolved above'}: bash ${LIB_DIR}/tcm-watch-leg.sh '${paneArg}' '${p.session}' '${p.pollSeconds}' '${LEG_SECONDS}' '${p.seedCount}' '${seedHashArg}'
It self-limits to ${LEG_SECONDS}s and always prints exactly one RESOLUTION= line. It spends most of its time blocked on a long-poll curl to the local TCM server; that is by design. (Standalone sleep is blocked in your harness; the in-script sleep/curl is fine.)

STEP 2 — Map the printed line to StructuredOutput: resolution=RESOLUTION, polls=POLLS, quiescence_count=QCOUNT, last_pane_hash=HASH, last_state=STATE, resolved_pane=the PANE value used, evidence=NOTE.

WHY THE SCRIPT IS SHAPED THIS WAY (do not "improve" it): GET /wait is the primary signal — the server reconciles hook status against the codex thread's rollout evidence, so its done/error/interrupted are trustworthy and arrive with zero polling. The pane-quiescence branch only runs when /wait is unusable (server down or session untracked), and the seed QCOUNT/LAST_HASH continue the previous leg's quiescence streak. A "waiting" wake is confirmed 5s later before being reported, because approval prompts can flash and self-resolve.

HARD RULES
- Write ONLY inside your scratchpad directory — never ~/.claude, ~/.agents, or any project tree.
- Do not edit, copy, or "improve" ${LIB_DIR}/tcm-watch-leg.sh.
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
