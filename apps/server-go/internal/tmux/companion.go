// Companion pane operations: tcm's generic second managed pane — an
// arbitrary user-configured command stacked below the sidebar in the same
// column, sharing the sidebar's spawn/stash mechanism. tcm never knows
// what the command is; the guest never knows about tcm (see
// docs/specs/companion-pane.md).
package tmux

import "strconv"

const (
	// CompanionPaneTitle is the identification title; the @tcm-companion
	// pane option is the stable marker (survives pane_title rewriting by
	// escape sequences the guest may emit).
	CompanionPaneTitle    = "tcm-companion"
	companionMarkerOption = "@tcm-companion"
)

const (
	// companionMinRows is the height floor: below 3 rows most guests
	// can't render a single widget line plus borders.
	companionMinRows = 3
	// companionCeilingDiv caps the companion at 1/companionCeilingDiv of
	// the window height — the sidebar keeps the majority of the column.
	companionCeilingDiv = 2
)

// CompanionPanes derives the non-stash companion panes from a listing.
func CompanionPanes(panes []Pane) []Pane {
	var out []Pane
	for _, p := range panes {
		if p.Companion && p.Session != StashSession {
			out = append(out, p)
		}
	}
	return out
}

// SpawnCompanion creates a companion pane directly below the sidebar pane
// and returns its pane id ("" on failure). A stashed companion pane is
// restored via join-pane when one exists (hide/show cycles park live
// guests in the stash rather than killing them); otherwise a fresh pane
// runs command. Both paths pass -d: unlike the sidebar TUI, which
// refocuses the main pane itself after spawning focused, an arbitrary
// guest never gives focus back — so the companion must never take it.
//
// Lists panes itself, fresh per call, for the same reason SpawnSidebar
// does: a caller-shared listing would offer the same stashed pane to
// every window.
func (t *Tmux) SpawnCompanion(sidebarPaneID string, rows int, command string) string {
	for _, p := range t.ListAllPanes() {
		if p.Session != StashSession || !p.Companion {
			continue
		}
		if _, err := t.Run("join-pane", "-d", "-v", "-l", strconv.Itoa(rows), "-s", p.ID, "-t", sidebarPaneID); err != nil {
			break // fall through to a fresh spawn
		}
		t.markPane(p.ID, companionMarkerOption, CompanionPaneTitle)
		return p.ID
	}
	return t.SpawnManagedPane(sidebarPaneID, []string{"-d", "-v"}, rows, command,
		companionMarkerOption, CompanionPaneTitle)
}

// ResizePaneHeight sets a pane's height in rows (the height analog of
// ResizePane).
func (t *Tmux) ResizePaneHeight(paneID string, height int) {
	_, _ = t.Run("resize-pane", "-t", paneID, "-y", strconv.Itoa(height))
}

// ClampCompanionHeight bounds the configured companion rows: floor of
// companionMinRows, ceiling of the window-height share companions may
// take. Returns 0 when the window is too short to host even the floor —
// callers must skip spawn/resize rather than retry a split tmux will
// reject ("pane too small") on every hook.
func ClampCompanionHeight(rows, windowHeight int) int {
	if windowHeight > 0 {
		ceiling := windowHeight / companionCeilingDiv
		if ceiling < companionMinRows {
			return 0 // window can't fit a companion at all
		}
		if rows > ceiling {
			rows = ceiling
		}
	}
	if rows < companionMinRows {
		return companionMinRows
	}
	return rows
}
