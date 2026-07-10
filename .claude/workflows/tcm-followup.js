// tcm-followup — deterministic resume-respawn follow-up (experiment arm B1).
//
// Delivers a follow-up message to an existing codex delegate thread by
// relaunching its TUI pre-prompted (tmux respawn-pane -k + codex resume
// <uuid>), which is the reliable channel — composer send-keys silently
// drops Enter. The seams the prose protocol kept fumbling become code:
// uuid pinned from the thread's own rollout (never --last), a running-guard
// before the kill, a runStarted checkpoint on the live-run indicator, and
// bounded JS retries.
//
// Invoke via scriptPath with args:
//   { session: "tcm-session-name", pane: "%42", dir: "/abs/delegate/cwd",
//     message: "the follow-up", uuid: "..." (optional, skips discovery),
//     retries: 2 (optional) }
// Returns:
//   { outcome: delivered|failed|refused-running|no-thread|error,
//     detail, uuid, attempts }

export const meta = {
  name: 'tcm-followup',
  description: 'Deliver a follow-up to a TCM codex delegate via resume-respawn with a runStarted checkpoint',
  phases: [
    { title: 'Locate thread', detail: 'pin rollout uuid, guard against a live run' },
    { title: 'Deliver', detail: 'respawn + verify the run started' },
  ],
}

const FIND_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['uuid', 'rollout_path', 'delegate_running', 'evidence'],
  properties: {
    uuid: { type: 'string' },
    rollout_path: { type: 'string' },
    delegate_running: { type: 'boolean' },
    evidence: { type: 'string' },
  },
}

const DELIVER_SCHEMA = {
  type: 'object', additionalProperties: false,
  required: ['started', 'evidence'],
  properties: {
    started: { type: 'boolean' },
    evidence: { type: 'string' },
  },
}

function findScript(p) {
  return `#!/bin/bash
DIR='${p.dir}'
PANE='${p.pane}'
if tmux capture-pane -p -t "$PANE" 2>/dev/null | grep -q 'esc to interrupt'; then echo "RUNNING=yes UUID=none ROLLOUT=none"; exit 0; fi
for f in $(ls -t "$HOME"/.codex/sessions/*/*/*/rollout-*.jsonl 2>/dev/null | head -40); do
  cwd=$(head -1 "$f" | jq -r 'first(.. | objects | select(has("cwd")) | .cwd) // empty' 2>/dev/null)
  if [ "$cwd" = "$DIR" ]; then
    uuid=$(basename "$f" .jsonl | grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')
    echo "RUNNING=no UUID=$uuid ROLLOUT=$f"
    exit 0
  fi
done
echo "RUNNING=no UUID=none ROLLOUT=none"`
}

function findPrompt(p) {
  return `You are a thread-locator for a delegation follow-up. Write the following script VERBATIM (byte-for-byte, no edits) to tcm-followup-find.sh in your scratchpad directory, run it with one Bash call (timeout 120000), and map its single output line to StructuredOutput: delegate_running = (RUNNING=yes), uuid = UUID value ("" if none), rollout_path = ROLLOUT value ("" if none), evidence = the raw line.

${findScript(p)}

HARD RULES: write only inside your scratchpad; never interact with any tmux pane beyond the capture in the script; never compose your own tmux or codex commands; if the script fails, return delegate_running=false, uuid="", rollout_path="", evidence describing the failure.`
}

function deliverScript(p) {
  return `#!/bin/bash
PANE='${p.pane}'
UUID='${p.uuid}'
MSGFILE='__MSGFILE__'
tmux respawn-pane -k -t "$PANE" "codex -c mcp_servers.just.enabled=false resume $UUID \\"Read $MSGFILE and address it\\""
DEADLINE=$((SECONDS+90))
while (( SECONDS < DEADLINE )); do
  if tmux capture-pane -p -t "$PANE" 2>/dev/null | grep -q 'esc to interrupt'; then echo "STARTED=yes NOTE=live-run-indicator-seen"; exit 0; fi
  sleep 3
done
echo "STARTED=no NOTE=no-live-run-indicator-within-90s"`
}

function deliverPrompt(p) {
  return `You are a follow-up deliverer for a delegation thread (attempt ${p.attempt}). Three steps, nothing else.

STEP 1 — Write the following message text EXACTLY to a file named followup-msg.md in your scratchpad directory:
---BEGIN MESSAGE---
${p.message}
---END MESSAGE---
(Do not include the BEGIN/END marker lines in the file.)

STEP 2 — Write the following script to tcm-followup-deliver.sh in your scratchpad directory, VERBATIM except for ONE edit: replace __MSGFILE__ with the absolute path of the followup-msg.md you just wrote.

${deliverScript(p)}

STEP 3 — Run it with one Bash call (timeout 180000; the in-script sleep is fine, standalone sleep is blocked). Map its single output line to StructuredOutput: started = (STARTED=yes), evidence = the NOTE value.

HARD RULES: write only inside your scratchpad; the script's respawn-pane and capture-pane are the ONLY pane interactions allowed — no send-keys, no kill-session, no extra tmux commands; never compose your own codex command; if the script fails or emits nothing, return started=false with the failure in evidence.`
}

let cfg = args || {}
if (typeof cfg === 'string') {
  try { cfg = JSON.parse(cfg) } catch (e) { cfg = {} }
}
for (const k of ['session', 'pane', 'dir', 'message']) {
  if (!cfg[k]) return { outcome: 'error', detail: `args.${k} is required`, uuid: '', attempts: 0 }
}
const retries = cfg.retries === undefined ? 2 : cfg.retries

let uuid = cfg.uuid || ''
if (!uuid) {
  const found = await agent(findPrompt({ dir: cfg.dir, pane: cfg.pane }), {
    label: `find-thread:${cfg.session}`, phase: 'Locate thread', model: 'haiku', effort: 'low', schema: FIND_SCHEMA,
  })
  if (!found) return { outcome: 'error', detail: 'thread-locator leg died', uuid: '', attempts: 0 }
  if (found.delegate_running) return { outcome: 'refused-running', detail: 'delegate shows the live-run indicator; respawn would kill an active run', uuid: '', attempts: 0 }
  if (!found.uuid) return { outcome: 'no-thread', detail: `no rollout with cwd == ${cfg.dir} (${found.evidence})`, uuid: '', attempts: 0 }
  uuid = found.uuid
  log(`tcm-followup: thread ${uuid} located for ${cfg.session}`)
}

for (let attempt = 1; attempt <= 1 + retries; attempt++) {
  const r = await agent(deliverPrompt({ pane: cfg.pane, uuid, message: cfg.message, attempt }), {
    label: `deliver-${attempt}:${cfg.session}`, phase: 'Deliver', model: 'haiku', effort: 'low', schema: DELIVER_SCHEMA,
  })
  if (r && r.started) {
    return { outcome: 'delivered', detail: r.evidence, uuid, attempts: attempt }
  }
  log(`tcm-followup: attempt ${attempt} did not start a run (${r ? r.evidence : 'leg died'})`)
}
return { outcome: 'failed', detail: `run never started after ${1 + retries} respawn attempts`, uuid, attempts: 1 + retries }
