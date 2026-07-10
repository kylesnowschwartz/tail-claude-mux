// tcm-followup — deterministic resume-respawn follow-up (experiment arm B1).
//
// Delivers a follow-up to an existing codex delegate thread by relaunching
// its TUI pre-prompted (tmux respawn-pane -k + codex resume <uuid>) — the
// reliable channel; composer send-keys silently drops Enter.
//
// SPLIT DESIGN (learned 2026-07-10): the permission classifier blocks
// workflow subagents from respawn-pane -k ("interferes with workloads it
// didn't create"), so the one interfering command is executed by the
// ORCHESTRATOR between two workflow calls. Everything deterministic stays
// in code: uuid pinned from the thread's own rollout (never --last), a
// running-guard before any kill, the exact respawn command emitted
// verbatim, and a runStarted checkpoint after.
//
// Orchestrator protocol (the B1 follow-up seam):
//   1. Write the follow-up message to a file (multiline-safe).
//   2. Run this workflow with mode "locate" -> get respawn_command with
//      __MSGFILE__ placeholder; substitute the message file's absolute path.
//   3. Run that ONE command yourself (your permission context).
//   4. Run this workflow with mode "verify" (pass the locate result's
//      rollout_path and the message-file path as marker) -> delivery
//      checkpoint: marker in the rollout is primary (codex resume appends
//      to the same rollout file), pane live-run indicator secondary.
//   5. verify says not-started -> repeat 3-4 (max 2 retries), then report.
//
// args (locate): { mode: "locate", dir: "/abs/delegate/cwd", pane: "%42" }
// args (verify): { mode: "verify", pane: "%42", rollout: "/abs/rollout.jsonl",
//                  marker: "/abs/path/to/message-file.md" }
// Returns (locate): { outcome: ready|refused-running|no-thread|error, uuid,
//                     rollout_path, respawn_command, detail }
// Returns (verify): { outcome: started|not-started|error, detail }

export const meta = {
  name: 'tcm-followup',
  description: 'Locate a TCM codex delegate thread and verify a resume-respawn follow-up started (the respawn itself is orchestrator-run)',
  phases: [
    { title: 'Locate thread', detail: 'pin rollout uuid, guard against a live run' },
    { title: 'Verify start', detail: 'runStarted checkpoint on the live-run indicator' },
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

const VERIFY_SCHEMA = {
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

HARD RULES: write only inside your scratchpad; the script's capture-pane is the only tmux interaction allowed; never compose your own tmux or codex commands; if the script fails, return delegate_running=false, uuid="", rollout_path="", evidence describing the failure.`
}

function verifyScript(p) {
  // Primary signal: the follow-up prompt (marker) lands in the thread's own
  // rollout file — codex resume appends to the same rollout (verified
  // 2026-07-10). The pane's live-run indicator alone false-negatives on fast
  // runs that finish before the first poll.
  return `#!/bin/bash
PANE='${p.pane}'
ROLLOUT='${p.rollout}'
MARKER='${p.marker}'
DEADLINE=$((SECONDS+90))
while (( SECONDS < DEADLINE )); do
  if grep -qF "$MARKER" "$ROLLOUT" 2>/dev/null; then
    if tmux capture-pane -p -t "$PANE" 2>/dev/null | grep -q 'esc to interrupt'; then echo "STARTED=yes NOTE=delivered-and-running"; else echo "STARTED=yes NOTE=delivered-run-may-already-be-finished"; fi
    exit 0
  fi
  if tmux capture-pane -p -t "$PANE" 2>/dev/null | grep -q 'esc to interrupt'; then echo "STARTED=yes NOTE=live-run-indicator-seen"; exit 0; fi
  sleep 3
done
echo "STARTED=no NOTE=no-rollout-marker-and-no-live-run-indicator-within-90s"`
}

function verifyPrompt(p) {
  return `You are a runStarted checkpoint for a delegation follow-up. Write the following script VERBATIM (byte-for-byte, no edits) to tcm-followup-verify.sh in your scratchpad directory, run it with one Bash call (timeout 180000; the in-script sleep is fine, standalone sleep is blocked), and map its single output line to StructuredOutput: started = (STARTED=yes), evidence = the NOTE value.

${verifyScript(p)}

HARD RULES: write only inside your scratchpad; the script's capture-pane is the only tmux interaction allowed — no send-keys, no respawn, no kills; if the script fails or emits nothing, return started=false with the failure in evidence.`
}

function respawnCommand(pane, uuid) {
  return `tmux respawn-pane -k -t '${pane}' 'codex -c mcp_servers.just.enabled=false resume ${uuid} "Read __MSGFILE__ and address it"'`
}

let cfg = args || {}
if (typeof cfg === 'string') {
  try { cfg = JSON.parse(cfg) } catch (e) { cfg = {} }
}
const mode = cfg.mode || 'locate'

if (mode === 'verify') {
  for (const k of ['pane', 'rollout', 'marker']) {
    if (!cfg[k]) return { outcome: 'error', detail: `args.${k} is required for verify (rollout = the thread's rollout path from locate; marker = the message-file path in the respawn command)` }
  }
  const v = await agent(verifyPrompt({ pane: cfg.pane, rollout: cfg.rollout, marker: cfg.marker }), {
    label: `verify-start:${cfg.pane}`, phase: 'Verify start', model: 'haiku', effort: 'low', schema: VERIFY_SCHEMA,
  })
  if (!v) return { outcome: 'error', detail: 'verify leg died' }
  return { outcome: v.started ? 'started' : 'not-started', detail: v.evidence }
}

for (const k of ['dir', 'pane']) {
  if (!cfg[k]) return { outcome: 'error', detail: `args.${k} is required for locate`, uuid: '', rollout_path: '', respawn_command: '' }
}
const found = await agent(findPrompt({ dir: cfg.dir, pane: cfg.pane }), {
  label: `find-thread:${cfg.pane}`, phase: 'Locate thread', model: 'haiku', effort: 'low', schema: FIND_SCHEMA,
})
if (!found) return { outcome: 'error', detail: 'thread-locator leg died', uuid: '', rollout_path: '', respawn_command: '' }
if (found.delegate_running) return { outcome: 'refused-running', detail: 'delegate shows the live-run indicator; respawn would kill an active run', uuid: '', rollout_path: '', respawn_command: '' }
if (!found.uuid) return { outcome: 'no-thread', detail: `no rollout with cwd == ${cfg.dir} (${found.evidence})`, uuid: '', rollout_path: '', respawn_command: '' }
return {
  outcome: 'ready',
  detail: 'substitute __MSGFILE__ with the absolute path of your message file, run the command, then re-invoke with mode "verify"',
  uuid: found.uuid,
  rollout_path: found.rollout_path,
  respawn_command: respawnCommand(cfg.pane, found.uuid),
}
