// Package procwalk ports packages/runtime/src/agents/resolve-agent-pid.ts
// and resolve-session-by-pid.ts — pure process-tree walkers.
//
// Claude Code dispatches hooks via `sh -c "<hook command>"`, so the hook
// script's $PPID points at a wrapper shell that exits the moment the hook
// returns. ResolveAgentSessionPid walks ancestry through a ps snapshot to
// the first ancestor matching the agent binary — the pid that lives for the
// full session and that liveness sweeps can trust.
//
// ResolveSessionByPid routes a hook to its tmux session by walking the same
// ancestry until it crosses a pane shell pid, replacing the cwd resolver
// whose active-pane keying silently mis-routes when the user navigates away.
//
// Pure functions only — no shell, no tmux. Callers supply the snapshots.
package procwalk

import (
	"regexp"
	"strconv"
	"strings"
)

// ProcInfo is one process from a `ps -axww -o pid=,ppid=,command=` snapshot.
type ProcInfo struct {
	PID     int
	PPID    int
	Command string
}

var snapshotLineRE = regexp.MustCompile(`^(\d+)\s+(\d+)\s+(.*)$`)

// ParseProcessSnapshot parses `ps -axww -o pid=,ppid=,command=` output.
// Malformed lines and non-positive pids are skipped.
func ParseProcessSnapshot(snapshot string) map[int]ProcInfo {
	m := map[int]ProcInfo{}
	for line := range strings.SplitSeq(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		match := snapshotLineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		pid, err1 := strconv.Atoi(match[1])
		ppid, err2 := strconv.Atoi(match[2])
		if err1 != nil || err2 != nil || pid <= 0 || ppid < 0 {
			continue
		}
		m[pid] = ProcInfo{PID: pid, PPID: ppid, Command: match[3]}
	}
	return m
}

// ResolveAgentSessionPid walks up the parent chain from reportedPid,
// returning the first ancestor whose command matches pattern. Returns the
// input pid unchanged if no match is found — graceful degradation.
// Cycle-safe via a seen-set.
func ResolveAgentSessionPid(reportedPid int, pattern *regexp.Regexp, snapshot map[int]ProcInfo) int {
	if reportedPid <= 1 {
		return reportedPid
	}
	seen := map[int]bool{}
	current := reportedPid
	for current > 1 && !seen[current] {
		info, ok := snapshot[current]
		if !ok {
			return reportedPid
		}
		if pattern.MatchString(info.Command) {
			return current
		}
		seen[current] = true
		if info.PPID == current || info.PPID <= 1 {
			return reportedPid
		}
		current = info.PPID
	}
	return reportedPid
}

// ResolveSessionByPid walks up from targetPid through the snapshot's parent
// chain, returning the session of the first pid (targetPid included) found
// in panePidIndex. Returns "" when the target is not in the snapshot, the
// chain reaches pid 1/0 or a cycle without crossing a pane pid, or the
// target is non-positive.
func ResolveSessionByPid(targetPid int, panePidIndex map[int]string, snapshot map[int]ProcInfo) string {
	if targetPid <= 1 {
		return ""
	}
	seen := map[int]bool{}
	current := targetPid
	for current > 1 && !seen[current] {
		if session, ok := panePidIndex[current]; ok {
			return session
		}
		info, ok := snapshot[current]
		if !ok {
			return ""
		}
		seen[current] = true
		if info.PPID == current || info.PPID <= 1 {
			return ""
		}
		current = info.PPID
	}
	return ""
}
