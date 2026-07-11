// Agent-pane resolution and the kill gate: resolveAgentPaneId /
// killAgentPane from packages/runtime/src/server/index.ts. The codex
// (bun:sqlite) and opencode (lsof) resolvers are deliberately not ported —
// verified dead code (no watcher emits a threadId their gates require; see
// SCOPING-go-backend.md Decisions).
package server

import (
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/panescan"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

type agentResolutionError struct {
	status  int
	message string
}

func (s *Server) resolveTrackedEvent(session, threadID, paneID string) (*wire.AgentEvent, *agentResolutionError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Tracker == nil {
		if threadID != "" || paneID != "" {
			return nil, &agentResolutionError{status: http.StatusNotFound, message: "agent not found"}
		}
		return nil, nil
	}
	if threadID != "" && paneID != "" {
		threadState := s.Tracker.GetEvent(session, "", threadID, "")
		paneState := s.Tracker.GetEvent(session, "", "", paneID)
		if threadState == nil || paneState == nil {
			return nil, &agentResolutionError{status: http.StatusNotFound, message: "agent not found"}
		}
		if threadState.Agent != paneState.Agent || threadState.ThreadID != paneState.ThreadID || threadState.PaneID != paneState.PaneID {
			return nil, &agentResolutionError{status: http.StatusBadRequest, message: "pane and thread identify different agents"}
		}
		return threadState, nil
	}
	if threadID != "" || paneID != "" {
		state := s.Tracker.GetEvent(session, "", threadID, paneID)
		if state == nil {
			return nil, &agentResolutionError{status: http.StatusNotFound, message: "agent not found"}
		}
		return state, nil
	}
	agents := s.Tracker.GetAgents(session)
	if len(agents) > 1 {
		return nil, &agentResolutionError{
			status: http.StatusBadRequest, message: fmt.Sprintf("session has %d agents; specify pane", len(agents)),
		}
	}
	return s.Tracker.GetState(session), nil
}

func writeAgentResolutionError(w http.ResponseWriter, err *agentResolutionError) {
	http.Error(w, err.message, err.status)
}

// resolveAgentPaneIDLocked finds the tmux pane hosting an agent when the
// client didn't send one. Strategies in order, all over a fresh pane
// listing (staleness is the enemy here — never the TTL cache):
//
//  1. pid-first: the tracker's pid found among a pane's agent descendants
//  2. claude-code: sessions/<pid>.json sessionId == threadId
//  3. amp: fail-closed title match ("amp - <name>", exactly one candidate)
//  4. first pane whose process tree matches the agent at all
func (s *Server) resolveAgentPaneIDLocked(session, agent, threadID, threadName string) string {
	if s.Scanner == nil || !panescan.KnownAgent(agent) {
		return ""
	}
	var panes []tmux.Pane
	for _, p := range s.Builder.Tmux.ListAllPanes() {
		if p.Session == session && !p.Managed() {
			panes = append(panes, p)
		}
	}
	if len(panes) == 0 {
		return ""
	}
	pidsByPane := s.Scanner.AgentPidsByPane(panes, agent)

	if ev := s.Tracker.GetEvent(session, agent, threadID, ""); ev != nil && ev.PID != 0 {
		for _, p := range panes {
			if slices.Contains(pidsByPane[p.ID], ev.PID) {
				return p.ID
			}
		}
	}

	if agent == "claude-code" && threadID != "" && s.Watcher != nil {
		for _, p := range panes {
			for _, pid := range pidsByPane[p.ID] {
				if s.Watcher.SessionIDForPid(pid) == threadID {
					return p.ID
				}
			}
		}
	}

	// Amp has no per-thread id surface; the pane title `amp - <name>` is
	// the only signal. Substring matches collide ("refactor" vs
	// "refactor-helper"), so fail closed unless exactly one pane matches.
	if agent == "amp" && threadName != "" {
		hit, n := "", 0
		for _, p := range panes {
			if strings.HasPrefix(strings.ToLower(p.Title), "amp - ") && strings.Contains(p.Title, threadName) {
				hit = p.ID
				n++
			}
		}
		if n == 1 {
			return hit
		}
	}

	for _, p := range panes {
		if len(pidsByPane[p.ID]) > 0 {
			return p.ID
		}
	}
	return ""
}

// killAgentPane kills the pane hosting an agent, behind the
// pid-verification gate: the tracker holds the agent pid from the moment
// the watcher reported it, and if the pane has since been recycled — same
// paneId, new process inside — killing on paneId alone takes out an
// unrelated process. Verify the pid against the pane's CURRENT agent
// descendants and refuse on any mismatch. Events without a known pid fall
// through ungated (pane-scanner synthetics, where the target is
// unambiguous anyway).
func (s *Server) killAgentPane(cmd wire.ClientCommand) {
	paneID := cmd.PaneID
	if paneID == "" {
		paneID = s.resolveAgentPaneIDLocked(cmd.Session, cmd.Agent, cmd.ThreadID, cmd.ThreadName)
	}
	if paneID == "" {
		return
	}

	var expectedPid int
	if ev := s.Tracker.GetEvent(cmd.Session, cmd.Agent, cmd.ThreadID, ""); ev != nil {
		expectedPid = ev.PID
	}
	t := s.Builder.Tmux
	if expectedPid != 0 {
		if s.Scanner == nil || !panescan.KnownAgent(cmd.Agent) {
			log.Printf("kill-agent-pane: unable to verify pid (no comm patterns for agent %q)", cmd.Agent)
			return
		}
		// Fresh pane_pid lookup, never cached: pane_pid mutates on every
		// pane recycle, and a cached value would re-create the exact
		// stale-data bug this gate exists to catch.
		panePidStr, err := t.Run("display-message", "-t", paneID, "-p", "#{pane_pid}")
		panePid, convErr := strconv.Atoi(strings.TrimSpace(panePidStr))
		if err != nil || convErr != nil {
			log.Printf("kill-agent-pane: unable to verify pid (no pane_pid) pane=%s", paneID)
			return
		}
		live := s.Scanner.AgentPidsByPane([]tmux.Pane{{ID: paneID, PID: panePid}}, cmd.Agent)[paneID]
		if !slices.Contains(live, expectedPid) {
			log.Printf("kill-agent-pane: refusing — pid mismatch (pane recycled?) pane=%s expected=%d live=%v", paneID, expectedPid, live)
			return
		}
	}

	log.Printf("kill-agent-pane: killing pane=%s agent=%s expectedPid=%d", paneID, cmd.Agent, expectedPid)
	_, _ = t.Run("kill-pane", "-t", paneID)
}
