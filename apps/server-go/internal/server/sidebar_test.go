package server

import (
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
)

// paneRow builds one ListAllPanes -F row (17 tab-separated fields, title
// last) — mirrors the helper in internal/tmux/tmux_test.go.
func paneRow(fields ...string) string { return strings.Join(fields, "\t") }

// Canned rows for one window @1 in session proj, 153x41.
func mainRow() string {
	return paneRow("proj", "%1", "100", "/p", "1", "", "", "0", "0", "@1", "0", "119", "120", "153", "41", "41", "zsh")
}
func sidebarRow() string {
	return paneRow("proj", "%2", "101", "/p", "1", "1", "", "0", "1", "@1", "120", "152", "33", "153", "33", "41", "tcm-sidebar")
}
func companionRow() string {
	return paneRow("proj", "%3", "102", "/p", "1", "", "1", "0", "2", "@1", "120", "152", "33", "153", "8", "41", "tcm-companion")
}
func stashSidebarRow() string {
	return paneRow(tmux.StashSession, "%8", "108", "/p", "1", "1", "", "0", "0", "@9", "0", "119", "120", "153", "40", "41", "tcm-sidebar")
}
func stashCompanionRow() string {
	return paneRow(tmux.StashSession, "%9", "109", "/p", "1", "", "1", "0", "1", "@9", "0", "119", "120", "153", "8", "41", "tcm-companion")
}

func rows(rs ...string) string { return strings.Join(rs, "\n") }

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

func TestEnsureCompanionInWindow_FeatureOff_NoTmuxTraffic(t *testing.T) {
	var cmds [][]string
	s := newTestServer(sequencedRunner([]string{rows(mainRow(), sidebarRow())}, nil, &cmds), state.CompanionPaneConfig{})
	s.ensureCompanionInWindow("@1")
	if len(cmds) != 0 {
		t.Errorf("feature off must issue zero tmux commands, got %v", cmds)
	}
}

func TestEnsureCompanionInWindow_SpawnsBelowSidebar(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{rows(mainRow(), sidebarRow())}, map[string]string{"split-window": "%3"}, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1")
	want := [][]string{{"split-window", "-d", "-v", "-l", "8", "-t", "%2", "-P", "-F", "#{pane_id}", "watch date"}}
	if got := only(cmds, "split-window"); !reflect.DeepEqual(got, want) {
		t.Errorf("split-window = %v\nwant %v", got, want)
	}
}

func TestEnsureCompanionInWindow_WindowTooShort_NoSpawn(t *testing.T) {
	// Window height 5: ceiling (5/2=2) is below the 3-row floor — the
	// split would fail "pane too small" on every retry, so skip entirely.
	shortMain := paneRow("proj", "%1", "100", "/p", "1", "", "", "0", "0", "@1", "0", "119", "120", "153", "5", "5", "zsh")
	shortSidebar := paneRow("proj", "%2", "101", "/p", "1", "1", "", "0", "1", "@1", "120", "152", "33", "153", "5", "5", "tcm-sidebar")
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{rows(shortMain, shortSidebar)}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1")
	if got := only(cmds, "split-window", "join-pane"); len(got) != 0 {
		t.Errorf("too-short window must not spawn, got %v", got)
	}
}

func TestEnsureCompanionInWindow_Idempotent(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{rows(mainRow(), sidebarRow(), companionRow())}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1")
	if got := only(cmds, "split-window", "join-pane"); len(got) != 0 {
		t.Errorf("existing companion must not respawn, got %v", got)
	}
}

func TestEnsureCompanionInWindow_NoSidebar_NoSpawn(t *testing.T) {
	var cmds [][]string
	s := newTestServer(
		sequencedRunner([]string{mainRow()}, nil, &cmds),
		state.CompanionPaneConfig{Command: "watch date", Rows: 8},
	)
	s.ensureCompanionInWindow("@1")
	if got := only(cmds, "split-window", "join-pane"); len(got) != 0 {
		t.Errorf("companion must target an existing sidebar, got %v", got)
	}
}

func TestHandlePaneExited_OrphanPredicate(t *testing.T) {
	otherMain := paneRow("proj", "%5", "105", "/p", "1", "", "", "1", "0", "@2", "0", "119", "120", "153", "41", "41", "zsh")
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
	// stash below it. Listings evolve as the layout changes underneath.
	cmds = nil
	s = newTestServer(sequencedRunner([]string{
		rows(mainRow(), stashSidebarRow(), stashCompanionRow()), // spawnInActiveWindows
		rows(mainRow(), stashSidebarRow(), stashCompanionRow()), // SpawnSidebar
		rows(mainRow(), sidebarRow(), stashCompanionRow()),      // ensureCompanionInWindow
		rows(mainRow(), sidebarRow(), stashCompanionRow()),      // SpawnCompanion
		rows(mainRow(), sidebarRow(), companionRow()),           // enforce passes
	}, nil, &cmds), companion)
	s.sidebarVisible = false
	s.handleToggle(httptest.NewRecorder(), nil)
	wantShow := [][]string{
		{"join-pane", "-h", "-f", "-l", "33", "-s", "%8", "-t", "%1"},
		{"join-pane", "-d", "-v", "-l", "8", "-s", "%9", "-t", "%2"},
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
		rows(mainRow(), sidebarRow()),                 // ensureCompanionInWindow
		rows(mainRow(), sidebarRow()),                 // SpawnCompanion
		rows(mainRow(), sidebarRow(), companionRow()), // enforce passes
	}, map[string]string{"split-window": "%2"}, &cmds), state.CompanionPaneConfig{Command: "watch date", Rows: 8})
	s.SidebarPosition = "right"
	s.ensureSidebarInWindow("@1")

	wantKills := [][]string{{"kill-pane", "-t", "%3"}}
	if got := only(cmds, "kill-pane"); !reflect.DeepEqual(got, wantKills) {
		t.Errorf("kill-pane = %v\nwant %v", got, wantKills)
	}
	wantSpawns := [][]string{
		{"split-window", "-h", "-f", "-l", "33", "-t", "%1", "-P", "-F", "#{pane_id}", "REFOCUS_WINDOW=@1 exec /scripts/start.sh"},
		{"split-window", "-d", "-v", "-l", "8", "-t", "%2", "-P", "-F", "#{pane_id}", "watch date"},
	}
	if got := only(cmds, "split-window"); !reflect.DeepEqual(got, wantSpawns) {
		t.Errorf("split-window = %v\nwant %v", got, wantSpawns)
	}
}
