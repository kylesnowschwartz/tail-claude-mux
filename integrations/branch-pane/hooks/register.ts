/**
 * branch-pane: `/branch [name]` opens a fork of this conversation in a new
 * tmux window of the same tmux session, running
 * `claude --resume <id> --fork-session --name <name>`, and this session keeps
 * running. With no name given, the fork is named after this session's own
 * name in `~/.claude/sessions/` plus `(branch)`, so the two read apart.
 *
 * `/branch here [name]` runs the built-in /branch in this session instead,
 * as does any /branch outside tmux or without a local terminal.
 *
 * Every `$.noun.verb(...)` is written literally here; the loader inventories
 * them statically. The helpers in parse.ts take no `$`.
 */

import type { EngineInterface, On } from 'claude-code'

import {
  carriedModel,
  claudeArgv,
  commandLine,
  DEFAULT_NAME,
  defaultForkName,
  failure,
  newWindowArgv,
  parseArgs,
  parseNewWindow,
  parseSessionRecord,
  type SessionRecord,
} from './parse'

const COMMAND_NAME = 'branch'

/**
 * The model the session started on, so only a mid-session /model change is
 * carried to the fork; the fork resolves the start model on its own.
 */
let startModel: string | undefined

/**
 * The session files of every running Claude Code process on this machine,
 * `~/.claude/sessions/<pid>.json`. A file that cannot be read or parsed is
 * left out: they come and go as sessions start and exit. No folder (or no
 * HOME) reads as no files.
 *
 * @param $ the engine interface
 */
async function sessionRecords($: EngineInterface): Promise<SessionRecord[]> {
  const home = await $.env.get('HOME')

  if (!home) {
    return []
  }

  const dir = `${home}/.claude/sessions`
  const entries = await $.fs.list(dir).catch(() => [])
  const files = entries.filter(entry => entry.kind === 'file' && entry.name.endsWith('.json'))
  const texts = await Promise.all(files.map(file => $.fs.read(`${dir}/${file.name}`).catch(() => null)))

  return texts.flatMap(text => {
    const record = text === null ? null : parseSessionRecord(text)

    return record ? [record] : []
  })
}

/**
 * Opens the fork in a new tmux window and says where, or what tmux refused.
 *
 * @param $ the engine interface
 * @param pane this session's tmux pane (`$TMUX_PANE`)
 * @param name the name the person gave the branch, empty for none
 */
async function openBranch($: EngineInterface, pane: string, name: string): Promise<string> {
  const sessionId = await $.session.id()
  // The project root, not the current directory: a shell `cd` during the
  // session moves the latter, and the branch should open where it started.
  const dir = await $.session.root()
  const model = await $.session.model()

  const shown = await $.process.run(['tmux', 'display', '-p', '-t', pane, '#{session_id}'])
  const tmuxSession = shown.stdout.trim()

  if (shown.exitCode !== 0 || !tmuxSession) {
    return `/branch could not read the tmux session of pane ${pane}: ${failure(shown)}`
  }

  const forkName = name || defaultForkName(await sessionRecords($), sessionId)
  const claude = claudeArgv(sessionId, forkName, carriedModel(model, startModel))
  const run = await $.process.run(newWindowArgv(tmuxSession, dir, name || DEFAULT_NAME, claude))

  if (run.exitCode !== 0) {
    return `/branch could not open a tmux window: ${failure(run)}`
  }

  const opened = parseNewWindow(run.stdout)
  const where = opened ? `window ${opened.window} (${opened.pane})` : `a new tmux window (tmux printed: ${run.stdout.trim()})`

  return `Opened branch "${forkName}" in ${where}: ${commandLine(claude)}`
}

/**
 * Registers the /branch takeover and the start-model record.
 *
 * @param on the engine's registrar
 */
export function register(on: On) {
  on('session.start', async ($, e, next) => {
    startModel = await $.session.model()

    return next(e)
  })

  on('command.run', { command: COMMAND_NAME }, async ($, e, next) => {
    const parsed = parseArgs(e.args)

    if (parsed.here) {
      return next({ ...e, args: parsed.rest })
    }

    const pane = await $.env.get('TMUX_PANE')
    const surfaces = await $.session.surfaces()

    if (!pane || !surfaces.includes('terminal')) {
      return next(e)
    }

    return { text: await openBranch($, pane, parsed.name) }
  })
    .catch(($, e, next) => (next.called ? next(e) : { text: `/branch failed: ${next.error.message}` }))
}
