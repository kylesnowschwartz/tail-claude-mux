/**
 * Pid-based session resolution.
 *
 * The cwd-based resolver (server/index.ts `resolveSession`) keys on each tmux
 * session's `s.dir`, which is the active pane's cwd. When the active pane has
 * navigated to a directory unrelated to where an agent in the same session
 * was launched, the cwd → session lookup misses and the hook is silently
 * dropped. That failure mode is what this module fixes.
 *
 * The replacement routes by pid: every hook payload carries the agent's pid;
 * we walk upward through the process tree until we hit a pid that matches
 * some tmux pane's shell pid, and attribute the hook to that pane's session.
 *
 * Pure functions only — no shell, no tmux. The server wires this together
 * with live snapshots and caches the index for a few seconds, the same shape
 * as `getDirSessionMap` does for the cwd resolver.
 */

import type { ProcInfo } from "./resolve-agent-pid";

/** Build a pane shell pid → tmux session-name index from `tmux list-panes -a`
 *  output in the format `#{session_name}|#{pane_pid}` (one pane per line).
 *  Rejects blank lines, malformed entries, and non-positive pids — the latter
 *  silently because they cannot be a real ancestor of anything. */
export function buildPanePidIndex(listPanesOutput: string): Map<number, string> {
  const map = new Map<number, string>();
  for (const rawLine of listPanesOutput.split("\n")) {
    const line = rawLine.trim();
    if (line.length === 0) continue;
    const sep = line.indexOf("|");
    if (sep < 0) continue;
    const session = line.slice(0, sep);
    const pidStr = line.slice(sep + 1).trim();
    const pid = Number(pidStr);
    if (!Number.isInteger(pid) || pid <= 0) continue;
    if (session.length === 0) continue;
    map.set(pid, session);
  }
  return map;
}

/** Walk up from `targetPid` through `snapshot`'s parent chain, returning the
 *  session of the first ancestor (or `targetPid` itself) that matches a pid
 *  in `panePidIndex`. Returns null when:
 *   - `targetPid` is not in the snapshot (process exited, or snapshot is stale)
 *   - the chain reaches pid 1 / 0 / a self-cycle without crossing any pane pid
 *   - `targetPid` is non-positive / NaN
 *
 *  Cycle-safe via a seen-set; bounded by the natural reach-init-or-1 termination.
 */
export function resolveSessionByPid(
  targetPid: number,
  panePidIndex: ReadonlyMap<number, string>,
  snapshot: ReadonlyMap<number, ProcInfo>,
): string | null {
  if (!Number.isFinite(targetPid) || !Number.isInteger(targetPid) || targetPid <= 1) {
    return null;
  }

  const seen = new Set<number>();
  let current = targetPid;
  while (current > 1 && !seen.has(current)) {
    const hit = panePidIndex.get(current);
    if (hit !== undefined) return hit;
    const info = snapshot.get(current);
    if (!info) return null;
    seen.add(current);
    if (info.ppid === current || info.ppid <= 1) return null;
    current = info.ppid;
  }
  return null;
}
