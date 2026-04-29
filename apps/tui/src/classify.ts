/**
 * Message → verb classifier for the activity-zone verb-glyph column.
 *
 * The runtime emits log entries with a free-form `message` string but no
 * `verb` tag (the producer-side `verb?` recommendation in
 * docs/simmer/activity-zone/result.md is forward-compatible — see §Verb-glyph
 * column rule (b) — but until the producer ships it, the renderer derives
 * the verb client-side via the regex classifier below).
 *
 * Ten verbs match the closed dictionary in vocab.ts:
 *
 *   read     — "Reading file.ts", "Read package.json"
 *   list     — "Listing src", "Listing dir"
 *   search   — "Searching for foo", "Grep ...", "Glob ..."
 *   edit     — "Editing file.ts", "Wrote ...", "Patched ..."
 *   run      — "ran bun test", "Running pytest", "Bash ..."
 *   web      — "WebFetch ...", "Fetching ...", "https://..."
 *   task     — "Task ...", "Agent ...", "Delegating ..."
 *   skill    — "Skill ...", "Invoking skill ..."
 *   thinking — "Thinking ...", "Reasoning ..."
 *   error    — "Error: ...", "Failed to ..."
 *
 * Misclassification degrades gracefully: rule 4 in §Verb-glyph column renders
 * a single space when the classifier returns undefined. Gaps in the verb
 * stripe are themselves informative (status transitions, [bell], non-tool
 * events) so we deliberately *don't* invent a fallback glyph here.
 *
 * Add a regex when an agent watcher emits a new common message shape; do
 * not invent regexes speculatively. Each regex is anchored at start to
 * avoid mid-string accidents (`Reading the docs` is a read; `I tried
 * reading docs` is not).
 */

export type Verb =
  | "read"
  | "list"
  | "search"
  | "edit"
  | "run"
  | "web"
  | "task"
  | "skill"
  | "thinking"
  | "error";

interface Rule {
  re: RegExp;
  verb: Verb;
}

// Order matters only for ambiguous prefixes; the regexes below are mutually
// distinct so the first match always wins cleanly.
const RULES: Rule[] = [
  { re: /^reading\b/i, verb: "read" },
  { re: /^read\b/i, verb: "read" },

  { re: /^listing\b/i, verb: "list" },
  { re: /^ls\b/i, verb: "list" },

  { re: /^searching\b/i, verb: "search" },
  { re: /^grep\b/i, verb: "search" },
  { re: /^glob\b/i, verb: "search" },
  { re: /^find\b/i, verb: "search" },

  { re: /^editing\b/i, verb: "edit" },
  { re: /^edit\b/i, verb: "edit" },
  { re: /^wrote\b/i, verb: "edit" },
  { re: /^writing\b/i, verb: "edit" },
  { re: /^patching\b/i, verb: "edit" },
  { re: /^patched\b/i, verb: "edit" },

  { re: /^ran\b/i, verb: "run" },
  { re: /^running\b/i, verb: "run" },
  { re: /^run\b/i, verb: "run" },
  { re: /^bash\b/i, verb: "run" },
  { re: /^executing\b/i, verb: "run" },

  { re: /^web(fetch|search)?\b/i, verb: "web" },
  { re: /^fetching\b/i, verb: "web" },
  { re: /^https?:\/\//i, verb: "web" },

  { re: /^task\b/i, verb: "task" },
  { re: /^agent\b/i, verb: "task" },
  { re: /^delegat(ing|ed)\b/i, verb: "task" },
  { re: /^spawn(ing|ed)\b/i, verb: "task" },

  { re: /^skill\b/i, verb: "skill" },
  { re: /^invoking skill\b/i, verb: "skill" },

  { re: /^thinking\b/i, verb: "thinking" },
  { re: /^reasoning\b/i, verb: "thinking" },

  // Error rules use a colon or word-boundary so we don't false-match
  // "erroneously", "failed login" in a status sentence, etc.
  { re: /^error[:\s]/i, verb: "error" },
  { re: /^failed\s+to\b/i, verb: "error" },
];

/**
 * Classify a log entry's message by its verb prefix.
 *
 * Returns one of the five verbs in the closed dictionary, or undefined if
 * no rule matches. Undefined rows render column 1 blank (rule 4 in
 * §Verb-glyph column).
 */
export function classifyVerb(message: string): Verb | undefined {
  const trimmed = message.trimStart();
  for (const { re, verb } of RULES) {
    if (re.test(trimmed)) return verb;
  }
  return undefined;
}
