// Package tmuxtest renders ListAllPanes -F fixture rows for tests. The
// PaneSpec field order below is the ONE shared definition of the row
// format; it must match the -F format string in tmux.ListAllPanes —
// change both together (the parser tests fail loudly on divergence).
// Fields are strings on purpose: tmux emits strings, and tests need
// unparseable junk ("notapid", "x") for the fallback paths.
//
// Deliberately import-free of the tmux package so the tmux package's own
// internal tests can use it without an import cycle.
package tmuxtest

import "strings"

// PaneSpec mirrors tmux.ListAllPanes' -F field order, one field each.
// Zero-value fields render as empty columns (unset marker, unparseable
// number), which is what tmux emits for unset options.
type PaneSpec struct {
	Session      string
	ID           string
	PID          string
	Dir          string
	WindowActive string // "1" when the window is active
	Sidebar      string // "1" for the @tcm-sidebar option
	Companion    string // "1" for the @tcm-companion option
	WindowIndex  string
	PaneIndex    string
	WindowID     string
	Left         string
	Right        string
	Width        string
	WindowWidth  string
	Height       string
	WindowHeight string
	Ignored      string // "1" for the @tcm-ignore session option
	Title        string // last on purpose: the only field that may contain the separator
}

// Row renders the spec as one tab-separated list-panes row.
func (s PaneSpec) Row() string {
	return strings.Join([]string{
		s.Session, s.ID, s.PID, s.Dir, s.WindowActive, s.Sidebar,
		s.Companion, s.WindowIndex, s.PaneIndex, s.WindowID, s.Left,
		s.Right, s.Width, s.WindowWidth, s.Height, s.WindowHeight,
		s.Ignored, s.Title,
	}, "\t")
}

// Listing joins rows into one list-panes output block.
func Listing(rows ...string) string { return strings.Join(rows, "\n") }
