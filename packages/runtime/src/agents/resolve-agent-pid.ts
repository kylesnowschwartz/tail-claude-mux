/**
 * Ancestor walker for resolving the long-lived agent PID.
 *
 * Claude Code dispatches hooks via `sh -c "<hook command>"`, so the hook
 * script sees `$PPID` pointing at the wrapper shell — a short-lived process
 * that exits the moment the hook returns. Using that pid for liveness
 * checking would mark every session ended within seconds of every hook.
 *
 * The fix: ship a `ps` snapshot from the hook (`ps -axww -o pid=,ppid=,command=`)
 * and walk ancestry on the server until we find the first ancestor whose
 * command line matches the agent binary. That pid lives for the full
 * session and is what the tracker's liveness sweep should track.
 *
 * Returns the input pid unchanged when no matching ancestor is found —
 * graceful degradation. Pi doesn't need this (the extension runs in-process
 * and reports its own pid directly).
 */

export interface ProcInfo {
  pid: number;
  ppid: number;
  command: string;
}

/** Parse `ps -axww -o pid=,ppid=,command=` output. Each line is
 *  whitespace-prefixed pid + ppid + a free-form command. */
export function parseProcessSnapshot(snapshot: string): Map<number, ProcInfo> {
  const map = new Map<number, ProcInfo>();
  for (const rawLine of snapshot.split("\n")) {
    const line = rawLine.trim();
    if (line.length === 0) continue;
    // First two tokens are pid and ppid; the rest is the command. The macOS
    // `ps` indents pids in a fixed-width field, so trim+split-on-whitespace
    // (with a limit of 3) recovers them robustly.
    const match = line.match(/^(\d+)\s+(\d+)\s+(.*)$/);
    if (!match) continue;
    const pid = Number(match[1]);
    const ppid = Number(match[2]);
    const command = match[3] ?? "";
    if (!Number.isInteger(pid) || pid <= 0) continue;
    if (!Number.isInteger(ppid) || ppid < 0) continue;
    map.set(pid, { pid, ppid, command });
  }
  return map;
}

/**
 * Walk up the parent chain from `pid`, returning the first ancestor whose
 * command matches `pattern`. Returns the input pid unchanged if no match is
 * found within the chain, or if any precondition is violated.
 *
 * Cycle-safe: bounded by a seen-set and by the natural reach-init-or-1
 * termination.
 */
export function resolveAgentSessionPid(
  reportedPid: number,
  pattern: RegExp,
  snapshot: ReadonlyMap<number, ProcInfo>,
): number {
  if (!Number.isInteger(reportedPid) || reportedPid <= 1) return reportedPid;

  const seen = new Set<number>();
  let current = reportedPid;
  while (current > 1 && !seen.has(current)) {
    const info = snapshot.get(current);
    if (!info) return reportedPid;
    if (pattern.test(info.command)) return current;
    seen.add(current);
    if (info.ppid === current || info.ppid <= 1) return reportedPid;
    current = info.ppid;
  }
  return reportedPid;
}

/** Convenience: walk + parse-snapshot in one call. */
export function resolveAgentSessionPidFromSnapshot(
  reportedPid: number,
  pattern: RegExp,
  snapshot: string,
): number {
  return resolveAgentSessionPid(reportedPid, pattern, parseProcessSnapshot(snapshot));
}
