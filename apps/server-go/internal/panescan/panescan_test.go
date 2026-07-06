// Unit tests for the pane agent scanner. No TS reference suite exists; these
// exercise the Scan contract via the injectable Exec against canned ps
// output, mirroring the behavior of server/index.ts buildProcessTree /
// matchProcessTreeFast / scanAllTmuxPaneAgents. The pane listing itself is
// the caller's job (tmux.ListAllPanes) and is passed in as literals here.
package panescan

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
)

// fakeExec returns an Exec answering the scanner's two ps commands with
// canned output. Any other command is an error so contract drift is loud.
func fakeExec(t *testing.T, psComm, psFull string) Exec {
	t.Helper()
	return func(name string, args ...string) (string, error) {
		joined := name + " " + strings.Join(args, " ")
		switch joined {
		case "ps -eo pid=,ppid=,comm=":
			return psComm, nil
		case "ps -axww -o pid=,ppid=,command=":
			return psFull, nil
		default:
			t.Errorf("unexpected command: %s", joined)
			return "", fmt.Errorf("unexpected command: %s", joined)
		}
	}
}

// workPane is the default single-pane input: session "work", pane %1,
// shell pid 100.
func workPane() []tmux.Pane {
	return []tmux.Pane{{Session: "work", ID: "%1", PID: 100, WindowIndex: 0, PaneIndex: 0, Title: "zsh"}}
}

// commLine builds one `ps -eo pid=,ppid=,comm=` row (leading-space padded,
// as real ps emits).
func commLine(pid, ppid int, comm string) string {
	return fmt.Sprintf("  %d %d %s", pid, ppid, comm)
}

// fullLine builds one `ps -axww -o pid=,ppid=,command=` row.
func fullLine(pid, ppid int, command string) string {
	return fmt.Sprintf("%d %d %s", pid, ppid, command)
}

func onlyPresence(t *testing.T, result map[string][]tracker.PanePresence, session string) tracker.PanePresence {
	t.Helper()
	if len(result) != 1 {
		t.Fatalf("result has %d sessions, want 1: %#v", len(result), result)
	}
	agents := result[session]
	if len(agents) != 1 {
		t.Fatalf("result[%q] has %d agents, want 1: %#v", session, len(agents), agents)
	}
	return agents[0]
}

func TestScanReportsClaudeChildWithChildPid(t *testing.T) {
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(200, 100, "claude"),
	}, "\n")
	psFull := strings.Join([]string{
		fullLine(100, 1, "-zsh"),
		fullLine(200, 100, "claude --resume"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, psFull)}
	got := onlyPresence(t, s.Scan(workPane()), "work")

	if got.Agent != "claude-code" {
		t.Errorf("Agent = %q, want %q", got.Agent, "claude-code")
	}
	if got.PaneID != "%1" {
		t.Errorf("PaneID = %q, want %q", got.PaneID, "%1")
	}
	if got.PID != 200 {
		t.Errorf("PID = %d, want the matched child pid 200 (not the pane pid)", got.PID)
	}
}

func TestScanReportsPaneRootAgent(t *testing.T) {
	// `tmux new-window 'claude'`: the shell execs away and the agent IS the
	// pane root — no shell parent in the tree at all.
	psComm := commLine(100, 1, "claude")
	psFull := fullLine(100, 1, "/Users/x/.local/bin/claude")

	s := &Scanner{Run: fakeExec(t, psComm, psFull)}
	got := onlyPresence(t, s.Scan(workPane()), "work")

	if got.Agent != "claude-code" {
		t.Errorf("Agent = %q, want %q", got.Agent, "claude-code")
	}
	if got.PID != 100 {
		t.Errorf("PID = %d, want the pane-root pid 100", got.PID)
	}
}

func TestAgentPidsByPaneIncludesPaneRoot(t *testing.T) {
	// Pane-root claude with a Task-spawned subagent child: both pids.
	psComm := strings.Join([]string{
		commLine(100, 1, "claude"),
		commLine(200, 100, "claude"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, "")}
	pids := s.AgentPidsByPane(workPane(), "claude-code")["%1"]
	if len(pids) != 2 || pids[0] != 100 || pids[1] != 200 {
		t.Errorf("pids = %v, want [100 200]", pids)
	}
}

func TestScanDepthLimit(t *testing.T) {
	t.Run("match three levels deep is found", func(t *testing.T) {
		psComm := strings.Join([]string{
			commLine(100, 1, "zsh"),
			commLine(200, 100, "bash"),   // level 1
			commLine(300, 200, "sh"),     // level 2
			commLine(400, 300, "claude"), // level 3 — deepest reachable
		}, "\n")
		s := &Scanner{Run: fakeExec(t, psComm, "")}
		got := onlyPresence(t, s.Scan(workPane()), "work")
		if got.Agent != "claude-code" || got.PID != 400 {
			t.Errorf("got agent %q pid %d, want claude-code pid 400", got.Agent, got.PID)
		}
	})

	t.Run("match four levels deep is not found", func(t *testing.T) {
		psComm := strings.Join([]string{
			commLine(100, 1, "zsh"),
			commLine(200, 100, "bash"),   // level 1
			commLine(300, 200, "sh"),     // level 2
			commLine(400, 300, "sh"),     // level 3
			commLine(500, 400, "claude"), // level 4 — beyond maxTreeDepth
		}, "\n")
		s := &Scanner{Run: fakeExec(t, psComm, "")}
		if got := s.Scan(workPane()); len(got) != 0 {
			t.Errorf("Scan() = %#v, want empty (match beyond depth limit)", got)
		}
	})
}

func TestScanExcludesSidebarAndStashPanes(t *testing.T) {
	panes := []tmux.Pane{
		{Session: "work", ID: "%1", PID: 100, Sidebar: true},                // @tcm-sidebar / legacy title, per tmux.ListAllPanes
		{Session: tmux.StashSession, ID: "%3", PID: 120},                    // stash session
		{Session: "work", ID: "%4", PID: 130, Title: "an honest work pane"}, // kept
	}
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(101, 100, "claude"),
		commLine(120, 1, "zsh"),
		commLine(121, 120, "claude"),
		commLine(130, 1, "zsh"),
		commLine(131, 130, "claude"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, "")}
	got := onlyPresence(t, s.Scan(panes), "work")

	if got.PaneID != "%4" || got.PID != 131 {
		t.Errorf("got pane %q pid %d, want only the non-sidebar pane %%4 pid 131", got.PaneID, got.PID)
	}
}

func TestScanAgentFromCommandFallback(t *testing.T) {
	// comm is the runtime ("node"); identity lives in the full command line.
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(200, 100, "node"),
	}, "\n")
	psFull := strings.Join([]string{
		fullLine(100, 1, "-zsh"),
		fullLine(200, 100, "node /Users/kyle/.npm-global/lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, psFull)}
	got := onlyPresence(t, s.Scan(workPane()), "work")

	if got.Agent != "pi" {
		t.Errorf("Agent = %q, want %q via AgentFromCommand fallback", got.Agent, "pi")
	}
	if got.PID != 200 {
		t.Errorf("PID = %d, want 200", got.PID)
	}
}

func TestScanPipCommDoesNotMatchPi(t *testing.T) {
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(200, 100, "pip"),
	}, "\n")
	psFull := strings.Join([]string{
		fullLine(100, 1, "-zsh"),
		fullLine(200, 100, "/usr/bin/pip install requests"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, psFull)}
	if got := s.Scan(workPane()); len(got) != 0 {
		t.Errorf("Scan() = %#v, want empty ('pip' must not match agent 'pi')", got)
	}
}

func TestScanFirstPatternOrderWinsOneAgentPerPane(t *testing.T) {
	// Two agent children under one pane: claude listed first in ps, but amp
	// precedes claude-code in AgentCommPatterns, so amp must win.
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(200, 100, "claude"),
		commLine(300, 100, "amp"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, "")}
	got := onlyPresence(t, s.Scan(workPane()), "work")

	if got.Agent != "amp" || got.PID != 300 {
		t.Errorf("got agent %q pid %d, want amp pid 300 (pattern order, one agent per pane)", got.Agent, got.PID)
	}
}

func TestScanCarriesIndicesAndTitle(t *testing.T) {
	panes := []tmux.Pane{{Session: "work", ID: "%7", PID: 100, WindowIndex: 3, PaneIndex: 1, Title: "my pane title"}}
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(200, 100, "claude"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, "")}
	got := onlyPresence(t, s.Scan(panes), "work")

	if got.WindowIndex == nil || *got.WindowIndex != 3 {
		t.Errorf("WindowIndex = %v, want *3", got.WindowIndex)
	}
	if got.PaneIndex == nil || *got.PaneIndex != 1 {
		t.Errorf("PaneIndex = %v, want *1", got.PaneIndex)
	}
	if got.PaneTitle != "my pane title" {
		t.Errorf("PaneTitle = %q, want %q", got.PaneTitle, "my pane title")
	}
}

func TestScanNegativeIndicesBecomeNil(t *testing.T) {
	// tmux.ListAllPanes encodes unparseable indices as -1.
	panes := []tmux.Pane{{Session: "work", ID: "%7", PID: 100, WindowIndex: -1, PaneIndex: -1, Title: "t"}}
	psComm := strings.Join([]string{
		commLine(100, 1, "zsh"),
		commLine(200, 100, "claude"),
	}, "\n")

	s := &Scanner{Run: fakeExec(t, psComm, "")}
	got := onlyPresence(t, s.Scan(panes), "work")

	if got.WindowIndex != nil {
		t.Errorf("WindowIndex = %v, want nil for unparseable index", *got.WindowIndex)
	}
	if got.PaneIndex != nil {
		t.Errorf("PaneIndex = %v, want nil for unparseable index", *got.PaneIndex)
	}
}

func TestScanEmptyPaneListSkipsPs(t *testing.T) {
	run := func(name string, args ...string) (string, error) {
		t.Errorf("ps must not be invoked when there are no panes, got: %s %s", name, strings.Join(args, " "))
		return "", nil
	}
	s := &Scanner{Run: run}
	if got := s.Scan(nil); len(got) != 0 {
		t.Errorf("Scan(nil) = %#v, want empty map", got)
	}
	if got := s.Scan([]tmux.Pane{}); len(got) != 0 {
		t.Errorf("Scan([]) = %#v, want empty map", got)
	}
}
