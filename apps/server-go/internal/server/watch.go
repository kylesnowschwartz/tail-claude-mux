// watch.go wires the stage-4 agent pipeline into the server: the Claude
// hook watcher, the tracker, the pane scanner, session routing (cwd and
// pid), the liveness sweep, and the debounced watcher broadcast. Interval
// and TTL constants mirror server/index.ts.
package server

import (
	"log"
	"strings"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/claude/claudedir"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/procwalk"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const (
	// paneScanInterval is PANE_SCAN_INTERVAL_MS.
	paneScanInterval = 3 * time.Second
	// livenessInterval drives the pid liveness sweep (startLivenessCheck).
	livenessInterval = 5 * time.Second
	// watcherDebounce batches emit-driven broadcasts (debouncedBroadcast).
	watcherDebounce = 200 * time.Millisecond
	// seedGrace is how long after start seed events keep the seed flag
	// (watchersSeeded timeout).
	seedGrace = 3 * time.Second
	// dirCacheTTL bounds the dir→session and pane-pid routing caches; both
	// turn over as fast as tmux panes do, and the watcher contract for
	// staleness is "a few seconds is fine".
	dirCacheTTL = 5 * time.Second

	// reconcileStaleMS is RECONCILE_STALE_MS: running entries older than
	// this get the authoritative probe on the broadcast path.
	reconcileStaleMS = 60 * 1000
	// stuckRunningTimeoutMS is STUCK_RUNNING_TIMEOUT_MS (pruneStuck).
	stuckRunningTimeoutMS = 3 * 60 * 1000

	paneHighlightBorder = "fg=#fab387,bold"
	paneHighlightBg     = "bg=#2a2a4a"
	paneHighlightFlash  = 300 * time.Millisecond
)

// StartWatchers binds the Claude watcher context, runs its cold-start seed,
// arms the seed-grace timer, and launches the pane-scan and liveness-sweep
// loops. Call once, before serving.
func (s *Server) StartWatchers() {
	if s.Tracker == nil {
		return
	}

	s.mu.Lock()
	// Active sessions drive the seen/unseen policy: sessions with an
	// attached client are active (bun: attachedSessions, falling back to
	// the current session).
	var active []string
	for _, c := range s.Builder.Tmux.ListClients() {
		if c.SessionName != "" {
			active = append(active, c.SessionName)
		}
	}
	if len(active) == 0 {
		if current, ok := s.Builder.Tmux.CurrentSession(""); ok {
			active = []string{current}
		}
	}
	s.Tracker.SetActiveSessions(active)

	if s.Watcher != nil {
		s.Watcher.Start(&ccwatch.Context{
			ResolveSession:      s.resolveSessionLocked,
			ResolveSessionByPid: s.resolveSessionByPidLocked,
			Emit:                s.emitLocked,
			Locked: func(fn func()) {
				s.mu.Lock()
				defer s.mu.Unlock()
				fn()
			},
		})
		log.Printf("agent watcher started: %s", s.Watcher.Name())
	}
	s.mu.Unlock()

	// Seed grace: after it, events are live (unseen policy applies only to
	// inactive sessions) and the current session's seed-unseen flags clear.
	time.AfterFunc(seedGrace, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.watchersSeeded = true
		if current, ok := s.Builder.Tmux.CurrentSession(""); ok {
			if s.Tracker.HandleFocus(current) {
				s.broadcastLocked()
			}
		}
	})

	go s.paneScanLoop()
	go s.livenessLoop()
}

// emitLocked is the watcher context's Emit: derive activity-log entries,
// fold the event into the tracker, and schedule a debounced broadcast.
// Runs with s.mu held.
func (s *Server) emitLocked(ev wire.AgentEvent) {
	log.Printf("agent-emit %s session=%s status=%s", ev.Agent, ev.Session, ev.Status)
	// Always update lastSeenByThread (so post-seed diffs are correct), but
	// only push log entries once initial seeding is complete — otherwise
	// every cold-start reconstruction would flood the buffer.
	entries := s.deriveLogEntriesLocked(ev)
	if s.watchersSeeded {
		for _, e := range entries {
			s.Metadata.AppendLog(ev.Session, e)
		}
	}
	s.Tracker.ApplyEvent(ev, !s.watchersSeeded)
	s.debouncedBroadcastLocked()
}

// lastSeen tracks the last thread/tool/status surfaced per thread so the
// log only records changes (deriveLogEntries' lastSeenByThread).
type lastSeen struct {
	tool, thread, status string
}

// agentCode is the two-letter source prefix in ln-zone entries.
func agentCode(agent string) string {
	switch agent {
	case "claude-code":
		return "cc"
	case "pi":
		return "pi"
	case "codex":
		return "cd"
	case "amp":
		return "ap"
	default:
		if len(agent) > 2 {
			return agent[:2]
		}
		return agent
	}
}

func shortThreadIDSuffix(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

// deriveLogEntriesLocked synthesizes human-readable ln-zone entries from
// one agent event. Emit order: thread name (least recent) → tool (mid) →
// status transition (most recent), so the freshest visible entry reflects
// the latest signal. Running/idle/done are deliberately not surfaced:
// running is implied by tool descriptions; idle/done are too noisy.
func (s *Server) deriveLogEntriesLocked(ev wire.AgentEvent) []wire.MetadataLogEntry {
	if ev.ThreadID == "" {
		return nil
	}
	last := s.lastSeenByThread[ev.ThreadID]
	source := strings.TrimSpace(agentCode(ev.Agent) + " " + shortThreadIDSuffix(ev.ThreadID))
	var out []wire.MetadataLogEntry

	if ev.ThreadName != "" && ev.ThreadName != last.thread {
		out = append(out, wire.MetadataLogEntry{Source: source, Message: ev.ThreadName, Tone: "neutral"})
	}
	if ev.ToolDescription != "" && ev.ToolDescription != last.tool {
		out = append(out, wire.MetadataLogEntry{Source: source, Message: ev.ToolDescription, Tone: "info"})
	}
	if ev.Status != last.status {
		switch ev.Status {
		case wire.StatusError:
			out = append(out, wire.MetadataLogEntry{Source: source, Message: "errored", Tone: "error"})
		case wire.StatusWaiting:
			out = append(out, wire.MetadataLogEntry{Source: source, Message: "awaiting input", Tone: "info"})
		case wire.StatusInterrupted:
			out = append(out, wire.MetadataLogEntry{Source: source, Message: "interrupted", Tone: "warn"})
		}
	}

	s.lastSeenByThread[ev.ThreadID] = lastSeen{
		tool:   ev.ToolDescription,
		thread: ev.ThreadName,
		status: ev.Status,
	}
	return out
}

// debouncedBroadcastLocked batches watcher-driven broadcasts at
// watcherDebounce. Runs with s.mu held.
func (s *Server) debouncedBroadcastLocked() {
	if s.broadcastTimer != nil {
		return
	}
	s.broadcastTimer = time.AfterFunc(watcherDebounce, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.broadcastTimer = nil
		s.broadcastLocked()
	})
}

// paneScanLoop is startPaneScan: every paneScanInterval, scan all panes and
// fold presence into the tracker. The scan (tmux + 2×ps) runs outside the
// lock; only the fold and broadcast hold it. Idle servers skip the scan.
func (s *Server) paneScanLoop() {
	for range time.Tick(paneScanInterval) {
		s.mu.Lock()
		n := len(s.clients)
		s.mu.Unlock()
		if n == 0 || s.Scanner == nil {
			continue
		}

		panes := s.Builder.Tmux.ListAllPanes()
		next := s.Scanner.Scan(panes)

		s.mu.Lock()
		changed := false
		for session, paneAgents := range next {
			if s.Tracker.ApplyPanePresence(session, paneAgents) {
				changed = true
			}
		}
		// Sessions absent from the scan get empty presence so alive
		// entries transition to exited.
		for _, name := range s.lastSessions {
			if _, ok := next[name]; !ok {
				if s.Tracker.ApplyPanePresence(name, nil) {
					changed = true
				}
			}
		}
		if changed {
			s.broadcastLocked()
		}
		s.mu.Unlock()
	}
}

// livenessLoop is startLivenessCheck: every livenessInterval, mark tracked
// instances with a dead pid as exited and broadcast on any flip so
// pruneTerminal removes the dead row now.
func (s *Server) livenessLoop() {
	for range time.Tick(livenessInterval) {
		s.mu.Lock()
		if s.Tracker.RunLivenessSweepOnce() {
			s.broadcastLocked()
		}
		s.mu.Unlock()
	}
}

// probeLiveness asks the owning watcher whether a stale running instance is
// genuinely working (reconcileStaleRunning's probe). Runs with s.mu held.
func (s *Server) probeLiveness(ev wire.AgentEvent) tracker.ProbeVerdict {
	if ev.PID == 0 || s.Watcher == nil || ev.Agent != s.Watcher.Name() {
		return tracker.ProbeNoSignal
	}
	return s.Watcher.ProbeLiveStatus(ev.PID, ev.ThreadID, ev.PaneTitle)
}

// resolveSessionLocked ports watcherCtx.resolveSession: direct dir match,
// longest-prefix match in both directions, then the encoded-path fallback
// for cwds the watcher couldn't decode. Runs with s.mu held.
func (s *Server) resolveSessionLocked(projectDir string) string {
	m := s.dirSessionMapLocked()
	if name, ok := m[projectDir]; ok {
		return name
	}
	bestName, bestLen := "", -1
	for dir, name := range m {
		match := strings.HasPrefix(projectDir, dir+"/") || strings.HasPrefix(dir, projectDir+"/")
		if match && len(dir) > bestLen {
			bestName, bestLen = name, len(dir)
		}
	}
	if bestName != "" {
		return bestName
	}
	if encoded, ok := strings.CutPrefix(projectDir, "__encoded__:"); ok {
		for dir, name := range m {
			if claudedir.EncodeProjectPath(dir) == encoded {
				return name
			}
		}
	}
	return ""
}

// dirSessionMapLocked is getDirSessionMap: session dir → name, active-pane
// dirs overriding session_path, cached for dirCacheTTL.
func (s *Server) dirSessionMapLocked() map[string]string {
	if s.dirSessionCache != nil && time.Since(s.dirSessionCacheAt) < dirCacheTTL {
		return s.dirSessionCache
	}
	m := map[string]string{}
	activeDirs := tmux.ActiveDirs(s.panesLocked())
	for _, sess := range s.Builder.Tmux.ListSessions() {
		dir := sess.Dir
		if d, ok := activeDirs[sess.Name]; ok {
			dir = d
		}
		if dir != "" {
			m[dir] = sess.Name
		}
	}
	s.dirSessionCache = m
	s.dirSessionCacheAt = time.Now()
	return m
}

// panesLocked returns the pane listing behind every routing lookup, cached
// for dirCacheTTL (one tmux exec serves the dir map and the pid index).
func (s *Server) panesLocked() []tmux.Pane {
	if s.panesCache != nil && time.Since(s.panesCacheAt) < dirCacheTTL {
		return s.panesCache
	}
	s.panesCache = s.Builder.Tmux.ListAllPanes()
	s.panesCacheAt = time.Now()
	return s.panesCache
}

// resolveSessionByPidLocked ports resolveSessionByPidLive: walk the OS
// process tree from an agent pid up to the pane shell pid that owns it.
// The ps snapshot is read per call (cost is paid per hook, not per render);
// the pane-pid index is cached for dirCacheTTL.
func (s *Server) resolveSessionByPidLocked(pid int) string {
	if pid <= 1 || s.Scanner == nil {
		return ""
	}
	raw, err := s.Scanner.Run("ps", "-axo", "pid=,ppid=,command=")
	if err != nil {
		return ""
	}
	return procwalk.ResolveSessionByPid(pid, tmux.PanePidIndex(s.panesLocked()), procwalk.ParseProcessSnapshot(raw))
}

// focusAgentPane ports the select-window/select-pane navigation plus the
// 300ms highlight flash. Prefers the paneId the client sent (same source
// as the row the user clicked); falls back to the tracker's event.
func (s *Server) focusAgentPane(cmd wire.ClientCommand) {
	paneID := cmd.PaneID
	if paneID == "" {
		if ev := s.Tracker.GetEvent(cmd.Session, cmd.Agent, cmd.ThreadID, ""); ev != nil {
			paneID = ev.PaneID
		}
	}
	if paneID == "" {
		return
	}
	t := s.Builder.Tmux
	// select-window accepts a pane id directly (resolves to its window);
	// without it select-pane alone won't work across windows.
	_, _ = t.Run("select-window", "-t", paneID)
	_, _ = t.Run("select-pane", "-t", paneID)
	_, _ = t.Run("set-option", "-p", "-t", paneID, "pane-active-border-style", paneHighlightBorder)
	_, _ = t.Run("select-pane", "-t", paneID, "-P", paneHighlightBg)
	time.AfterFunc(paneHighlightFlash, func() {
		_, _ = t.Run("set-option", "-p", "-t", paneID, "-u", "pane-active-border-style")
		_, _ = t.Run("select-pane", "-t", paneID, "-P", "")
	})
}

// handleFocusContext is the POST /focus ingress (tmux hook): body is
// "clientTty|session|windowId" (new) or "session:windowId" (legacy).
// Returns the session name, "" when unparseable.
func parseFocusContext(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	if parts := strings.Split(trimmed, "|"); len(parts) == 3 && parts[1] != "" && parts[2] != "" {
		return parts[1]
	}
	if idx := strings.Index(trimmed, ":"); idx >= 1 {
		if session, windowID := trimmed[:idx], trimmed[idx+1:]; session != "" && windowID != "" {
			return session
		}
	}
	return ""
}
