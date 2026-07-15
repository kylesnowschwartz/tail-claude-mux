// Sidebar bootstrap: the boot-time branch of index.ts's sidebar
// management. Three cases on server start:
//
//	(a) existing sidebar panes, no reload — adopt them (restart.sh and
//	    /restart keep TUIs alive while the server cycles)
//	(b) existing sidebars + TCM_RELOAD_TUI=1 (set by POST /restart) —
//	    kill and respawn fresh (companions included) so the TUI and the
//	    companion guest pick up new code
//	(c) zero sidebars (cold boot) — auto-spawn in every active window so
//	    a fresh attach lands with the panel already visible
//
// The interactive sidebar surface (toggle, ensure-sidebar hooks, width
// enforcement) is stage 6 — this covers the boot/restart lifecycle only.
package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/config"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/widthsync"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const (
	// respawnKillSettle lets tmux process the kills before replacements
	// spawn (index.ts respawnAllActiveSidebars).
	respawnKillSettle = 300 * time.Millisecond
	// coldSpawnDelay lets palette writes settle before the first TUI pane
	// launches (index.ts bootstrap branch c).
	coldSpawnDelay = 200 * time.Millisecond
)

// BootstrapSidebars runs the boot-time sidebar lifecycle. Call once after
// the listener is up; reloadTUI comes from the TCM_RELOAD_TUI env set by
// the /restart self-exec.
func (s *Server) BootstrapSidebars(reloadTUI bool) {
	if s.ScriptsDir == "" {
		return
	}
	s.mu.Lock()
	s.sidebarVisible = true
	s.mu.Unlock()
	// Width floors to content now and again once watchers have detected
	// agents (agent names affect the minimum width) — index.ts runs the
	// same double pass.
	go s.floorWidthToContent()
	time.AfterFunc(2*time.Second, s.floorWidthToContent)

	t := s.Builder.Tmux
	panes := t.ListAllPanes()
	t.PruneStashOrphans(panes)
	existing := tmux.SidebarPanes(panes)

	// Companion feature turned OFF between runs: leftover companion panes
	// (live in windows or parked in the stash) have no other teardown
	// path — PruneStashOrphans spares them and the ensure paths no-op.
	if s.CompanionPane.Command == "" {
		for _, p := range panes {
			if p.Companion {
				log.Printf("companion: feature off — killing leftover pane=%s", p.ID)
				t.KillPane(p.ID)
			}
		}
	}

	switch {
	case len(existing) > 0 && !reloadTUI:
		log.Printf("sidebar: adopted %d existing pane(s)", len(existing))
		// Adopted sidebars predate this config: the natural enable path
		// (add companionPane, restart) must still grow companions.
		s.ensureCompanionsInAdoptedWindows(existing, panes)
	case len(existing) > 0 && reloadTUI:
		log.Printf("sidebar: reload requested — killing and respawning %d pane(s)", len(existing))
		s.killForReload(panes)
		time.AfterFunc(respawnKillSettle, s.spawnInActiveWindows)
	default:
		log.Printf("sidebar: cold boot — first-paint autospawn")
		time.AfterFunc(coldSpawnDelay, s.spawnInActiveWindows)
	}
}

// killForReload clears every pane the reload respawn replaces: all
// sidebar panes, the stash, and — when the companion feature is on —
// all companion panes. Companions die with their sidebars so a restart
// restarts the guest process too, and no window is left holding a
// stranded full-height companion until its next visit. When the feature
// is off, bootstrap's teardown already removed any leftovers.
func (s *Server) killForReload(panes []tmux.Pane) {
	t := s.Builder.Tmux
	for _, p := range tmux.SidebarPanes(panes) {
		t.KillPane(p.ID)
	}
	if s.CompanionPane.Command != "" {
		for _, p := range tmux.CompanionPanes(panes) {
			t.KillPane(p.ID)
		}
	}
	t.KillStashSession()
}

// spawnInActiveWindows spawns a sidebar in every session's active window
// that lacks one, ensures the companion pane in every active window, then
// tells connected TUIs to re-identify (index.ts spawnInEveryActiveWindow).
// Idempotent per window.
func (s *Server) spawnInActiveWindows() {
	t := s.Builder.Tmux
	panes := t.ListAllPanes()
	width := s.sidebarWidthSnapshot()

	hasSidebar := map[string]bool{}
	for _, p := range tmux.SidebarPanes(panes) {
		hasSidebar[p.WindowID] = true
	}

	seen := map[string]bool{}
	for _, p := range panes {
		if !p.WindowActive || p.Session == tmux.StashSession || p.Ignored || seen[p.WindowID] {
			continue
		}
		seen[p.WindowID] = true
		freshID := ""
		if !hasSidebar[p.WindowID] {
			s.killStrandedCompanions(p.WindowID, panes)
			freshID = t.SpawnSidebar(p.WindowID, width, s.SidebarPosition, s.ScriptsDir)
			log.Printf("sidebar: spawn window=%s session=%s pane=%s", p.WindowID, p.Session, freshID)
		}
		s.ensureCompanionInWindow(p.WindowID, panes, freshID)
	}

	if data, err := json.Marshal(wire.ReIdentify{Type: wire.TypeReIdentify}); err == nil {
		s.mu.Lock()
		for c := range s.clients {
			_ = c.conn.WriteText(string(data))
		}
		s.mu.Unlock()
	}
}

// ensureSidebarDebounce collapses rapid hook-fired /ensure-sidebar calls
// during fast session switching into one check after switching settles
// (index.ts debouncedEnsureSidebar).
const ensureSidebarDebounce = 150 * time.Millisecond

// handleEnsureSidebar is the POST /ensure-sidebar ingress (tmux
// window/session hooks): body is "clientTty|session|windowId". Debounced;
// the latest context wins.
func (s *Server) handleEnsureSidebar(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	windowID := parseWindowContext(string(body))
	s.setPendingEnforcement()
	s.mu.Lock()
	if windowID != "" {
		s.ensureSidebarWindowID = windowID
	}
	if s.ensureSidebarTimer == nil {
		s.ensureSidebarTimer = time.AfterFunc(ensureSidebarDebounce, func() {
			s.mu.Lock()
			s.ensureSidebarTimer = nil
			windowID := s.ensureSidebarWindowID
			s.ensureSidebarWindowID = ""
			s.mu.Unlock()
			s.ensureSidebarInWindow(windowID)
		})
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// ensureSidebarInWindow spawns a sidebar in windowID when it has none
// (index.ts ensureSidebarInWindow, minus width enforcement — stage 6).
// An empty windowID falls back to the current session's active window.
func (s *Server) ensureSidebarInWindow(windowID string) {
	s.mu.Lock()
	visible := s.sidebarVisible
	s.mu.Unlock()
	if s.ScriptsDir == "" || !visible {
		return
	}
	// Session switches can change window width, and tmux redistributes
	// pane sizes proportionally — always re-impose the stored width and
	// companion height (index.ts ensureSidebarInWindow tail).
	defer s.enforceGeometry("")
	t := s.Builder.Tmux
	panes := t.ListAllPanes()

	if windowID == "" {
		current, ok := t.CurrentSession("")
		if !ok {
			return
		}
		for _, p := range panes {
			if p.Session == current && p.WindowActive {
				windowID = p.WindowID
				break
			}
		}
		if windowID == "" {
			return
		}
	}

	// @tcm-ignore'd sessions never get managed panes. The deferred
	// enforceGeometry still runs: a switch INTO an ignored session should
	// still repair other windows' drift.
	if windowIgnored(panes, windowID) {
		return
	}

	hasSidebar := false
	for _, p := range tmux.SidebarPanes(panes) {
		if p.WindowID == windowID {
			hasSidebar = true
			break
		}
	}
	freshID := ""
	if !hasSidebar {
		s.killStrandedCompanions(windowID, panes)
		freshID = t.SpawnSidebar(windowID, s.sidebarWidthSnapshot(), s.SidebarPosition, s.ScriptsDir)
		log.Printf("sidebar: ensure spawn window=%s pane=%s", windowID, freshID)
	}
	s.ensureCompanionInWindow(windowID, panes, freshID)
}

// sidebarWidthSnapshot reads the configured width under the lock: the
// drag-save timer and floorWidthToContent write it under mu, so unlocked
// reads in the spawn paths race them.
func (s *Server) sidebarWidthSnapshot() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Builder.SidebarWidth
}

// killStrandedCompanions removes companion panes orphaned by a dead
// sidebar (crash, reload-kill, stale panes from a dead server). A dying
// sidebar hands its column to the companion; a fresh sidebar spawn then
// opens a NEW edge column, and the idempotent companion-ensure would see
// "already has one" and leave the stray full-height companion forever.
// Killing it first lets the ensure tail respawn it below the new sidebar.
func (s *Server) killStrandedCompanions(windowID string, panes []tmux.Pane) {
	t := s.Builder.Tmux
	for _, p := range tmux.CompanionPanes(panes) {
		if p.WindowID == windowID {
			log.Printf("companion: killing stranded pane=%s window=%s", p.ID, windowID)
			t.KillPane(p.ID)
		}
	}
}

// ensureCompanionInWindow spawns the configured companion pane below
// windowID's sidebar when one is missing. No-op when the feature is off,
// the window has no sidebar (the companion always targets an existing
// sidebar), or the window already has a companion.
//
// panes is the caller's listing; freshSidebarID is set when the caller
// just spawned the window's sidebar (the listing predates it, and any
// stranded companion was killed right before the spawn, so the
// exists-checks are skipped). SpawnCompanion still takes its own fresh
// listing for the stash scan — sharing one across windows would offer
// the same stashed pane twice and the second join-pane would steal it.
func (s *Server) ensureCompanionInWindow(windowID string, panes []tmux.Pane, freshSidebarID string) {
	if s.CompanionPane.Command == "" || windowID == "" {
		return
	}
	sidebarID := freshSidebarID
	windowHeight := windowHeightIn(panes, windowID)
	if sidebarID == "" {
		var sidebar tmux.Pane
		for _, p := range tmux.SidebarPanes(panes) {
			if p.WindowID == windowID {
				sidebar = p
				break
			}
		}
		if sidebar.ID == "" {
			return
		}
		for _, p := range tmux.CompanionPanes(panes) {
			if p.WindowID == windowID {
				return // already has one
			}
		}
		sidebarID = sidebar.ID
		windowHeight = sidebar.WindowHeight
	}
	if sidebarID == "" {
		return // fresh spawn failed; nothing to attach to
	}
	rows := tmux.ClampCompanionHeight(s.CompanionPane.Rows, windowHeight)
	if rows == 0 {
		return // window too short for a companion; retrying would fail forever
	}
	id := s.Builder.Tmux.SpawnCompanion(sidebarID, rows, s.CompanionPane.Command)
	log.Printf("companion: spawn window=%s pane=%s", windowID, id)
}

// windowIgnored reports whether windowID belongs to an @tcm-ignore'd
// session (any of its panes carries the session-scoped option).
func windowIgnored(panes []tmux.Pane, windowID string) bool {
	for _, p := range panes {
		if p.WindowID == windowID {
			return p.Ignored
		}
	}
	return false
}

// windowHeightIn finds the window's height from any of its panes in the
// listing; -1 when the window has no measurable pane.
func windowHeightIn(panes []tmux.Pane, windowID string) int {
	for _, p := range panes {
		if p.WindowID == windowID && p.WindowHeight > 0 {
			return p.WindowHeight
		}
	}
	return -1
}

// ensureCompanionsInAdoptedWindows spawns companions under sidebars the
// boot adopted — the enable path "add companionPane to config, restart"
// must grow companions without waiting for a window-switch hook. No-op
// (zero tmux calls) when the feature is off.
func (s *Server) ensureCompanionsInAdoptedWindows(sidebars []tmux.Pane, panes []tmux.Pane) {
	if s.CompanionPane.Command == "" {
		return
	}
	seen := map[string]bool{}
	for _, p := range sidebars {
		if !seen[p.WindowID] {
			seen[p.WindowID] = true
			s.ensureCompanionInWindow(p.WindowID, panes, "")
		}
	}
}

// enforceGeometry re-imposes sidebar width and companion height from ONE
// pane listing: x-resizes never change heights and y-resizes never change
// widths, so a single snapshot serves both enforcers.
func (s *Server) enforceGeometry(skipSession string) {
	panes := s.Builder.Tmux.ListAllPanes()
	s.enforceSidebarWidthIn(panes, skipSession)
	s.enforceCompanionHeightIn(panes)
}

// enforceCompanionHeightIn resizes every companion pane in the listing
// back to the configured rows (clamped per window): session switches
// redistribute pane sizes proportionally, the same drift
// enforceSidebarWidth corrects for width.
func (s *Server) enforceCompanionHeightIn(panes []tmux.Pane) {
	if s.CompanionPane.Command == "" {
		return
	}
	t := s.Builder.Tmux
	for _, p := range tmux.CompanionPanes(panes) {
		want := tmux.ClampCompanionHeight(s.CompanionPane.Rows, p.WindowHeight)
		if want == 0 || p.Height == want {
			continue
		}
		log.Printf("companion: enforce %s %d→%d", p.ID, p.Height, want)
		t.ResizePaneHeight(p.ID, want)
	}
}

// handlePaneExited is the POST /pane-exited ingress (tmux pane-exited /
// pane-died hooks): a pane closed — when every remaining pane in a window
// is tcm-managed (sidebar or companion), the user's last real pane is
// gone, so kill the managed leftovers (index.ts killOrphanedSidebarPanes,
// generalized from "sidebar alone").
func (s *Server) handlePaneExited(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	visible := s.sidebarVisible
	s.mu.Unlock()
	if !visible {
		w.WriteHeader(http.StatusOK)
		return
	}
	t := s.Builder.Tmux
	panes := t.ListAllPanes()
	userPanesByWindow := map[string]int{}
	for _, p := range panes {
		if p.Session != tmux.StashSession && !p.Managed() {
			userPanesByWindow[p.WindowID]++
		}
	}
	for _, p := range tmux.ManagedPanes(panes) {
		if userPanesByWindow[p.WindowID] == 0 {
			log.Printf("sidebar: killing orphaned pane=%s window=%s", p.ID, p.WindowID)
			t.KillPane(p.ID)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// parseWindowContext extracts the windowId from a tmux hook context body:
// "clientTty|session|windowId" (new) or "session:windowId" (legacy).
// "" when unparseable.
func parseWindowContext(body string) string {
	trimmed := strings.Trim(strings.TrimSpace(body), `"'`)
	if parts := strings.Split(trimmed, "|"); len(parts) == 3 && parts[1] != "" && parts[2] != "" {
		return parts[2]
	}
	if idx := strings.Index(trimmed, ":"); idx >= 1 && idx < len(trimmed)-1 {
		return trimmed[idx+1:]
	}
	return ""
}

// --- Toggle + width enforcement (index.ts toggleSidebar /
// enforceSidebarWidth / report-width handling) ---

// pendingEnforcementWindow is how long a /focus, /ensure-sidebar, or
// /client-resized hook marks the NEXT report-width as a proportional
// resize echo rather than a user drag.
const pendingEnforcementWindow = 500 * time.Millisecond

// handleToggle is the POST /toggle ingress (prefix+o,s): hide stashes
// every sidebar pane; show restores/spawns in every active window.
func (s *Server) handleToggle(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wasVisible := s.sidebarVisible
	s.sidebarVisible = !wasVisible
	s.mu.Unlock()

	t := s.Builder.Tmux
	if wasVisible {
		panes := t.ListAllPanes()
		for _, p := range tmux.ManagedPanes(panes) {
			t.StashPane(p.ID, panes)
		}
		log.Printf("sidebar: toggle off — stashed panes")
	} else {
		s.setPendingEnforcement()
		s.spawnInActiveWindows() // restores from stash first; re-identify broadcast
		s.enforceGeometry("")
		log.Printf("sidebar: toggle on")
	}
	w.WriteHeader(http.StatusOK)
}

// handleClientResized is the POST /client-resized ingress (terminal
// SIGWINCH hook): the next report-width is an enforcement echo, and the
// stored width is re-imposed immediately.
func (s *Server) handleClientResized(w http.ResponseWriter, _ *http.Request) {
	s.setPendingEnforcement()
	s.mu.Lock()
	visible := s.sidebarVisible
	s.mu.Unlock()
	if visible {
		s.enforceGeometry("")
	}
	w.WriteHeader(http.StatusOK)
}

// setPendingEnforcement marks the next report-width as a reflow echo, not
// a user drag; auto-expires in case no SIGWINCH follows.
func (s *Server) setPendingEnforcement() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelPendingSaveLocked()
	s.pendingEnforcement = true
	if s.pendingEnforcementTimer != nil {
		s.pendingEnforcementTimer.Stop()
	}
	s.pendingEnforcementTimer = time.AfterFunc(pendingEnforcementWindow, func() {
		s.mu.Lock()
		s.pendingEnforcement = false
		s.pendingEnforcementTimer = nil
		s.mu.Unlock()
	})
}

func (s *Server) cancelPendingSaveLocked() {
	if s.saveTimer != nil {
		s.saveTimer.Stop()
		s.saveTimer = nil
	}
}

// enforceSidebarWidth lists panes and re-imposes the width; the
// width-only callers (report-width, equalize, content floor). Reflow
// sites that also enforce companion height go through enforceGeometry
// to share one listing.
func (s *Server) enforceSidebarWidth(skipSession string) {
	s.enforceSidebarWidthIn(s.Builder.Tmux.ListAllPanes(), skipSession)
}

// enforceSidebarWidthIn resizes every sidebar pane in the listing back to
// the configured width. skipSession exempts the session whose TUI just
// reported the width (its pane already has it — resizing would echo).
func (s *Server) enforceSidebarWidthIn(panes []tmux.Pane, skipSession string) {
	t := s.Builder.Tmux
	s.mu.Lock()
	want := s.Builder.SidebarWidth
	s.mu.Unlock()
	for _, p := range tmux.SidebarPanes(panes) {
		if p.Width == want || (skipSession != "" && p.Session == skipSession) {
			continue
		}
		log.Printf("sidebar: enforce %s %d→%d", p.ID, p.Width, want)
		t.ResizePane(p.ID, want)
	}
}

// reportWidthLocked handles the report-width command (TUI drag). Runs
// with s.mu held (command dispatch). An enforcement echo restores the
// stored width; a real drag persists after a debounce so reflow echoes
// can cancel it.
func (s *Server) reportWidthLocked(c *client, cmd wire.ClientCommand) {
	if !s.sidebarVisible {
		return
	}
	windowWidth := 0
	if c.sessionName != "" {
		for _, p := range tmux.SidebarPanes(s.panesLocked()) {
			if p.Session == c.sessionName && p.WindowWidth > 0 {
				windowWidth = p.WindowWidth
				break
			}
		}
	}
	reported := widthsync.Clamp(cmd.Width, windowWidth)
	if s.pendingEnforcement {
		s.pendingEnforcement = false
		go s.enforceSidebarWidth("")
		return
	}
	if reported == s.Builder.SidebarWidth {
		return
	}
	session := c.sessionName
	s.cancelPendingSaveLocked()
	s.saveTimer = time.AfterFunc(widthsync.SaveDebounce, func() {
		s.mu.Lock()
		s.saveTimer = nil
		s.Builder.SidebarWidth = reported
		if err := config.Save(s.Builder.ConfigDir, map[string]any{"sidebarWidth": reported}); err != nil {
			log.Printf("save sidebarWidth: %v", err)
		}
		s.broadcastLocked()
		s.mu.Unlock()
		s.enforceSidebarWidth(session)
	})
}

// equalizeWidthLocked snaps the width baseline back to the default
// (equalize-width command). Runs with s.mu held.
func (s *Server) equalizeWidthLocked() {
	s.cancelPendingSaveLocked()
	s.Builder.SidebarWidth = widthsync.DefaultWidth
	if err := config.Save(s.Builder.ConfigDir, map[string]any{"sidebarWidth": widthsync.DefaultWidth}); err != nil {
		log.Printf("save sidebarWidth: %v", err)
	}
	go s.enforceSidebarWidth("")
	s.broadcastLocked()
}

// floorWidthToContent widens the configured width when session/agent
// content needs more room (index.ts floorWidthToContent) — the operator
// preference is a floor, not a cap. Bump is runtime-only, never saved.
func (s *Server) floorWidthToContent() {
	s.mu.Lock()
	st := s.prepareStateLocked()
	minWidth := widthsync.ComputeMin(st.Sessions)
	bump := s.Builder.SidebarWidth < minWidth
	if bump {
		log.Printf("sidebar: width %d < content min %d, bumping", s.Builder.SidebarWidth, minWidth)
		s.Builder.SidebarWidth = minWidth
		s.broadcastLocked()
	}
	s.mu.Unlock()
	if bump {
		s.enforceSidebarWidth("")
	}
}

// applyThemeLocked re-applies the palette to tmux after a theme change
// (index.ts applyPaletteToTmux). Apply re-reads config from disk, so the
// config write must land before this runs.
func (s *Server) applyThemeLocked(reason string) {
	if s.Palette != nil {
		s.Palette.Apply(reason)
	}
}

// quitAll is index.ts quitAll: kill every sidebar and companion pane and
// the stash session, notify TUIs, then hand off to the wired Quit
// (pid-file cleanup + exit). Serves both POST /quit and the TUI's quit
// command.
func (s *Server) quitAll() {
	t := s.Builder.Tmux
	panes := t.ListAllPanes()
	for _, p := range tmux.ManagedPanes(panes) {
		t.KillPane(p.ID)
	}
	t.KillStashSession()

	s.mu.Lock()
	if quit, err := json.Marshal(wire.QuitNotify{Type: wire.TypeQuit}); err == nil {
		for c := range s.clients {
			_ = c.conn.WriteText(string(quit))
		}
	}
	s.mu.Unlock()
	if s.Quit != nil {
		time.AfterFunc(50*time.Millisecond, s.Quit)
	}
}
