/**
 * Claude Code emits its turn state in the terminal title via OSC sequences:
 * a leading braille spinner glyph (U+2800–U+28FF) while a turn is in flight,
 * and a leading sparkle (U+2733 "✳") when idle at the prompt. tmux decodes the
 * OSC title into `pane_title`, which the pane scanner already captures — so we
 * classify the decoded string directly, no OSC byte parsing needed.
 *
 * Source of truth: herdr `src/detect/manifests/claude.toml`
 *   - `osc_title_working`: regex `^[\x{2800}-\x{28FF}] ` → working
 *   - `osc_title_idle`:    regex `^\x{2733} `           → idle
 *
 * Pure and side-effect free. Used only to *fill gaps* left by the authoritative
 * `~/.claude/sessions/<pid>.json` probe (see claude-code-hooks.probeLiveStatus):
 * the session file always wins; this supplies a verdict when the file is null
 * (sdk-cli / absent). It never overrides a definitive file verdict, so a
 * mid-turn title can't manufacture a false "ended".
 */

const BRAILLE_START = 0x2800;
const BRAILLE_END = 0x28ff;
const SPARKLE = 0x2733;

/** Classify a decoded tmux `pane_title` into a Claude turn-state verdict.
 *    - leading braille glyph (U+2800–U+28FF) → "working"
 *    - leading sparkle (U+2733)              → "ended"
 *    - anything else (plain title, empty)    → null (no signal)
 *  The verdict vocabulary ("working" | "ended") matches the watcher probe
 *  contract so callers can combine the two with a single `??`. */
export function classifyTitleStatus(title: string): "working" | "ended" | null {
  if (!title) return null;
  const cp = title.codePointAt(0);
  if (cp === undefined) return null;
  if (cp >= BRAILLE_START && cp <= BRAILLE_END) return "working";
  if (cp === SPARKLE) return "ended";
  return null;
}
