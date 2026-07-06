// Package panescan ports the bun server's pane agent scanner
// (server/index.ts buildProcessTree / matchProcessTreeFast /
// scanAllTmuxPaneAgents): every PANE_SCAN_INTERVAL the server hands it the
// tmux pane listing, it builds one process tree from two ps snapshots, and
// reports which panes host a live agent process. The tracker folds the
// result in via ApplyPanePresence.
//
// The scanner returns only {agent, paneId, pid, indices, title} — watchers
// remain the single source of truth for threadId, status, and thread names.
package panescan

import (
	"os/exec"
	"strconv"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/agentmatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/procwalk"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
)

// AgentPattern is one AGENT_COMM_PATTERNS entry. Order matters: parents
// precede child tools, and the first match per pane wins.
type AgentPattern struct {
	Name     string
	Patterns []string
}

// AgentCommPatterns mirrors AGENT_COMM_PATTERNS in server/index.ts.
var AgentCommPatterns = []AgentPattern{
	{Name: "amp", Patterns: []string{"amp"}},
	{Name: "claude-code", Patterns: []string{"claude"}},
	{Name: "codex", Patterns: []string{"codex"}},
	{Name: "opencode", Patterns: []string{"opencode"}},
	{Name: "pi", Patterns: []string{"pi"}},
}

const maxTreeDepth = 2

// Exec runs a command and returns its stdout. Injectable for tests.
type Exec func(name string, args ...string) (string, error)

// DefaultExec runs the real binary.
func DefaultExec(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// Scanner scans tmux panes for agent processes.
type Scanner struct {
	Run Exec
}

// New returns a Scanner backed by real tmux/ps.
func New() *Scanner { return &Scanner{Run: DefaultExec} }

// processTree is buildProcessTree's result: parent→children plus comm and
// full command line per pid, from two ps passes (`comm=` for the fast
// basename path, `command=` for the AgentFromCommand fallback).
type processTree struct {
	childrenOf map[int][]int
	commOf     map[int]string
	cmdlineOf  map[int]string
}

func (s *Scanner) buildProcessTree() processTree {
	tree := processTree{
		childrenOf: map[int][]int{},
		commOf:     map[int]string{},
		cmdlineOf:  map[int]string{},
	}
	out, err := s.Run("ps", "-eo", "pid=,ppid=,comm=")
	if err == nil {
		for line := range strings.SplitSeq(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			pid, err1 := strconv.Atoi(fields[0])
			ppid, err2 := strconv.Atoi(fields[1])
			if err1 != nil || err2 != nil {
				continue
			}
			tree.commOf[pid] = strings.ToLower(strings.Join(fields[2:], " "))
			tree.childrenOf[ppid] = append(tree.childrenOf[ppid], pid)
		}
	}
	snap, err := s.Run("ps", "-axww", "-o", "pid=,ppid=,command=")
	if err == nil {
		for pid, info := range procwalk.ParseProcessSnapshot(snap) {
			tree.cmdlineOf[pid] = info.Command
		}
	}
	return tree
}

// pidMatchesAgent reports whether one pid's process matches the agent —
// comm patterns first, then the AgentFromCommand wrapper fallback (which
// only knows pi and claude-code, preserving comm-only behavior for
// amp/codex/opencode).
func pidMatchesAgent(pid int, patterns []string, agentName string, tree processTree) bool {
	comm := tree.commOf[pid]
	for _, pat := range patterns {
		if comm != "" && agentmatch.CommMatches(comm, pat) {
			return true
		}
	}
	return agentmatch.AgentFromCommand(comm, tree.cmdlineOf[pid]) == agentName
}

// matchTree tests the pane root itself, then walks up to 3 levels of child
// processes; returns the matched pid (the agent process itself), or 0. The
// root check matters for `tmux new-window 'claude'`-style panes where the
// shell execs away and the agent IS the pane root — skipping it makes such
// agents invisible to the scanner (no synthetic row, seed-ghost exiting).
func matchTree(pid int, patterns []string, agentName string, tree processTree, depth int) int {
	if depth == 0 && pidMatchesAgent(pid, patterns, agentName, tree) {
		return pid
	}
	if depth > maxTreeDepth {
		return 0
	}
	for _, childPid := range tree.childrenOf[pid] {
		if pidMatchesAgent(childPid, patterns, agentName, tree) {
			return childPid
		}
		if deeper := matchTree(childPid, patterns, agentName, tree, depth+1); deeper != 0 {
			return deeper
		}
	}
	return 0
}

// KnownAgent reports whether name has an AgentCommPatterns entry. The
// kill gate checks this before pid verification: an unknown agent yields
// no matches from AgentPidsByPane, and refusing on that empty set would
// mislabel "unverifiable" as a pid mismatch.
func KnownAgent(name string) bool {
	for _, ap := range AgentCommPatterns {
		if ap.Name == name {
			return true
		}
	}
	return false
}

// AgentPidsByPane returns, per pane id, every descendant pid (depth ≤
// maxTreeDepth) matching agentName — comm boundary rules plus the
// AgentFromCommand wrapper fallback, the same matcher Scan discovers
// agents with. (The bun original verifies by comm only; using the
// discovery matcher here means wrapper-launched agents like node-wrapped
// pi verify by the rules that found them.) Fresh ps snapshots on every
// call, on purpose: pane contents mutate on pane recycling, and the kill
// gate exists to catch exactly that staleness.
func (s *Scanner) AgentPidsByPane(panes []tmux.Pane, agentName string) map[string][]int {
	out := map[string][]int{}
	var patterns []string
	for _, ap := range AgentCommPatterns {
		if ap.Name == agentName {
			patterns = ap.Patterns
			break
		}
	}
	if patterns == nil || len(panes) == 0 {
		return out
	}
	tree := s.buildProcessTree()
	for _, p := range panes {
		var pids []int
		// Same pane-root check as matchTree: an exec'd agent IS the root.
		if pidMatchesAgent(p.PID, patterns, agentName, tree) {
			pids = append(pids, p.PID)
		}
		collectAgentPids(p.PID, patterns, agentName, tree, 0, &pids)
		out[p.ID] = pids
	}
	return out
}

// collectAgentPids is matchTree's collect-all sibling: it appends every
// matching descendant instead of stopping at the first, and keeps
// recursing below matches — a claude pane hosts parent + Task-spawned
// subagent as parent/child, and the resolver must see both.
func collectAgentPids(pid int, patterns []string, agentName string, tree processTree, depth int, acc *[]int) {
	if depth > maxTreeDepth {
		return
	}
	for _, childPid := range tree.childrenOf[pid] {
		if pidMatchesAgent(childPid, patterns, agentName, tree) {
			*acc = append(*acc, childPid)
		}
		collectAgentPids(childPid, patterns, agentName, tree, depth+1, acc)
	}
}

// Scan identifies running agents in the given panes, keyed by session
// name. tcm-managed panes and the stash session are excluded. The pane listing
// comes from the caller (tmux.ListAllPanes) so one tmux exec serves every
// consumer; the scanner spends its own execs on the two ps snapshots.
func (s *Scanner) Scan(panes []tmux.Pane) map[string][]tracker.PanePresence {
	result := map[string][]tracker.PanePresence{}
	if len(panes) == 0 {
		return result
	}

	tree := s.buildProcessTree()

	for _, p := range panes {
		if p.Managed() || p.Session == tmux.StashSession {
			continue
		}
		for _, ap := range AgentCommPatterns {
			// Process-tree matching only — title matching produces false
			// positives (a thread named "Detect Claude session names").
			agentPid := matchTree(p.PID, ap.Patterns, ap.Name, tree, 0)
			if agentPid == 0 {
				continue
			}
			pp := tracker.PanePresence{
				Agent:     ap.Name,
				PaneID:    p.ID,
				PID:       agentPid,
				PaneTitle: p.Title,
			}
			if p.WindowIndex >= 0 {
				wi := p.WindowIndex
				pp.WindowIndex = &wi
			}
			if p.PaneIndex >= 0 {
				pi := p.PaneIndex
				pp.PaneIndex = &pi
			}
			result[p.Session] = append(result[p.Session], pp)
			break // one agent per pane — first match wins
		}
	}

	return result
}
