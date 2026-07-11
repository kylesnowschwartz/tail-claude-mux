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
//     watchScriptPath: "/abs/path/tcm-watch.js" (optional override) }
// Returns:
//   { outcome: finished|waiting|error|session-dead|timeout|spawn-failed,
//     sessionName, paneId, dir, resultSummary, watch: {...}, detail }

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

const SPAWN_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['session_name', 'pane_id', 'window_id', 'alive', 'evidence'],
  properties: {
    session_name: { type: 'string' },
    pane_id: { type: 'string' },
    window_id: { type: 'string' },
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

function spawnScript(p) {
  return `#!/bin/bash
BRIEF='__BRIEF__'
RESP=$(curl -fsS -X POST localhost:7391/spawn-agent -H 'Content-Type: application/json' -d "$(jq -n --arg dir '${p.dir}' --arg name '${p.name}' --rawfile pr "$BRIEF" '{dir:$dir, agent:"codex", prompt:$pr, name:$name, command:["codex","-c","mcp_servers.just.enabled=false"]}')")
if [ -z "$RESP" ]; then echo "SESSION=none PANE=none WINDOW=none ALIVE=no NOTE=spawn-request-failed"; exit 0; fi
SESH=$(printf '%s' "$RESP" | jq -r .sessionName)
PANE=$(printf '%s' "$RESP" | jq -r .paneId)
WIN=$(printf '%s' "$RESP" | jq -r .windowId)
sleep 2
if tmux has-session -t "=$SESH" 2>/dev/null; then ALIVE=yes; else ALIVE=no; fi
echo "SESSION=$SESH PANE=$PANE WINDOW=$WIN ALIVE=$ALIVE NOTE=ok"`
}

function spawnPrompt(p) {
  return `You are a delegate spawner. Three steps, nothing else.

STEP 1 — Write the following brief text EXACTLY to a file named delegate-brief-${p.name}.md in your scratchpad directory:
---BEGIN BRIEF---
${p.brief}
---END BRIEF---
(Do not include the BEGIN/END marker lines in the file.)

STEP 2 — Write the following script to tcm-spawn-${p.name}.sh in your scratchpad directory, VERBATIM except for ONE edit: replace __BRIEF__ with the absolute path of the delegate-brief-${p.name}.md you just wrote.

${spawnScript(p)}

STEP 3 — Run it with one Bash call (timeout 60000). Map its single output line to StructuredOutput: session_name=SESSION, pane_id=PANE, window_id=WINDOW, alive=(ALIVE=yes), evidence=NOTE. Use "" for none values.

HARD RULES: write only inside your scratchpad; do not send keys to, capture, or kill any tmux session; do not retry the POST yourself (the workflow decides); if the script fails, return alive=false with the failure in evidence.`
}

function resultPrompt(p) {
  return `Run this single command exactly once: curl -fsS 'localhost:7391/result?session=${encodeURIComponent(p.session)}'

Map the JSON response to StructuredOutput: summary=finalMessage, rollout_path=rolloutPath, evidence=identification+"; status="+status. If curl fails, including HTTP 404, return summary="", rollout_path="", and put the curl failure in evidence. Do not read or scan rollout files. Do not run any other command.`
}

let cfg = args || {}
if (typeof cfg === 'string') {
  try { cfg = JSON.parse(cfg) } catch (e) { cfg = {} }
}
for (const k of ['dir', 'name', 'brief']) {
  if (!cfg[k]) return { outcome: 'spawn-failed', detail: `args.${k} is required`, sessionName: '', paneId: '', dir: cfg.dir || '', resultSummary: '', watch: null }
}

phase('Spawn')
const spawned = await agent(spawnPrompt({ dir: cfg.dir, name: cfg.name, brief: cfg.brief }), {
  label: `spawn:${cfg.name}`, phase: 'Spawn', model: 'haiku', effort: 'low', schema: SPAWN_SCHEMA,
})
if (!spawned || !spawned.alive || !spawned.session_name || spawned.session_name === 'none') {
  return { outcome: 'spawn-failed', detail: spawned ? spawned.evidence : 'spawn leg died', sessionName: '', paneId: '', dir: cfg.dir, resultSummary: '', watch: null }
}
log(`tcm-delegate: spawned "${spawned.session_name}" (pane ${spawned.pane_id}) in ${cfg.dir}`)

phase('Watch')
const watch = await workflow(
  { scriptPath: cfg.watchScriptPath || DEFAULT_WATCH },
  { session: spawned.session_name, pane: spawned.pane_id, watchMinutes: cfg.watchMinutes || 20, pollSeconds: cfg.pollSeconds || 30 }
)
if (!watch) {
  return { outcome: 'error', detail: 'nested watch returned nothing', sessionName: spawned.session_name, paneId: spawned.pane_id, dir: cfg.dir, resultSummary: '', watch }
}
// The rollout outlives the pane: attempt the result-read even on
// session-dead/timeout, so a killed pane still yields the delegate's last
// words (learned when a trial session was trimmed mid-run).
const READABLE = watch.resolution === 'finished' || watch.resolution === 'session-dead' || watch.resolution === 'timeout'
if (!READABLE) {
  return { outcome: watch.resolution, detail: watch.detail, sessionName: spawned.session_name, paneId: spawned.pane_id, dir: cfg.dir, resultSummary: '', watch }
}

phase('Result')
// Invariant: once the delegate reached a readable state, this workflow
// returns that outcome no matter what the result leg does. A schema
// retry-cap inside agent() THROWS — without this catch it voids a
// delegation whose real work already succeeded.
let result = null
try {
  result = await agent(resultPrompt({ session: spawned.session_name }), {
    label: `result:${spawned.session_name}`, phase: 'Result', model: 'haiku', effort: 'low', schema: RESULT_SCHEMA,
  })
} catch (e) {
  log(`tcm-delegate: result leg failed (${e && e.message ? e.message : e}); delegate outcome preserved`)
}
return {
  outcome: watch.resolution,
  detail: result ? result.evidence : `result leg failed; curl localhost:7391/result?session=${spawned.session_name}`,
  sessionName: spawned.session_name, paneId: spawned.pane_id, dir: cfg.dir,
  resultSummary: result ? result.summary : '', watch,
}
