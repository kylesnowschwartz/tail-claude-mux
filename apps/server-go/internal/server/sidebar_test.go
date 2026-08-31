package server

import (
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux/tmuxtest"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// Canned rows for one window @1 in session proj, 153x41.
func mainRow() string {
	return tmuxtest.PaneSpec{Session: "proj", ID: "%1", PID: "100", Dir: "/p", WindowActive: "1", WindowID: "@1", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "41", WindowHeight: "41", Title: "zsh"}.Row()
}
func sidebarRow() string {
	return tmuxtest.PaneSpec{Session: "proj", ID: "%2", PID: "101", Dir: "/p", WindowActive: "1", Sidebar: "1", WindowID: "@1", Left: "120", Right: "152", Width: "33", WindowWidth: "153", Height: "33", WindowHeight: "41", Title: "tcm-sidebar"}.Row()
}
func companionRow() string {
	return tmuxtest.PaneSpec{Session: "proj", ID: "%3", PID: "102", Dir: "/p", WindowActive: "1", Companion: "1", WindowID: "@1", Left: "120", Right: "152", Width: "33", WindowWidth: "153", Height: "8", WindowHeight: "41", Title: "tcm-companion"}.Row()
}
func ignoredMainRow() string {
	return tmuxtest.PaneSpec{Session: "revdiff-42", ID: "%4", PID: "104", Dir: "/p", WindowActive: "1", WindowID: "@2", Left: "0", Right: "152", Width: "153", WindowWidth: "153", Height: "41", WindowHeight: "41", Ignored: "1", Title: "revdiff"}.Row()
}
func stashSidebarRow() string {
	return tmuxtest.PaneSpec{Session: tmux.StashSession, ID: "%8", PID: "108", Dir: "/p", WindowActive: "1", Sidebar: "1", WindowID: "@9", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "40", WindowHeight: "41", Title: "tcm-sidebar"}.Row()
}
func stashCompanionRow() string {
	return tmuxtest.PaneSpec{Session: tmux.StashSession, ID: "%9", PID: "109", Dir: "/p", WindowActive: "1", Companion: "1", WindowID: "@9", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "8", WindowHeight: "41", Title: "tcm-companion"}.Row()
}

func rows(rs ...string) string { return tmuxtest.Listing(rs...) }

// sequencedRunner serves list-panes output from a queue (last entry
// sticky) so multi-step flows can see the layout evolve, records every
// command, and serves static output for everything else.
func sequencedRunner(listings []string, outputs map[string]string, cmds *[][]string) tmux.Runner {
	i := 0
	return func(args ...string) (string, error) {
		*cmds = append(*cmds, args)
		if args[0] == "list-panes" {
			out := listings[i]
			if i < len(listings)-1 {
				i++
			}
			return out, nil
		}
		return outputs[args[0]], nil
	}
}

func newTestServer(run tmux.Runner, companion state.CompanionPaneConfig) *Server {
	s := &Server{Builder: &state.Builder{Tmux: &tmux.Tmux{Run: run}, SidebarWidth: 33}}
	s.ScriptsDir = "/scripts"
	s.CompanionPane = companion
	s.sidebarVisible = true
	return s
}

// only keeps the commands whose subcommand is in names, preserving order.
func only(cmds [][]string, names ...string) [][]string {
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	var out [][]string
	for _, c := range cmds {
		if keep[c[0]] {
			out = append(out, c)
		}
	}
	return out
}

// Struct panes for driving ensureCompanionInWindow directly (it takes
// the caller's parsed listing).
func mainPane() tmux.Pane {
	return tmux.Pane{Session: "proj", ID: "%1", WindowID: "@1", WindowActive: true, Height: 41, WindowHeight: 41}
}
func sidebarPane() tmux.Pane {
	return tmux.Pane{Session: "proj", ID: "%2", WindowID: "@1", Sidebar: true, Height: 33, WindowHeight: 41}
}
func companionPane() tmux.Pane {
	return tmux.Pane{Session: "proj", ID: "%3", WindowID: "@1", Companion: true, Height: 8, WindowHeight: 41}
}

func TestEnsureCompanionInWindow_FeatureOff_NoTmuxTraffic(t *testing.T) {
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{""}, nil, &cmds), state.CompanionPaneConfig{})
	s.ensureCompanionInWindow("@1", []tmux.Pane{mainPane(), sidebarPane()}, "")
	if len(cmds) != 0 {
		t.Errorf("feature off must issue zero tmux commands, got %v", cmds)
	}
}

func TestEnsureCompanionInWindow_SpawnsBelowSidebar(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{""}, map[string]string{"split-window": "%3"}, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1", []tmux.Pane{mainPane(), sidebarPane()}, "")
	want := [][]string{{"split-window", "-d", "-v", "-l", "8", "-t", "%2", "-P", "-F", "#{pane_id}", "watch date"}}
	if got := only(cmds, "split-window"); !reflect.DeepEqual(got, want) {
		t.Errorf("split-window = %v\nwant %v", got, want)
	}
}

func TestEnsureCompanionInWindow_WindowTooShort_NoSpawn(t *testing.T) {
	// Window height 5: ceiling (5/2=2) is below the 3-row floor — the
	// split would fail "pane too small" on every retry, so skip entirely.
	short := sidebarPane()
	short.Height, short.WindowHeight = 5, 5
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{""}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1", []tmux.Pane{short}, "")
	if got := only(cmds, "split-window", "join-pane"); len(got) != 0 {
		t.Errorf("too-short window must not spawn, got %v", got)
	}
}

func TestEnsureCompanionInWindow_Idempotent(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{""}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1", []tmux.Pane{mainPane(), sidebarPane(), companionPane()}, "")
	if got := only(cmds, "split-window", "join-pane"); len(got) != 0 {
		t.Errorf("existing companion must not respawn, got %v", got)
	}
}

func TestEnsureCompanionInWindow_NoSidebar_NoSpawn(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{""}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1", []tmux.Pane{mainPane()}, "")
	if got := only(cmds, "split-window", "join-pane"); len(got) != 0 {
		t.Errorf("companion must target an existing sidebar, got %v", got)
	}
}

func TestSpawnInActiveWindows_SkipsIgnoredSession(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{rows(mainRow(), ignoredMainRow())}, map[string]string{"split-window": "%9"}, &cmds),
		state.CompanionPaneConfig{},
	)
	s.SidebarPosition = "right"
	s.spawnInActiveWindows()
	want := [][]string{
		{"split-window", "-d", "-h", "-f", "-l", "33", "-t", "%1", "-P", "-F", "#{pane_id}", "exec /scripts/start.sh"},
	}
	if got := only(cmds, "split-window", "join-pane"); !reflect.DeepEqual(got, want) {
		t.Errorf("spawns = %v\nwant only the normal window's sidebar %v", got, want)
	}
}

func TestEnsureSidebarInWindow_IgnoredWindow_NoSpawnButGeometryEnforced(t *testing.T) {
	// The sidebar in the NORMAL window @1 has drifted to width 40: the
	// deferred enforceGeometry must still repair it even when the ensure
	// targets the ignored session's window.
	drifted := tmuxtest.PaneSpec{Session: "proj", ID: "%2", PID: "101", Dir: "/p", WindowActive: "1", Sidebar: "1", WindowID: "@1", Left: "113", Right: "152", Width: "40", WindowWidth: "153", Height: "33", WindowHeight: "41", Title: "tcm-sidebar"}.Row()
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{rows(mainRow(), drifted, ignoredMainRow())}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureSidebarInWindow("@2")
	if got := only(cmds, "split-window", "join-pane", "kill-pane"); len(got) != 0 {
		t.Errorf("ignored window must get no managed panes, got %v", got)
	}
	wantResize := [][]string{{"resize-pane", "-t", "%2", "-x", "33"}}
	if got := only(cmds, "resize-pane"); !reflect.DeepEqual(got, wantResize) {
		t.Errorf("resize-pane = %v\nwant %v (geometry enforcement must survive the ignore gate)", got, wantResize)
	}
}

func TestHandlePaneExited_OrphanPredicate(t *testing.T) {
	otherMain := tmuxtest.PaneSpec{Session: "proj", ID: "%5", PID: "105", Dir: "/p", WindowActive: "1", WindowIndex: "1", WindowID: "@2", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "41", WindowHeight: "41", Title: "zsh"}.Row()
	cases := []struct {
		name      string
		listing   string
		wantKills []string
	}{
		{"sidebar+companion alone: both killed", rows(sidebarRow(), companionRow(), otherMain), []string{"%2", "%3"}},
		{"main+sidebar+companion: untouched", rows(mainRow(), sidebarRow(), companionRow()), nil},
		{"sidebar alone: killed (current behavior)", rows(sidebarRow(), otherMain), []string{"%2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmds [][]string
			s := newTestServer(sequencedRunner([]string{tc.listing}, nil, &cmds), state.CompanionPaneConfig{})
			s.handlePaneExited(httptest.NewRecorder(), nil)
			var kills []string
			for _, c := range only(cmds, "kill-pane") {
				kills = append(kills, c[2])
			}
			sort.Strings(kills)
			if !reflect.DeepEqual(kills, tc.wantKills) {
				t.Errorf("kills = %v, want %v", kills, tc.wantKills)
			}
		})
	}
}

func TestHandlePaneExited_TransientListingGap_NoKill(t *testing.T) {
	// list-panes can transiently omit a live pane; the sweep's confirm
	// listing shows the user pane again, so the sidebar must survive.
	otherMain := tmuxtest.PaneSpec{Session: "proj", ID: "%5", PID: "105", Dir: "/p", WindowActive: "1", WindowIndex: "1", WindowID: "@2", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "41", WindowHeight: "41", Title: "zsh"}.Row()
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{
		rows(sidebarRow(), companionRow(), otherMain),            // sweep listing: @1's user pane missing
		rows(mainRow(), sidebarRow(), companionRow(), otherMain), // confirm listing: it is back
	}, nil, &cmds), state.CompanionPaneConfig{})
	s.handlePaneExited(httptest.NewRecorder(), nil)
	if got := only(cmds, "kill-pane"); len(got) != 0 {
		t.Errorf("a window orphaned in only one listing must keep its panes, got kills %v", got)
	}
}

func TestHandleToggle_StashesAndRestoresCompanion(t *testing.T) {
	companion := state.CompanionPaneConfig{Command: "watch date", Rows: 8}

	// Hide: both panes join-pane'd into the stash session.
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{rows(mainRow(), sidebarRow(), companionRow())}, nil, &cmds), companion)
	s.handleToggle(httptest.NewRecorder(), nil)
	wantHide := [][]string{
		{"join-pane", "-d", "-s", "%2", "-t", tmux.StashSession + ":"},
		{"join-pane", "-d", "-s", "%3", "-t", tmux.StashSession + ":"},
	}
	if got := only(cmds, "join-pane"); !reflect.DeepEqual(got, wantHide) {
		t.Errorf("hide join-pane = %v\nwant %v", got, wantHide)
	}
	if s.sidebarVisible {
		t.Error("toggle off must clear sidebarVisible")
	}

	// Show: sidebar restored from stash, then companion restored from
	// stash below it (targeting the freshly restored sidebar %8).
	// Listings evolve as the layout changes underneath.
	cmds = nil
	s = newTestServer(sequencedRunner([]string{
		rows(mainRow(), stashSidebarRow(), stashCompanionRow()), // spawnInActiveWindows
		rows(mainRow(), stashSidebarRow(), stashCompanionRow()), // SpawnSidebar
		rows(mainRow(), sidebarRow(), stashCompanionRow()),      // SpawnCompanion (stash scan)
		rows(mainRow(), sidebarRow(), companionRow()),           // enforceGeometry
	}, nil, &cmds), companion)
	s.sidebarVisible = false
	s.handleToggle(httptest.NewRecorder(), nil)
	wantShow := [][]string{
		{"join-pane", "-d", "-h", "-f", "-l", "33", "-s", "%8", "-t", "%1"},
		{"join-pane", "-d", "-v", "-l", "8", "-s", "%9", "-t", "%8"},
	}
	if got := only(cmds, "join-pane"); !reflect.DeepEqual(got, wantShow) {
		t.Errorf("show join-pane = %v\nwant %v", got, wantShow)
	}
	if got := only(cmds, "split-window"); len(got) != 0 {
		t.Errorf("show must restore from stash, not spawn fresh: %v", got)
	}
}

func TestEnsureSidebarInWindow_KillsStrandedCompanion(t *testing.T) {
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{
		rows(mainRow(), companionRow()),               // ensureSidebarInWindow: sidebar died
		mainRow(),                                     // SpawnSidebar: companion killed
		rows(mainRow(), sidebarRow()),                 // SpawnCompanion (stash scan)
		rows(mainRow(), sidebarRow(), companionRow()), // enforceGeometry
	}, map[string]string{"split-window": "%2"}, &cmds), state.CompanionPaneConfig{Command: "watch date", Rows: 8})
	s.SidebarPosition = "right"
	s.ensureSidebarInWindow("@1")

	wantKills := [][]string{{"kill-pane", "-t", "%3"}}
	if got := only(cmds, "kill-pane"); !reflect.DeepEqual(got, wantKills) {
		t.Errorf("kill-pane = %v\nwant %v", got, wantKills)
	}
	wantSpawns := [][]string{
		{"split-window", "-d", "-h", "-f", "-l", "33", "-t", "%1", "-P", "-F", "#{pane_id}", "exec /scripts/start.sh"},
		{"split-window", "-d", "-v", "-l", "8", "-t", "%2", "-P", "-F", "#{pane_id}", "watch date"},
	}
	if got := only(cmds, "split-window"); !reflect.DeepEqual(got, wantSpawns) {
		t.Errorf("split-window = %v\nwant %v", got, wantSpawns)
	}
}

func TestKillForReload_KillsCompanionsWithSidebars(t *testing.T) {
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{""}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8})
	s.killForReload([]tmux.Pane{mainPane(), sidebarPane(), companionPane()})
	want := [][]string{
		{"kill-pane", "-t", "%2"},
		{"kill-pane", "-t", "%3"},
		{"kill-session", "-t", tmux.StashSession},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("cmds = %v\nwant %v", cmds, want)
	}
}

func TestKillForReload_FeatureOff_OnlySidebars(t *testing.T) {
	// Feature-off leftovers are bootstrap's teardown's job; the reload
	// kill must not duplicate those kill-pane commands.
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{""}, nil, &cmds), state.CompanionPaneConfig{})
	s.killForReload([]tmux.Pane{mainPane(), sidebarPane(), companionPane()})
	want := [][]string{
		{"kill-pane", "-t", "%2"},
		{"kill-session", "-t", tmux.StashSession},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("cmds = %v\nwant %v", cmds, want)
	}
}

func TestReportWidth_IgnoredWhileSweepPending(t *testing.T) {
	// A width report racing a pane-exit sweep is the dying window's
	// sidebar ballooning into the dead pane's space, not a user drag —
	// it must not arm a save.
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{rows(mainRow(), sidebarRow())}, nil, &cmds), state.CompanionPaneConfig{})
	for name, arm := range map[string]func(){
		"debounce pending": func() { s.paneExitedTimer = time.AfterFunc(time.Hour, func() {}) },
		"sweep in flight":  func() { s.sweepActive = true },
	} {
		t.Run(name, func(t *testing.T) {
			s.paneExitedTimer, s.sweepActive = nil, false
			arm()
			s.mu.Lock()
			s.reportWidthLocked(&client{sessionName: "proj"}, wire.ClientCommand{Type: wire.CmdReportWidth, Width: 120})
			saveArmed := s.saveTimer != nil
			s.mu.Unlock()
			if saveArmed {
				t.Errorf("width report during sweep armed a save")
			}
			if s.paneExitedTimer != nil {
				s.paneExitedTimer.Stop()
			}
		})
	}
}

func TestSweep_KillCancelsPendingSave(t *testing.T) {
	// The balloon report can land before the sweep starts; killing the
	// pane that sent it must discard the save it armed.
	otherMain := tmuxtest.PaneSpec{Session: "proj", ID: "%5", PID: "105", Dir: "/p", WindowActive: "1", WindowIndex: "1", WindowID: "@2", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "41", WindowHeight: "41", Title: "zsh"}.Row()
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{rows(sidebarRow(), companionRow(), otherMain)}, nil, &cmds), state.CompanionPaneConfig{})
	s.mu.Lock()
	s.saveTimer = time.AfterFunc(time.Hour, func() {})
	s.mu.Unlock()
	s.handlePaneExited(httptest.NewRecorder(), nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveTimer != nil {
		t.Errorf("orphan kill left the pending width save armed")
	}
}
