// Sidebar pane operations: the Go port of the tmux provider's sidebar
// surface (packages/mux/providers/tmux/src/provider.ts) — spawn (fresh or
// restore-from-stash), kill, and stash-orphan pruning. Orchestration
// (when to spawn where) stays in the server, mirroring the bun split.
package tmux

import "strconv"

const (
	// SidebarPaneTitle is the legacy identification title; the
	// @tcm-sidebar pane option is the stable marker (survives pane_title
	// rewriting by escape sequences the TUI emits).
	SidebarPaneTitle    = "tcm-sidebar"
	sidebarMarkerOption = "@tcm-sidebar"
	markerValue         = "1"
)

// SidebarPanes derives the non-stash sidebar panes from a listing.
func SidebarPanes(panes []Pane) []Pane {
	var out []Pane
	for _, p := range panes {
		if p.Sidebar && p.Session != StashSession {
			out = append(out, p)
		}
	}
	return out
}

// PruneStashOrphans kills stash-session panes whose title drifted away
// from tcm-sidebar (a dead TUI lets tmux automatic-rename re-derive the
// title, often to $USER); they accumulate forever and confuse future
// restore-from-stash attempts.
func (t *Tmux) PruneStashOrphans(panes []Pane) {
	for _, p := range panes {
		if p.Session == StashSession && !p.Sidebar {
			t.KillPane(p.ID)
		}
	}
}

// KillPane kills one pane by id.
func (t *Tmux) KillPane(paneID string) {
	_, _ = t.Run("kill-pane", "-t", paneID)
}

// ResizePane sets a pane's width in columns.
func (t *Tmux) ResizePane(paneID string, width int) {
	_, _ = t.Run("resize-pane", "-t", paneID, "-x", strconv.Itoa(width))
}

// StashPane parks a live tcm-managed pane in the stash session instead
// of killing it, so toggle-on restores the running process (provider.ts
// hideSidebar). The stash window is resized first: join-pane fails with
// "pane too small" when stash panes fill up.
func (t *Tmux) StashPane(paneID string, panes []Pane) {
	t.ensureStash()
	t.PruneStashOrphans(panes)
	_, _ = t.Run("resize-window", "-t", StashSession+":", "-x", "200", "-y", "200")
	_, _ = t.Run("join-pane", "-d", "-s", paneID, "-t", StashSession+":")
}

// ensureStash creates the hidden stash session when missing.
func (t *Tmux) ensureStash() {
	if _, err := t.Run("has-session", "-t", StashSession); err != nil {
		_, _ = t.Run("new-session", "-d", "-s", StashSession, "-x", "80", "-y", "24")
	}
}

// KillStashSession removes the hidden stash session (provider.ts
// cleanupSidebar).
func (t *Tmux) KillStashSession() {
	_, _ = t.Run("kill-session", "-t", StashSession)
}

// SpawnSidebar creates a sidebar pane in windowID at the given edge and
// returns its pane id ("" on failure). A stashed sidebar pane is restored
// via join-pane when one exists; otherwise a fresh pane runs
// scriptsDir/start.sh. Neither path focuses the new pane — the TUI
// refocuses itself after terminal-capability detection, and focusing
// earlier leaks capability query responses into the main pane as garbage
// escape sequences (see provider.ts spawnSidebar).
//
// Lists panes itself, fresh per call: a caller-shared listing would offer
// the same stashed pane to every window, and join-pane would keep moving
// it instead of spawning new TUIs.
func (t *Tmux) SpawnSidebar(windowID string, width int, position, scriptsDir string) string {
	panes := t.ListAllPanes()
	var target *Pane
	for i := range panes {
		p := &panes[i]
		if p.WindowID != windowID {
			continue
		}
		switch {
		case target == nil:
			target = p
		case position == "left" && p.Left < target.Left:
			target = p
		case position != "left" && p.Right > target.Right:
			target = p
		}
	}
	if target == nil {
		return ""
	}

	// Restore-from-stash first: hide/show cycles park live TUI panes in
	// the stash session rather than killing them.
	for _, p := range panes {
		if p.Session != StashSession || !p.Sidebar {
			continue
		}
		joinFlag := "-h"
		if position == "left" {
			joinFlag = "-hb"
		}
		if _, err := t.Run("join-pane", joinFlag, "-f", "-l", strconv.Itoa(width), "-s", p.ID, "-t", target.ID); err != nil {
			break // fall through to a fresh spawn
		}
		t.markPane(p.ID, sidebarMarkerOption, SidebarPaneTitle)
		return p.ID
	}

	splitFlags := []string{"-h", "-f"}
	if position == "left" {
		splitFlags = []string{"-h", "-b", "-f"}
	}
	return t.SpawnManagedPane(target.ID, splitFlags, width,
		"REFOCUS_WINDOW="+windowID+" exec "+scriptsDir+"/start.sh",
		sidebarMarkerOption, SidebarPaneTitle)
}

// SpawnManagedPane splits targetPane, sizes the new pane, runs command
// inside it, and marks it as tcm-managed. Returns the new pane id ("" on
// failure). splitFlags travel verbatim to split-window so each caller
// pins its exact orientation/placement arg sequence.
func (t *Tmux) SpawnManagedPane(targetPane string, splitFlags []string, size int, command, markerOption, title string) string {
	args := append([]string{"split-window"}, splitFlags...)
	args = append(args, "-l", strconv.Itoa(size), "-t", targetPane, "-P", "-F", "#{pane_id}", command)
	newID, err := t.Run(args...)
	if err != nil || newID == "" {
		return ""
	}
	t.markPane(newID, markerOption, title)
	return newID
}

// markPane stamps the identification title + stable marker option.
func (t *Tmux) markPane(paneID, markerOption, title string) {
	_, _ = t.Run("select-pane", "-t", paneID, "-T", title)
	_, _ = t.Run("set-option", "-p", "-t", paneID, markerOption, markerValue)
}
