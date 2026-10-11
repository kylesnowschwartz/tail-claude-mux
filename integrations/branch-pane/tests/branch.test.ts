import type { FsEntry, On, ProcessRunResult } from 'claude-code'
import { describe, expect, mock, test, type Engine } from 'claude-code/testing'

import { defaultForkName, escapeFormat, parseArgs, parseNewWindow, parseSessionRecord } from '../hooks/parse'

const SESSION_ID = '0b1c2d3e-aaaa-bbbb-cccc-000000000001'
const OTHER_ID = '0b1c2d3e-aaaa-bbbb-cccc-000000000002'
const ROOT = '/Users/kyle/Code/project'
const START_MODEL = 'claude-opus-5-5'
const HOME = '/Users/kyle'
const SESSIONS = `${HOME}/.claude/sessions`
const OWN_NAME = 'tail-claude-mux-01'

/** A session file's text, as Claude Code writes one. */
const sessionFile = (sessionId: string, name: string) =>
  JSON.stringify({ sessionId, name, nameSource: 'derived', pid: 4242 })

/** What the world beneath the plugin answers, and what it saw. */
type World = {
  model: string
  root: string
  surfaces: ('terminal' | 'desktop')[]
  tmux: (argv: readonly string[]) => Partial<ProcessRunResult>
  /** Files in the sessions folder by name; null for one whose read fails. */
  sessionFiles: Record<string, string | null>
  runs: (readonly string[])[]
  builtinArgs: string[]
}

/**
 * Stands in for the engine beneath the plugin: the session's facts, the
 * sessions folder through `fs`, tmux through `process.run`, and the built-in
 * /branch as a recorder.
 */
function world(on: On, env: Record<string, string>, overrides: Partial<World> = {}): World {
  const w: World = {
    model: START_MODEL,
    root: ROOT,
    surfaces: ['terminal'],
    tmux: argv => (argv[1] === 'display' ? { stdout: '$3\n' } : { stdout: '5 %1300\n' }),
    sessionFiles: { '4242.json': sessionFile(SESSION_ID, OWN_NAME), '5151.json': sessionFile(OTHER_ID, 'other') },
    runs: [],
    builtinArgs: [],
    ...overrides,
  }

  mock.env(on, env)
  on('session.id', () => ({ value: SESSION_ID }))
  on('session.root', () => ({ value: w.root }))
  on('session.model', () => ({ value: w.model }))
  on('session.surfaces', () => ({ value: w.surfaces }))
  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('fs.list', ($, e) => {
    if (e.path !== SESSIONS) {
      return { deny: `ENOENT: ${e.path}` }
    }

    const entries: FsEntry[] = Object.keys(w.sessionFiles).map(name => ({
      name,
      kind: 'file',
      size: 100,
      mtimeMs: 0,
      isLink: false,
    }))

    return { value: entries }
  })
  on('fs.read', ($, e) => {
    const name = e.path.startsWith(`${SESSIONS}/`) ? e.path.slice(SESSIONS.length + 1) : ''
    const text = w.sessionFiles[name]

    return typeof text === 'string' ? { value: text } : { deny: `ENOENT: ${e.path}` }
  })
  on('process.run', ($, e) => {
    w.runs.push(e.argv)

    return { value: { exitCode: 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false, ...w.tmux(e.argv) } }
  })
  on('command.run', { command: 'branch' }, ($, e) => {
    w.builtinArgs.push(e.args)

    return { text: 'built-in branch ran' }
  })

  return w
}

const IN_TMUX = { TMUX_PANE: '%42', HOME }

/** Types `/branch <args>` into a started session. */
function run($: Engine, args: string) {
  return $.command.run({
    command: 'branch',
    args,
    origin: { kind: 'composer' },
    presentation: { isFullscreen: false, columns: 120 },
  })
}

/** Starts the session, then types `/branch <args>`. */
async function branch($: Engine, args: string) {
  await $.session.start({ cwd: ROOT, surface: 'terminal', isInteractive: true })

  return run($, args)
}

describe('parse helpers', () => {
  test('here is a keyword only as the first word', () => {
    expect(parseArgs('here my idea')).toEqual({ here: true, rest: 'my idea' })
    expect(parseArgs('  HERE  ')).toEqual({ here: true, rest: '' })
    expect(parseArgs('  try a thing  ')).toEqual({ here: false, name: 'try a thing' })
    expect(parseArgs('')).toEqual({ here: false, name: '' })
    expect(parseArgs('nowhere')).toEqual({ here: false, name: 'nowhere' })
  })

  test('escapeFormat doubles every #', () => {
    expect(escapeFormat('a#b##c')).toBe('a##b####c')
  })

  test('parseNewWindow reads index and pane id', () => {
    expect(parseNewWindow('7 %12\n')).toEqual({ window: '7', pane: '%12' })
    expect(parseNewWindow('nonsense')).toBe(null)
  })

  test('parseSessionRecord keeps sessionId and name, refuses the rest', () => {
    expect(parseSessionRecord(sessionFile('a', 'x'))).toEqual({ sessionId: 'a', name: 'x' })
    expect(parseSessionRecord('{"sessionId":"a"}')).toEqual({ sessionId: 'a', name: '' })
    expect(parseSessionRecord('{"name":"x"}')).toBe(null)
    expect(parseSessionRecord('{"sessionId":')).toBe(null)
    expect(parseSessionRecord('null')).toBe(null)
  })

  test('defaultForkName takes the first free (branch N)', () => {
    const own = { sessionId: 'me', name: 'proj' }

    expect(defaultForkName([own], 'me')).toBe('proj (branch)')
    expect(defaultForkName([own, { sessionId: 'b', name: 'proj (branch)' }], 'me')).toBe('proj (branch 2)')
    expect(defaultForkName([own, { sessionId: 'b', name: 'proj (branch 2)' }], 'me')).toBe('proj (branch)')
    expect(defaultForkName([{ sessionId: 'b', name: 'proj' }], 'me')).toBe('branch')
    expect(defaultForkName([{ sessionId: 'me', name: '  ' }], 'me')).toBe('branch')
  })
})

describe('/branch', () => {
  test('without TMUX_PANE the built-in runs', async ($, on) => {
    const w = world(on, { HOME })
    const result = await branch($, 'idea')

    expect(result.text).toBe('built-in branch ran')
    expect(w.builtinArgs).toEqual(['idea'])
    expect(w.runs).toEqual([])
  })

  test('without a terminal surface the built-in runs', async ($, on) => {
    const w = world(on, IN_TMUX, { surfaces: ['desktop'] })
    const result = await branch($, '')

    expect(result.text).toBe('built-in branch ran')
    expect(w.runs).toEqual([])
  })

  test('here runs the built-in with the rest of the args', async ($, on) => {
    const w = world(on, IN_TMUX)
    const result = await branch($, 'here my idea')

    expect(result.text).toBe('built-in branch ran')
    expect(w.builtinArgs).toEqual(['my idea'])
    expect(w.runs).toEqual([])
  })

  test('default path opens a current window running the fork, named after this session', async ($, on) => {
    const w = world(on, IN_TMUX)
    const result = await branch($, '')

    expect(w.builtinArgs).toEqual([])
    expect(w.runs).toEqual([
      ['tmux', 'display', '-p', '-t', '%42', '#{session_id}'],
      [
        'tmux', 'new-window', '-P', '-F', '#{window_index} #{pane_id}',
        '-t', '$3:', '-c', ROOT, '-n', 'branch',
        'claude', '--resume', SESSION_ID, '--fork-session', '--name', `${OWN_NAME} (branch)`,
      ],
    ])
    expect(w.runs[1]).not.toContain('-d')
    expect(result.text).toBe(
      `Opened branch "${OWN_NAME} (branch)" in window 5 (%1300): ` +
        `claude --resume ${SESSION_ID} --fork-session --name "${OWN_NAME} (branch)"`,
    )
  })

  test('a taken default name moves on to (branch 2)', async ($, on) => {
    const w = world(on, IN_TMUX, {
      sessionFiles: {
        '4242.json': sessionFile(SESSION_ID, OWN_NAME),
        '6000.json': sessionFile(OTHER_ID, `${OWN_NAME} (branch)`),
      },
    })

    await branch($, '')
    expect((w.runs[1] ?? []).slice(-2)).toEqual(['--name', `${OWN_NAME} (branch 2)`])
  })

  test('with (branch) and (branch 2) taken, the fork is (branch 3)', async ($, on) => {
    const w = world(on, IN_TMUX, {
      sessionFiles: {
        '4242.json': sessionFile(SESSION_ID, OWN_NAME),
        '6000.json': sessionFile(OTHER_ID, `${OWN_NAME} (branch)`),
        '6001.json': sessionFile('0b1c2d3e-aaaa-bbbb-cccc-000000000003', `${OWN_NAME} (branch 2)`),
      },
    })

    await branch($, '')
    expect((w.runs[1] ?? []).slice(-2)).toEqual(['--name', `${OWN_NAME} (branch 3)`])
  })

  test('with no file for this session the fork is named branch', async ($, on) => {
    const w = world(on, IN_TMUX, { sessionFiles: { '5151.json': sessionFile(OTHER_ID, 'other') } })
    const result = await branch($, '')

    expect((w.runs[1] ?? []).slice(-2)).toEqual(['--name', 'branch'])
    expect(result.text).toBe(`Opened branch "branch" in window 5 (%1300): claude --resume ${SESSION_ID} --fork-session --name branch`)
  })

  test('unreadable and invalid session files are skipped', async ($, on) => {
    const w = world(on, IN_TMUX, {
      sessionFiles: {
        '1000.json': null,
        '1001.json': '{"sessionId":',
        '1002.json': sessionFile(OTHER_ID, `${OWN_NAME} (branch)`),
        '4242.json': sessionFile(SESSION_ID, OWN_NAME),
        'notes.txt': sessionFile(OTHER_ID, `${OWN_NAME} (branch 2)`),
      },
    })
    const result = await branch($, '')

    expect((w.runs[1] ?? []).slice(-2)).toEqual(['--name', `${OWN_NAME} (branch 2)`])
    expect(result.text).toContain(`Opened branch "${OWN_NAME} (branch 2)"`)
  })

  test('a given name names the window and the fork, unchanged by session files', async ($, on) => {
    const w = world(on, IN_TMUX)
    const result = await branch($, '  try redis  ')
    const argv = w.runs[1] ?? []

    expect(argv.slice(9, 11)).toEqual(['-n', 'try redis'])
    expect(argv.slice(11)).toEqual(['claude', '--resume', SESSION_ID, '--fork-session', '--name', 'try redis'])
    expect(result.text).toBe(`Opened branch "try redis" in window 5 (%1300): claude --resume ${SESSION_ID} --fork-session --name "try redis"`)
  })

  test('# is doubled in -n and -c but not in the fork argv', async ($, on) => {
    const w = world(on, IN_TMUX, { root: '/work/#1' })

    await branch($, 'issue #7')

    const argv = w.runs[1] ?? []

    expect(argv.slice(7, 11)).toEqual(['-c', '/work/##1', '-n', 'issue ##7'])
    expect(argv.slice(-2)).toEqual(['--name', 'issue #7'])
  })

  test('the model is carried only after a mid-session change', async ($, on) => {
    const w = world(on, IN_TMUX)

    await branch($, '')
    expect(w.runs[1]).not.toContain('--model')

    w.model = 'claude-sonnet-5'
    await run($, '')
    expect((w.runs[3] ?? []).slice(-2)).toEqual(['--model', 'claude-sonnet-5'])
  })

  test('a tmux failure answers with its stderr', async ($, on) => {
    world(on, IN_TMUX, {
      tmux: argv =>
        argv[1] === 'display' ? { stdout: '$3\n' } : { exitCode: 1, stderr: "can't find session: $3\n" },
    })
    const result = await branch($, '')

    expect(result.text).toBe("/branch could not open a tmux window: can't find session: $3")
  })

  test('a failed session lookup answers with its stderr', async ($, on) => {
    const w = world(on, IN_TMUX, {
      tmux: () => ({ exitCode: 1, stderr: 'no server running on /tmp/tmux-502/default\n' }),
    })
    const result = await branch($, '')

    expect(result.text).toBe('/branch could not read the tmux session of pane %42: no server running on /tmp/tmux-502/default')
    expect(w.runs).toHaveLength(1)
  })
})
