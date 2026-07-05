// Sidebar bootstrap: the boot-time branch of index.ts's sidebar
// management. Three cases on server start:
//
//	(a) existing sidebar panes, no reload — adopt them (restart.sh and
//	    /restart keep TUIs alive while the server cycles)
//	(b) existing sidebars + TCM_RELOAD_TUI=1 (set by POST /restart) —
//	    kill and respawn fresh so the TUI picks up new code
//	(c) zero sidebars (cold boot) — auto-spawn in every active window so
//	    a fresh attach lands with the panel already visible
//
// The interactive sidebar surface (toggle, ensure-sidebar hooks, width
// enforcement) is stage 6 — this covers the boot/restart lifecycle only.
package server

import (
	"encoding/json"
	"log"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
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
	t := s.Builder.Tmux
	panes := t.ListAllPanes()
	t.PruneStashOrphans(panes)
	existing := tmux.SidebarPanes(panes)

	switch {
	case len(existing) > 0 && !reloadTUI:
		log.Printf("sidebar: adopted %d existing pane(s)", len(existing))
	case len(existing) > 0 && reloadTUI:
		log.Printf("sidebar: reload requested — killing and respawning %d pane(s)", len(existing))
		for _, p := range existing {
			t.KillPane(p.ID)
		}
		t.KillStashSession()
		time.AfterFunc(respawnKillSettle, s.spawnInActiveWindows)
	default:
		log.Printf("sidebar: cold boot — first-paint autospawn")
		time.AfterFunc(coldSpawnDelay, s.spawnInActiveWindows)
	}
}

// spawnInActiveWindows spawns a sidebar in every session's active window
// that lacks one, then tells connected TUIs to re-identify (index.ts
// spawnInEveryActiveWindow). Idempotent per window.
func (s *Server) spawnInActiveWindows() {
	t := s.Builder.Tmux
	panes := t.ListAllPanes()

	hasSidebar := map[string]bool{}
	for _, p := range tmux.SidebarPanes(panes) {
		hasSidebar[p.WindowID] = true
	}

	spawned := map[string]bool{}
	for _, p := range panes {
		if !p.WindowActive || p.Session == tmux.StashSession || hasSidebar[p.WindowID] || spawned[p.WindowID] {
			continue
		}
		spawned[p.WindowID] = true
		id := t.SpawnSidebar(p.WindowID, s.Builder.SidebarWidth, s.SidebarPosition, s.ScriptsDir)
		log.Printf("sidebar: spawn window=%s session=%s pane=%s", p.WindowID, p.Session, id)
	}

	if data, err := json.Marshal(wire.ReIdentify{Type: wire.TypeReIdentify}); err == nil {
		s.mu.Lock()
		for c := range s.clients {
			_ = c.conn.WriteText(string(data))
		}
		s.mu.Unlock()
	}
}

// quitAll is index.ts quitAll: kill every sidebar pane and the stash
// session, notify TUIs, then hand off to the wired Quit (pid-file cleanup
// + exit). Serves both POST /quit and the TUI's quit command.
func (s *Server) quitAll() {
	t := s.Builder.Tmux
	panes := t.ListAllPanes()
	for _, p := range tmux.SidebarPanes(panes) {
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
