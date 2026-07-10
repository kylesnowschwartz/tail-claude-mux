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

// Experiment-branch default; override via args.watchScriptPath when the
// checkout lives elsewhere (distribution is a deferred decision).
const DEFAULT_WATCH = '/Users/kyle/Code/my-projects/tail-claude-mux/.claude/worktrees/delegation-workflows/.claude/workflows/tcm-watch.js'

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

STEP 1 — Write the following brief text EXACTLY to a file named delegate-brief.md in your scratchpad directory:
---BEGIN BRIEF---
${p.brief}
---END BRIEF---
(Do not include the BEGIN/END marker lines in the file.)

STEP 2 — Write the following script to tcm-spawn.sh in your scratchpad directory, VERBATIM except for ONE edit: replace __BRIEF__ with the absolute path of the delegate-brief.md you just wrote.

${spawnScript(p)}

STEP 3 — Run it with one Bash call (timeout 60000). Map its single output line to StructuredOutput: session_name=SESSION, pane_id=PANE, window_id=WINDOW, alive=(ALIVE=yes), evidence=NOTE. Use "" for none values.

HARD RULES: write only inside your scratchpad; do not send keys to, capture, or kill any tmux session; do not retry the POST yourself (the workflow decides); if the script fails, return alive=false with the failure in evidence.`
}

function resultPrompt(p) {
  return `A codex delegate working in ${p.dir} has finished. Find its rollout: the newest file matching ~/.codex/sessions/*/*/*/rollout-*.jsonl whose FIRST line contains "cwd":"${p.dir}" (check the newest ~40 by mtime; use head -1 plus grep/jq per file — never cat a whole rollout). From that file, extract the FINAL assistant message (the last line whose payload has type "message" with role "assistant", or the last agent_message payload — the format drifts, so fall back to the last line containing meaningful assistant text). Return StructuredOutput: summary = the delegate's conclusions in at most 120 words (verbatim key claims, no embellishment), rollout_path = the file you read, evidence = one clause on how you identified the final message. If no rollout matches, summary="" and evidence explains. Read nothing except rollout files under ~/.codex/sessions; write nothing anywhere.`
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
if (!watch || watch.resolution !== 'finished') {
  return {
    outcome: watch ? watch.resolution : 'error',
    detail: watch ? watch.detail : 'nested watch returned nothing',
    sessionName: spawned.session_name, paneId: spawned.pane_id, dir: cfg.dir, resultSummary: '', watch,
  }
}

phase('Result')
const result = await agent(resultPrompt({ dir: cfg.dir }), {
  label: `result:${spawned.session_name}`, phase: 'Result', model: 'sonnet', effort: 'low', schema: RESULT_SCHEMA,
})
return {
  outcome: 'finished',
  detail: result ? result.evidence : 'result leg died; pane capture is the fallback',
  sessionName: spawned.session_name, paneId: spawned.pane_id, dir: cfg.dir,
  resultSummary: result ? result.summary : '', watch,
}
