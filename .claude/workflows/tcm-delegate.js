// tcm-delegate — composite delegation workflow (experiment arm B1).
//
// spawn (TCM POST /spawn-agent) → watch (nested tcm-watch.js run) → read
// result (final assistant message from the thread's rollout). The delegate
// runs in a visible tmux pane with a dashboard row (MO-007); this workflow
// only automates the seams around it.
//
// Invoke via scriptPath with args:
//   { dir: "/abs/workdir", name: "kebab-session-name", brief: "context-complete task brief",
//     watchMinutes: 20 (optional), pollSeconds: 30 (optional),
//     ownerSession: "tmux-session" (optional; defaults to current tmux session),
//     watchScriptPath: "/abs/path/tcm-watch.js" (optional override) }
// Returns:
//   { outcome: finished|waiting|error|session-dead|timeout|spawn-failed,
//     sessionName, paneId, windowId, ownerSession, dir, resultSummary, watch: {...}, detail }

export const meta = {
  name: 'tcm-delegate',
  description: 'Spawn a visible codex delegate via TCM, watch it to terminal state, read its result',
  phases: [
    { title: 'Spawn', detail: 'POST /spawn-agent + survival check' },
    { title: 'Watch', detail: 'nested tcm-watch run' },
    { title: 'Result', detail: 'final assistant message from the rollout' },
  ],
}

// Main-checkout default (workflows ship in the TCM repo); override via
// args.watchScriptPath when running from another checkout.
const DEFAULT_WATCH = '/Users/kyle/Code/my-projects/tail-claude-mux/.claude/workflows/tcm-watch.js'
// Committed, argument-driven shell scripts the delegate agents run verbatim
// instead of transcribing a generated script to scratchpad.
const LIB_DIR = '/Users/kyle/Code/my-projects/tail-claude-mux/.claude/workflows/lib'

const SPAWN_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['session_name', 'pane_id', 'window_id', 'owner_session', 'alive', 'evidence'],
  properties: {
    session_name: { type: 'string' },
    pane_id: { type: 'string' },
    window_id: { type: 'string' },
    owner_session: { type: 'string' },
    alive: { type: 'boolean' },
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

function spawnPrompt(p) {
  const owner = p.ownerSession || ''
  // JSON-encode the brief so it rides as ONE single-line, safely-quoted shell
  // token: JSON.stringify escapes newlines to \n (no literal newlines for the
  // agent's line-joining shell wrapper to mangle) and the single-quote wrap
  // makes arbitrary content injection-safe. tcm-spawn.sh decodes it with jq.
  // This replaces the old "write the brief to a scratchpad file first" step —
  // the agent is now a pure executor of one exact, deterministic command.
  const briefArg = "'" + JSON.stringify(p.brief).replace(/'/g, "'\\''") + "'"
  return `You are a delegate spawner. Run this single command EXACTLY once (timeout 60000), and nothing else:
bash ${LIB_DIR}/tcm-spawn.sh '${p.dir}' '${p.name}' ${briefArg} '${owner}'

Map its single output line to StructuredOutput: session_name=SESSION, pane_id=PANE, window_id=WINDOW, owner_session=OWNER, alive=(ALIVE=yes), evidence=NOTE. Use "" for none values.

HARD RULES: do not write any files; do not modify the command; do not send keys to, capture, or kill any tmux session; do not retry the POST yourself (the workflow decides); do not edit or "improve" ${LIB_DIR}/tcm-spawn.sh; if the script fails, return alive=false with the failure in evidence.`
}

function resultPrompt(p) {
  return `Run this single command exactly once: curl -fsS -G 'localhost:7391/result' --data-urlencode 'session=${p.session}' --data-urlencode 'pane=${p.pane}'

Map the JSON response to StructuredOutput: summary=finalMessage, rollout_path=rolloutPath, evidence=identification+"; status="+status. If curl fails, including HTTP 404, return summary="", rollout_path="", and put the curl failure in evidence. Do not read or scan rollout files. Do not run any other command.`
}

let cfg = args || {}
if (typeof cfg === 'string') {
  try { cfg = JSON.parse(cfg) } catch (e) { cfg = {} }
}
for (const k of ['dir', 'name', 'brief']) {
  if (!cfg[k]) return { outcome: 'spawn-failed', detail: `args.${k} is required`, sessionName: '', paneId: '', windowId: '', ownerSession: cfg.ownerSession || '', dir: cfg.dir || '', resultSummary: '', watch: null }
}

phase('Spawn')
const spawned = await agent(spawnPrompt({ dir: cfg.dir, name: cfg.name, brief: cfg.brief, ownerSession: cfg.ownerSession || '' }), {
  label: `spawn:${cfg.name}`, phase: 'Spawn', model: 'haiku', effort: 'low', schema: SPAWN_SCHEMA,
})
if (!spawned || !spawned.alive || !spawned.session_name || spawned.session_name === 'none') {
  return { outcome: 'spawn-failed', detail: spawned ? spawned.evidence : 'spawn leg died', sessionName: '', paneId: '', windowId: spawned ? spawned.window_id : '', ownerSession: spawned ? spawned.owner_session : (cfg.ownerSession || ''), dir: cfg.dir, resultSummary: '', watch: null }
}
log(`tcm-delegate: spawned "${spawned.session_name}" (pane ${spawned.pane_id}) in ${cfg.dir}`)

phase('Watch')
const watch = await workflow(
  { scriptPath: cfg.watchScriptPath || DEFAULT_WATCH },
  { session: spawned.session_name, pane: spawned.pane_id, watchMinutes: cfg.watchMinutes || 20, pollSeconds: cfg.pollSeconds || 30 }
)
if (!watch) {
  return { outcome: 'error', detail: 'nested watch returned nothing', sessionName: spawned.session_name, paneId: spawned.pane_id, windowId: spawned.window_id, ownerSession: spawned.owner_session, dir: cfg.dir, resultSummary: '', watch }
}
// The rollout outlives the pane: attempt the result-read even on
// session-dead/timeout (a killed pane still yields the delegate's last
// words) and on waiting — GET /result never blocks, so a genuinely
// blocked pane returns hasFinal=false while a false-waiting (finished
// pane idling at the input prompt, seen 2/2 on 2026-07-11) still yields
// the final message instead of stalling the run.
const READABLE = watch.resolution === 'finished' || watch.resolution === 'session-dead' || watch.resolution === 'timeout' || watch.resolution === 'waiting'
if (!READABLE) {
  return { outcome: watch.resolution, detail: watch.detail, sessionName: spawned.session_name, paneId: spawned.pane_id, windowId: spawned.window_id, ownerSession: spawned.owner_session, dir: cfg.dir, resultSummary: '', watch }
}

phase('Result')
// Invariant: once the delegate reached a readable state, this workflow
// returns that outcome no matter what the result leg does. A schema
// retry-cap inside agent() THROWS — without this catch it voids a
// delegation whose real work already succeeded.
let result = null
try {
  result = await agent(resultPrompt({ session: spawned.session_name, pane: spawned.pane_id }), {
    label: `result:${spawned.session_name}`, phase: 'Result', model: 'haiku', effort: 'low', schema: RESULT_SCHEMA,
  })
} catch (e) {
  log(`tcm-delegate: result leg failed (${e && e.message ? e.message : e}); delegate outcome preserved`)
}
return {
  outcome: watch.resolution,
  detail: result ? result.evidence : `result leg failed; curl localhost:7391/result?session=${spawned.session_name}`,
  sessionName: spawned.session_name, paneId: spawned.pane_id, windowId: spawned.window_id, ownerSession: spawned.owner_session, dir: cfg.dir,
  resultSummary: result ? result.summary : '', watch,
}
