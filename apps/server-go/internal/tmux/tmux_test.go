package tmux

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux/tmuxtest"
)

// fake returns a Runner serving canned output per subcommand.
func fake(outputs map[string]string) Runner {
	return func(args ...string) (string, error) {
		return outputs[args[0]], nil
	}
}

func TestListSessions_ParsesAndFiltersStash(t *testing.T) {
	tm := &Tmux{Run: fake(map[string]string{
		"list-sessions": "$1\tproj\t1781731808\t1\t3\t/Users/u/proj\n" +
			"$2\t_tcm_stash\t1781731809\t0\t1\t/\n" +
			"$3\tother\t1781731810\t0\t2\t/Users/u/other",
	})}
	got := tm.ListSessions()
	want := []Session{
		{ID: "$1", Name: "proj", CreatedAt: 1781731808, Attached: 1, Windows: 3, Dir: "/Users/u/proj"},
		{ID: "$3", Name: "other", CreatedAt: 1781731810, Attached: 0, Windows: 2, Dir: "/Users/u/other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestResolveCurrentSession(t *testing.T) {
	clients := []Client{
		{TTY: "/dev/ttys001", SessionName: "a"},
		{TTY: "/dev/ttys002", SessionName: "b"},
	}
	if name, ok := ResolveCurrentSession(clients, "/dev/ttys002"); !ok || name != "b" {
		t.Errorf("tty match: got %q %v", name, ok)
	}
	if _, ok := ResolveCurrentSession(clients, "/dev/ttys999"); ok {
		t.Error("unknown tty must not resolve")
	}
	// Two clients, no disambiguator: fail closed rather than guess.
	if _, ok := ResolveCurrentSession(clients, ""); ok {
		t.Error("multiple clients without tty must not resolve")
	}
	if name, ok := ResolveCurrentSession(clients[:1], ""); !ok || name != "a" {
		t.Errorf("single client: got %q %v", name, ok)
	}
	if _, ok := ResolveCurrentSession(nil, ""); ok {
		t.Error("no clients must not resolve")
	}
}

func TestListAllPanes_Parses(t *testing.T) {
	tm := &Tmux{Run: fake(map[string]string{
		"list-panes": tmuxtest.Listing(
			tmuxtest.PaneSpec{Session: "proj", ID: "%1", PID: "100", Dir: "/Users/u/proj", WindowActive: "1", WindowIndex: "0", PaneIndex: "0", WindowID: "@1", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "40", WindowHeight: "41", Title: "zsh"}.Row(),
			tmuxtest.PaneSpec{Session: "proj", ID: "%2", PID: "101", Dir: "/Users/u/proj", WindowActive: "1", Sidebar: "1", WindowIndex: "0", PaneIndex: "1", WindowID: "@1", Left: "120", Right: "152", Width: "33", WindowWidth: "153", Height: "33", WindowHeight: "41", Title: "sidebar via option"}.Row(),
			tmuxtest.PaneSpec{Session: "proj", ID: "%3", PID: "102", Dir: "/Users/u/proj", WindowActive: "0", WindowIndex: "1", PaneIndex: "0", WindowID: "@2", Left: "0", Right: "80", Width: "81", WindowWidth: "81", Height: "24", WindowHeight: "24", Title: "tcm-sidebar"}.Row(),
			tmuxtest.PaneSpec{Session: "proj", ID: "%6", PID: "104", Dir: "/Users/u/proj", WindowActive: "1", Companion: "1", WindowIndex: "0", PaneIndex: "2", WindowID: "@1", Left: "120", Right: "152", Width: "33", WindowWidth: "153", Height: "8", WindowHeight: "41", Title: "companion via option"}.Row(),
			tmuxtest.PaneSpec{Session: "proj", ID: "%7", PID: "105", Dir: "/Users/u/proj", WindowActive: "0", WindowIndex: "1", PaneIndex: "1", WindowID: "@2", Left: "0", Right: "80", Width: "81", WindowWidth: "81", Height: "8", WindowHeight: "24", Title: "tcm-companion"}.Row(),
			tmuxtest.PaneSpec{Session: "other", ID: "%4", PID: "103", Dir: "/tmp", WindowActive: "0", WindowIndex: "x", PaneIndex: "y", WindowID: "@3", Left: "a", Right: "b", Width: "c", WindowWidth: "d", Height: "e", WindowHeight: "f", Title: "title\twith\ttabs"}.Row(),
			tmuxtest.PaneSpec{Session: "bad", ID: "%5", PID: "notapid", Dir: "/", WindowActive: "0", WindowIndex: "0", PaneIndex: "0", WindowID: "@4", Left: "0", Right: "0", Width: "0", WindowWidth: "0", Height: "0", WindowHeight: "0", Title: "t"}.Row(),
			"malformed",
		),
	})}
	got := tm.ListAllPanes()
	want := []Pane{
		{Session: "proj", ID: "%1", PID: 100, Dir: "/Users/u/proj", WindowActive: true, WindowIndex: 0, PaneIndex: 0, WindowID: "@1", Left: 0, Right: 119, Width: 120, WindowWidth: 153, Height: 40, WindowHeight: 41, Title: "zsh"},
		{Session: "proj", ID: "%2", PID: 101, Dir: "/Users/u/proj", WindowActive: true, Sidebar: true, WindowIndex: 0, PaneIndex: 1, WindowID: "@1", Left: 120, Right: 152, Width: 33, WindowWidth: 153, Height: 33, WindowHeight: 41, Title: "sidebar via option"},
		{Session: "proj", ID: "%3", PID: 102, Dir: "/Users/u/proj", Sidebar: true, WindowIndex: 1, PaneIndex: 0, WindowID: "@2", Left: 0, Right: 80, Width: 81, WindowWidth: 81, Height: 24, WindowHeight: 24, Title: "tcm-sidebar"},
		{Session: "proj", ID: "%6", PID: 104, Dir: "/Users/u/proj", WindowActive: true, Companion: true, WindowIndex: 0, PaneIndex: 2, WindowID: "@1", Left: 120, Right: 152, Width: 33, WindowWidth: 153, Height: 8, WindowHeight: 41, Title: "companion via option"},
		{Session: "proj", ID: "%7", PID: 105, Dir: "/Users/u/proj", Companion: true, WindowIndex: 1, PaneIndex: 1, WindowID: "@2", Left: 0, Right: 80, Width: 81, WindowWidth: 81, Height: 8, WindowHeight: 24, Title: "tcm-companion"},
		{Session: "other", ID: "%4", PID: 103, Dir: "/tmp", WindowIndex: -1, PaneIndex: -1, WindowID: "@3", Left: -1, Right: -1, Width: -1, WindowWidth: -1, Height: -1, WindowHeight: -1, Title: "title\twith\ttabs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestActiveDirs_FirstHitWins_SkipsSidebarAndInactive(t *testing.T) {
	panes := []Pane{
		{Session: "proj", Dir: "/sidebar", WindowActive: true, Sidebar: true},
		{Session: "proj", Dir: "/companion", WindowActive: true, Companion: true},
		{Session: "proj", Dir: "/Users/u/proj/sub", WindowActive: true},
		{Session: "proj", Dir: "/Users/u/elsewhere", WindowActive: true},
		{Session: "other", Dir: "/inactive-window"},
		{Session: "other", Dir: "/tmp", WindowActive: true},
	}
	got := ActiveDirs(panes)
	if len(got) != 2 || got["proj"] != "/Users/u/proj/sub" || got["other"] != "/tmp" {
		t.Errorf("got %v", got)
	}
}

func TestPaneCounts_CountsSidebarsToo(t *testing.T) {
	panes := []Pane{
		{Session: "proj"}, {Session: "proj"}, {Session: "proj", Sidebar: true},
		{Session: "other"},
	}
	got := PaneCounts(panes)
	if got["proj"] != 3 || got["other"] != 1 {
		t.Errorf("got %v", got)
	}
}

func TestPanePidIndex_SkipsNonPositivePids(t *testing.T) {
	panes := []Pane{
		{Session: "proj", PID: 100},
		{Session: "other", PID: 200},
		{Session: "zero", PID: 0},
	}
	got := PanePidIndex(panes)
	if len(got) != 2 || got[100] != "proj" || got[200] != "other" {
		t.Errorf("got %v", got)
	}
}

func TestSwitchClient_Args(t *testing.T) {
	var captured []string
	tm := &Tmux{Run: func(args ...string) (string, error) {
		captured = args
		return "", nil
	}}
	_ = tm.SwitchClient("proj", "/dev/ttys001")
	if strings.Join(captured, " ") != "switch-client -c /dev/ttys001 -t proj" {
		t.Errorf("args = %v", captured)
	}
	_ = tm.SwitchClient("proj", "")
	if strings.Join(captured, " ") != "switch-client -t proj" {
		t.Errorf("args without tty = %v", captured)
	}
}

// captureRunner serves canned output per subcommand and records every
// command issued, so tests can pin exact tmux traffic.
func captureRunner(outputs map[string]string, cmds *[][]string) Runner {
	return func(args ...string) (string, error) {
		*cmds = append(*cmds, args)
		return outputs[args[0]], nil
	}
}

// mutations strips read-only listing calls, leaving the state-changing
// tmux traffic a behavior-freeze can pin across refactors.
func mutations(cmds [][]string) [][]string {
	var out [][]string
	for _, c := range cmds {
		if c[0] == "list-panes" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// TestSpawnSidebar_CommandSequenceFrozen pins SpawnSidebar's exact tmux
// mutation traffic: the SpawnManagedPane extraction must leave the
// sidebar path byte-identical (CS-007 behavior freeze).
func TestSpawnSidebar_CommandSequenceFrozen(t *testing.T) {
	mainPane := tmuxtest.PaneSpec{Session: "proj", ID: "%1", PID: "100", Dir: "/p", WindowActive: "1", WindowID: "@1", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "40", WindowHeight: "41", Title: "zsh"}.Row()
	stashSidebar := tmuxtest.PaneSpec{Session: StashSession, ID: "%8", PID: "108", Dir: "/p", WindowActive: "1", Sidebar: "1", WindowID: "@9", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "40", WindowHeight: "41", Title: "tcm-sidebar"}.Row()
	cases := []struct {
		name     string
		position string
		listing  string
		want     [][]string
	}{
		{
			name:     "fresh spawn right",
			position: "right",
			listing:  mainPane,
			want: [][]string{
				{"split-window", "-d", "-h", "-f", "-l", "33", "-t", "%1", "-P", "-F", "#{pane_id}", "exec /scripts/start.sh"},
				{"select-pane", "-t", "%9", "-T", "tcm-sidebar"},
				{"set-option", "-p", "-t", "%9", "@tcm-sidebar", "1"},
				{"set-option", "-p", "-q", "-t", "%9", "allow-passthrough", "off"},
			},
		},
		{
			name:     "fresh spawn left",
			position: "left",
			listing:  mainPane,
			want: [][]string{
				{"split-window", "-d", "-h", "-b", "-f", "-l", "33", "-t", "%1", "-P", "-F", "#{pane_id}", "exec /scripts/start.sh"},
				{"select-pane", "-t", "%9", "-T", "tcm-sidebar"},
				{"set-option", "-p", "-t", "%9", "@tcm-sidebar", "1"},
				{"set-option", "-p", "-q", "-t", "%9", "allow-passthrough", "off"},
			},
		},
		{
			name:     "restore from stash left",
			position: "left",
			listing:  mainPane + "\n" + stashSidebar,
			want: [][]string{
				{"join-pane", "-d", "-hb", "-f", "-l", "33", "-s", "%8", "-t", "%1"},
				{"select-pane", "-t", "%8", "-T", "tcm-sidebar"},
				{"set-option", "-p", "-t", "%8", "@tcm-sidebar", "1"},
				{"set-option", "-p", "-q", "-t", "%8", "allow-passthrough", "off"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmds [][]string
			tm := &Tmux{Run: captureRunner(map[string]string{
				"list-panes":   tc.listing,
				"split-window": "%9",
			}, &cmds)}
			tm.SpawnSidebar("@1", 33, tc.position, "/scripts")
			if got := mutations(cmds); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mutations = %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestSpawnManagedPane_CommandSequence(t *testing.T) {
	var cmds [][]string
	tm := &Tmux{Run: captureRunner(map[string]string{"split-window": "%9"}, &cmds)}
	if got := tm.SpawnManagedPane("%1", []string{"-v"}, 8, "watch date", "@tcm-companion", CompanionPaneTitle); got != "%9" {
		t.Fatalf("pane id = %q, want %%9", got)
	}
	want := [][]string{
		{"split-window", "-d", "-v", "-l", "8", "-t", "%1", "-P", "-F", "#{pane_id}", "watch date"},
		{"select-pane", "-t", "%9", "-T", "tcm-companion"},
		{"set-option", "-p", "-t", "%9", "@tcm-companion", "1"},
		{"set-option", "-p", "-q", "-t", "%9", "allow-passthrough", "off"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("cmds = %v\nwant %v", cmds, want)
	}
}

func TestSpawnCompanion_FreshAndRestore(t *testing.T) {
	sidebar := tmuxtest.PaneSpec{Session: "proj", ID: "%2", PID: "101", Dir: "/p", WindowActive: "1", Sidebar: "1", WindowID: "@1", Left: "120", Right: "152", Width: "33", WindowWidth: "153", Height: "33", WindowHeight: "41", Title: "tcm-sidebar"}.Row()
	stashCompanion := tmuxtest.PaneSpec{Session: StashSession, ID: "%8", PID: "108", Dir: "/p", WindowActive: "1", Companion: "1", WindowID: "@9", Left: "0", Right: "119", Width: "120", WindowWidth: "153", Height: "8", WindowHeight: "41", Title: "tcm-companion"}.Row()
	cases := []struct {
		name    string
		listing string
		wantID  string
		want    [][]string
	}{
		{
			name:    "fresh spawn",
			listing: sidebar,
			wantID:  "%9",
			want: [][]string{
				{"split-window", "-d", "-v", "-l", "8", "-t", "%2", "-P", "-F", "#{pane_id}", "watch date"},
				{"select-pane", "-t", "%9", "-T", "tcm-companion"},
				{"set-option", "-p", "-t", "%9", "@tcm-companion", "1"},
				{"set-option", "-p", "-q", "-t", "%9", "allow-passthrough", "off"},
			},
		},
		{
			name:    "restore from stash",
			listing: sidebar + "\n" + stashCompanion,
			wantID:  "%8",
			want: [][]string{
				{"join-pane", "-d", "-v", "-l", "8", "-s", "%8", "-t", "%2"},
				{"select-pane", "-t", "%8", "-T", "tcm-companion"},
				{"set-option", "-p", "-t", "%8", "@tcm-companion", "1"},
				{"set-option", "-p", "-q", "-t", "%8", "allow-passthrough", "off"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmds [][]string
			tm := &Tmux{Run: captureRunner(map[string]string{
				"list-panes":   tc.listing,
				"split-window": "%9",
			}, &cmds)}
			if got := tm.SpawnCompanion("%2", 8, "watch date"); got != tc.wantID {
				t.Fatalf("pane id = %q, want %q", got, tc.wantID)
			}
			if got := mutations(cmds); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mutations = %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestClampCompanionHeight(t *testing.T) {
	cases := []struct {
		rows, windowHeight, want int
	}{
		{8, 41, 8}, // in range
		{8, 12, 6}, // ceiling: half the window
		{1, 41, 3}, // floor
		{8, 6, 3},  // ceiling exactly at the floor: still fits
		{8, 5, 0},  // window too short for the floor: doesn't fit
		{8, 4, 0},  // ditto
		{8, -1, 8}, // unparseable window height: skip ceiling
		{0, 0, 3},  // degenerate: no height info, floor applies
	}
	for _, tc := range cases {
		if got := ClampCompanionHeight(tc.rows, tc.windowHeight); got != tc.want {
			t.Errorf("ClampCompanionHeight(%d, %d) = %d, want %d", tc.rows, tc.windowHeight, got, tc.want)
		}
	}
}

func TestPruneStashOrphans_SparesManagedPanes(t *testing.T) {
	var cmds [][]string
	tm := &Tmux{Run: captureRunner(nil, &cmds)}
	tm.PruneStashOrphans([]Pane{
		{Session: StashSession, ID: "%1", Sidebar: true},
		{Session: StashSession, ID: "%2", Companion: true},
		{Session: StashSession, ID: "%3"}, // stranger: killed
		{Session: "proj", ID: "%4"},       // not in stash: untouched
	})
	want := [][]string{{"kill-pane", "-t", "%3"}}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("cmds = %v\nwant %v", cmds, want)
	}
}
