/**
 * Pure helpers for /branch: the argument parser, tmux format escaping, the
 * argv of the forked claude and of `tmux new-window`, and the parse of what
 * tmux prints back. No `$` here, so the tests import them directly.
 */

/**
 * What `/branch ...` asks for: `here` runs the built-in /branch in this
 * session with `rest` as its arguments; otherwise `name` is the branch's name,
 * empty when none was given.
 */
export type BranchArgs = { here: true; rest: string } | { here: false; name: string }

/**
 * Splits `/branch` arguments into the `here` keyword or a branch name.
 *
 * Only a first word of `here` (any case) is a keyword; anything else is the
 * name, trimmed, so a name of several words keeps its spaces.
 *
 * @param raw everything after `/branch`
 */
export function parseArgs(raw: string): BranchArgs {
  const text = raw.trim()
  const match = /^(\S+)\s*([\s\S]*)$/.exec(text)

  if (match && match[1]?.toLowerCase() === 'here') {
    return { here: true, rest: (match[2] ?? '').trim() }
  }

  return { here: false, name: text }
}

/**
 * The tmux window name when the person gives the branch none, and the fork's
 * session name when this session's own name cannot be found either.
 */
export const DEFAULT_NAME = 'branch'

/** The two fields /branch reads from a `~/.claude/sessions/<pid>.json` file. */
export type SessionRecord = { sessionId: string; name: string }

/**
 * Reads one session file's text as a record, or null when it is not JSON or
 * lacks a string `sessionId`; a missing or non-string `name` reads as `''`.
 *
 * @param text the file's contents
 */
export function parseSessionRecord(text: string): SessionRecord | null {
  let parsed: unknown

  try {
    parsed = JSON.parse(text)
  } catch {
    return null
  }

  if (!parsed || typeof parsed !== 'object') {
    return null
  }

  const record = parsed as Record<string, unknown>

  if (typeof record.sessionId !== 'string') {
    return null
  }

  return { sessionId: record.sessionId, name: typeof record.name === 'string' ? record.name : '' }
}

/**
 * The fork's session name when the person gave none: this session's name plus
 * `(branch)`, or `(branch 2)`, `(branch 3)`, ... when a session already uses
 * it, so the fork is told apart from this session and earlier forks. Falls
 * back to `branch` when no record is this session's or its name is empty.
 *
 * @param records the session files that parsed
 * @param ownId this session's id
 */
export function defaultForkName(records: readonly SessionRecord[], ownId: string): string {
  const own = records.find(record => record.sessionId === ownId)?.name.trim()

  if (!own) {
    return DEFAULT_NAME
  }

  const taken = new Set(records.map(record => record.name))

  for (let n = 1; ; n++) {
    const candidate = n === 1 ? `${own} (branch)` : `${own} (branch ${n})`

    if (!taken.has(candidate)) {
      return candidate
    }
  }
}

/**
 * Doubles every `#`, since tmux expands `#{...}` formats in `new-window`'s
 * `-c` and `-n` values and `##` is its literal `#`.
 *
 * @param value a directory or window name
 */
export function escapeFormat(value: string): string {
  return value.replaceAll('#', '##')
}

/**
 * Whether `$.session.model()`'s answer can go to `--model` as is: a model id,
 * alias or provider id, optionally with a `[1m]` suffix. A display name
 * (spaces, other brackets) does not, so the fork keeps claude's default.
 *
 * @param model what `$.session.model()` returned
 */
export function isModelFlag(model: string | undefined | null): model is string {
  return typeof model === 'string' && /^[A-Za-z0-9][\w.:/@-]*(\[1m\])?$/.test(model) && model.length <= 200
}

/**
 * The model to pass the fork, or undefined to leave it to its own flags: only
 * a mid-session /model change is carried, since the start model is what the
 * fork resolves to anyway.
 *
 * @param model the session's model now
 * @param startModel the model at `session.start`, undefined when not seen
 */
export function carriedModel(model: string | undefined, startModel: string | undefined): string | undefined {
  return isModelFlag(model) && model !== startModel ? model : undefined
}

/**
 * The forked claude's argv: `claude --resume <id> --fork-session --name
 * <name>`, then `--model` when it changed.
 *
 * @param sessionId the session being forked
 * @param name the fork's session name
 * @param model the model to carry, undefined for none
 */
export function claudeArgv(sessionId: string, name: string, model: string | undefined): string[] {
  const argv = ['claude', '--resume', sessionId, '--fork-session', '--name', name]

  if (model) {
    argv.push('--model', model)
  }

  return argv
}

/** What `tmux new-window` prints for the window it opened, via `-P -F`. */
export const NEW_WINDOW_FORMAT = '#{window_index} #{pane_id}'

/**
 * The `tmux new-window` argv that runs `claude` in a new, current window of
 * the given tmux session. No `-d`, so the person lands in the branch. The
 * claude command stays separate argv entries: tmux execs it without a shell.
 *
 * @param tmuxSession the tmux session id (`$3`) of the pane /branch ran in
 * @param dir the directory the window starts in
 * @param windowName the window's name
 * @param claude the forked claude's argv
 */
export function newWindowArgv(tmuxSession: string, dir: string, windowName: string, claude: readonly string[]): string[] {
  return [
    'tmux',
    'new-window',
    '-P',
    '-F',
    NEW_WINDOW_FORMAT,
    '-t',
    `${tmuxSession}:`,
    '-c',
    escapeFormat(dir),
    '-n',
    escapeFormat(windowName),
    ...claude,
  ]
}

/** The window index and pane id `tmux new-window -P -F` printed. */
export type NewWindow = { window: string; pane: string }

/**
 * Reads `<window_index> <pane_id>` from `tmux new-window`'s output, or null
 * when it printed something else.
 *
 * @param stdout what tmux printed
 */
export function parseNewWindow(stdout: string): NewWindow | null {
  const match = /^(\d+) (%\d+)$/.exec(stdout.trim())

  return match && match[1] && match[2] ? { window: match[1], pane: match[2] } : null
}

/** An argv as one line, quoting the parts that hold spaces or are empty. */
export function commandLine(argv: readonly string[]): string {
  return argv.map(part => (/\s/.test(part) || part === '' ? `"${part}"` : part)).join(' ')
}

/**
 * What a failed tmux call said, for the reply: its stderr, else its stdout,
 * else its exit code.
 *
 * @param run the call's exit code and output
 */
export function failure(run: { exitCode: number; stdout: string; stderr: string }): string {
  return run.stderr.trim() || run.stdout.trim() || `exit code ${run.exitCode}`
}
