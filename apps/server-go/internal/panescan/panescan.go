// Package panescan ports the bun server's pane agent scanner
// (server/index.ts buildProcessTree / matchProcessTreeFast /
// scanAllTmuxPaneAgents): every PANE_SCAN_INTERVAL it lists all tmux panes,
// builds one process tree from two ps snapshots, and reports which panes
// host a live agent process. The tracker folds the result in via
// ApplyPanePresence.
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

// HasCommPatterns reports whether an agent name has a comm-pattern entry
// (the kill-agent-pane pid-verification gate needs this).
func HasCommPatterns(agent string) bool {
	for _, p := range AgentCommPatterns {
		if p.Name == agent {
			return true
		}
	}
	return false
}

const (
	sidebarMarkerValue = "1"
	sidebarPaneTitle   = "tcm-sidebar"
	stashSession       = "_tcm_stash"
	maxTreeDepth       = 2
)

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

// matchTree walks up to 3 levels of child processes and returns the matched
// child pid (the agent process itself), or 0. Comm patterns first, then the
// AgentFromCommand fallback for runtime/wrapper comms — which only knows
// pi and claude-code, preserving comm-only behavior for amp/codex/opencode.
func matchTree(pid int, patterns []string, agentName string, tree processTree, depth int) int {
	if depth > maxTreeDepth {
		return 0
	}
	for _, childPid := range tree.childrenOf[pid] {
		comm := tree.commOf[childPid]
		for _, pat := range patterns {
			if comm != "" && agentmatch.CommMatches(comm, pat) {
				return childPid
			}
		}
		if agentmatch.AgentFromCommand(comm, tree.cmdlineOf[childPid]) == agentName {
			return childPid
		}
		if deeper := matchTree(childPid, patterns, agentName, tree, depth+1); deeper != 0 {
			return deeper
		}
	}
	return 0
}

// pane is one parsed list-panes row.
type pane struct {
	session     string
	id          string
	pid         int
	windowIndex int
	paneIndex   int
	sidebar     bool
	title       string
}

// Scan lists all panes across all tmux sessions and identifies running
// agents, keyed by session name. Sidebar panes (the @tcm-sidebar marker or
// the legacy tcm-sidebar title) and the stash session are excluded.
func (s *Scanner) Scan() map[string][]tracker.PanePresence {
	result := map[string][]tracker.PanePresence{}

	raw, err := s.Run("tmux", "list-panes", "-a", "-F",
		"#{session_name}|#{pane_id}|#{pane_pid}|#{window_index}|#{pane_index}|#{@tcm-sidebar}|#{pane_title}")
	if err != nil || strings.TrimSpace(raw) == "" {
		return result
	}

	var panes []pane
	for line := range strings.SplitSeq(raw, "\n") {
		if line == "" {
			continue
		}
		// Title is last on purpose: it is the only field that can contain
		// the separator, so split a bounded number of times.
		fields := strings.SplitN(line, "|", 7)
		if len(fields) != 7 {
			continue
		}
		pid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		wi, wiErr := strconv.Atoi(fields[3])
		pi, piErr := strconv.Atoi(fields[4])
		p := pane{
			session: fields[0],
			id:      fields[1],
			pid:     pid,
			sidebar: fields[5] == sidebarMarkerValue || fields[6] == sidebarPaneTitle,
			title:   fields[6],
		}
		if wiErr == nil {
			p.windowIndex = wi
		} else {
			p.windowIndex = -1
		}
		if piErr == nil {
			p.paneIndex = pi
		} else {
			p.paneIndex = -1
		}
		panes = append(panes, p)
	}

	tree := s.buildProcessTree()

	for _, p := range panes {
		if p.sidebar || p.session == stashSession {
			continue
		}
		for _, ap := range AgentCommPatterns {
			// Process-tree matching only — title matching produces false
			// positives (a thread named "Detect Claude session names").
			agentPid := matchTree(p.pid, ap.Patterns, ap.Name, tree, 0)
			if agentPid == 0 {
				continue
			}
			pp := tracker.PanePresence{
				Agent:     ap.Name,
				PaneID:    p.id,
				PID:       agentPid,
				PaneTitle: p.title,
			}
			if p.windowIndex >= 0 {
				wi := p.windowIndex
				pp.WindowIndex = &wi
			}
			if p.paneIndex >= 0 {
				pi := p.paneIndex
				pp.PaneIndex = &pi
			}
			result[p.session] = append(result[p.session], pp)
			break // one agent per pane — first match wins
		}
	}

	return result
}
