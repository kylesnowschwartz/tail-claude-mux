// Package widthsync ports the sidebar width math from
// packages/runtime/src/server/sidebar-width-sync.ts. The TypeScript module
// is the contract of record while both servers exist; every constant,
// truncation limit, and layout column count here must stay identical to it.
//
// Name/branch/agent lengths use len() (UTF-8 bytes) where the TS uses
// .length (UTF-16 code units). Both measures undercount wide glyphs (CJK,
// emoji) the same way — neither is a terminal-cell count — and the TS
// behavior is the contract. For the ASCII session, branch, and agent names
// tcm produces in practice the two agree exactly.
package widthsync

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// AbsoluteMin is sidebar-width-sync.ts ABSOLUTE_MIN_SIDEBAR_WIDTH.
const AbsoluteMin = 15

// DefaultWidth is sidebar-width-sync.ts DEFAULT_SIDEBAR_WIDTH.
//
// Sized for both column tenants. Activity zone: at 33+ cols the
// description budget fits the median `tmux …` /
// `apps/tui/src/index.tsx`-style payloads without truncation. Companion
// (gearshifter strip --compact, 12 rows): the chip flow wraps past 12
// rows below 37 cols and degrades to "canvas too small" — 37 is the
// narrowest width where both fit. Above ~37 the editing pane starts
// feeling pinched on a 192-col terminal.
//
// `=` (equalize-width) snaps back to this constant. The runtime may push
// further upward via enforceSidebarWidth when session/agent content needs
// more room — this is the floor preference, not a hard cap.
const DefaultWidth = 37

// MaxWidthPercent is sidebar-width-sync.ts MAX_SIDEBAR_WIDTH_PERCENT.
const MaxWidthPercent = 0.4

// SaveDebounce is sidebar-width-sync.ts SAVE_DEBOUNCE_MS (1000 ms).
const SaveDebounce = 1000 * time.Millisecond

// Layout constants matching the TUI's SessionCard rendering
// (sidebar-width-sync.ts private constants).
const (
	paddingLeft      = 1
	paddingRight     = 1
	statusIconCols   = 2 // " ⠋" or " ●"
	focusedBorder    = 2 // <box border> on focused card: 1 col per side
	nameTruncLimit   = 18
	branchTruncLimit = 15
	branchIconCols   = 2 // "⎇ " prefix when branch is present
	dirMismatchCols  = 2 // " 󱧋" suffix on focused branch row when cwd leaf differs from session name

	// Expanded agent list item layout (inside focused card border + paddingLeft={1}):
	//   expandPad(1) + dismiss(2) + name + [threadId] + [unseenBadge] + statusIcon(2) + padRight(1)
	agentRowFixed = 1 + 2 + 2 + 1 // = 6
	threadIDCols  = 6             // " #" + 4 chars (threadId.slice(-4))
)

// ComputeMin ports computeMinSidebarWidth: the narrowest sidebar width that
// still fits session content without clipping. Mirrors the TUI's
// SessionCard layout math.
//
// Collapsed card rows (all cards):
//
//	Row 1 (name):   index(3) + name + badge + spacer + statusIcon(2) + pad(1)
//	Row 2 (branch): index(3) + branch + pad(1)
//
// Expanded agent row (focused card only, inside border):
//
//	border(1) + expandPad(3) + agentPad(1) + icon+space(2) + agentName + statusLabel + dismiss(2) + pad(1) + border(1)
//
// The focused card is wrapped in a border box, so pane = content + 2.
func ComputeMin(sessions []wire.SessionData) int {
	widestContent := 0

	for _, s := range sessions {
		// Collapsed name row
		nameLen := min(len(s.Name), nameTruncLimit)
		badge := agentBadgeWidth(s)
		unseenCols := 0
		if s.Unseen {
			unseenCols = 2 // " ●" when session has unseen agents
		}
		nameRow := paddingLeft + nameLen + badge + unseenCols + statusIconCols + paddingRight

		// Collapsed branch row (renders when branch is present)
		branchLen := min(len(s.Branch), branchTruncLimit)
		branchRow := 0
		if branchLen > 0 {
			// Focused-card branch row also carries the dir-mismatch glyph when
			// the cwd's leaf segment disagrees with the session name. Any card
			// may become focused, so measure for it whenever the mismatch holds.
			mismatchCols := 0
			if hasDirMismatch(s) {
				mismatchCols = dirMismatchCols
			}
			branchRow = paddingLeft + branchIconCols + branchLen + mismatchCols + paddingRight
		}

		widestContent = max(widestContent, nameRow, branchRow)

		// Expanded agent rows (only the focused card shows these, but any
		// card can become focused, so measure all of them)
		for _, a := range s.Agents {
			threadCols := 0
			if a.ThreadID != "" {
				threadCols = threadIDCols
			}
			unseenBadge := 0
			if a.Unseen {
				unseenBadge = 2 // " ●" when instance is unseen
			}
			agentRow := agentRowFixed + len(a.Agent) + threadCols + unseenBadge
			widestContent = max(widestContent, agentRow)
		}
	}

	return max(AbsoluteMin, widestContent+focusedBorder)
}

// hasDirMismatch mirrors the TUI's dirMismatch predicate: leaf segment of
// cwd differs from the session name (after stripping a leading $HOME, which
// doesn't affect the leaf). Kept inline so this package stays
// self-contained — no cross-package dep on the TUI's formatDir helper.
func hasDirMismatch(s wire.SessionData) bool {
	if s.Dir == "" {
		return false
	}
	leaf := ""
	for _, seg := range strings.Split(s.Dir, "/") {
		if seg != "" {
			leaf = seg
		}
	}
	return leaf != "" && leaf != s.Name
}

// agentBadgeWidth mirrors the TUI's agentCount/agentBadge logic:
// " ●" for 1, " ●N" for N>1.
func agentBadgeWidth(s wire.SessionData) int {
	count := 0
	for _, a := range s.Agents {
		if a.Liveness == wire.LivenessAlive ||
			(a.Liveness != wire.LivenessExited && !wire.IsTerminalStatus(a.Status)) {
			count++
		}
	}
	if count == 0 {
		return 0
	}
	if count == 1 {
		return 2
	}
	// " ●" = 2, " ●2" = 3, " ●10" = 4, etc.
	return 1 + 1 + len(strconv.Itoa(count))
}

// Clamp ports clampSidebarWidth. windowWidth 0 means unbounded (the TS
// signature's omitted/falsy windowWidth): no percentage max is enforced.
func Clamp(width, windowWidth int) int {
	clamped := max(AbsoluteMin, width)
	if windowWidth == 0 {
		return clamped
	}
	maxWidth := int(math.Floor(float64(windowWidth) * MaxWidthPercent))
	return min(maxWidth, clamped)
}
