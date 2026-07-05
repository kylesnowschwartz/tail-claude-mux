package tmux

import (
	"reflect"
	"strings"
	"testing"
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

// paneRow builds one ListAllPanes -F row (12 tab-separated fields, title last).
func paneRow(fields ...string) string { return strings.Join(fields, "\t") }

func TestListAllPanes_Parses(t *testing.T) {
	tm := &Tmux{Run: fake(map[string]string{
		"list-panes": paneRow("proj", "%1", "100", "/Users/u/proj", "1", "", "0", "0", "@1", "0", "119", "zsh") + "\n" +
			paneRow("proj", "%2", "101", "/Users/u/proj", "1", "1", "0", "1", "@1", "120", "152", "sidebar via option") + "\n" +
			paneRow("proj", "%3", "102", "/Users/u/proj", "0", "", "1", "0", "@2", "0", "80", "tcm-sidebar") + "\n" +
			paneRow("other", "%4", "103", "/tmp", "0", "", "x", "y", "@3", "a", "b", "title\twith\ttabs") + "\n" +
			paneRow("bad", "%5", "notapid", "/", "0", "", "0", "0", "@4", "0", "0", "t") + "\n" +
			"malformed",
	})}
	got := tm.ListAllPanes()
	want := []Pane{
		{Session: "proj", ID: "%1", PID: 100, Dir: "/Users/u/proj", WindowActive: true, WindowIndex: 0, PaneIndex: 0, WindowID: "@1", Left: 0, Right: 119, Title: "zsh"},
		{Session: "proj", ID: "%2", PID: 101, Dir: "/Users/u/proj", WindowActive: true, Sidebar: true, WindowIndex: 0, PaneIndex: 1, WindowID: "@1", Left: 120, Right: 152, Title: "sidebar via option"},
		{Session: "proj", ID: "%3", PID: 102, Dir: "/Users/u/proj", Sidebar: true, WindowIndex: 1, PaneIndex: 0, WindowID: "@2", Left: 0, Right: 80, Title: "tcm-sidebar"},
		{Session: "other", ID: "%4", PID: 103, Dir: "/tmp", WindowIndex: -1, PaneIndex: -1, WindowID: "@3", Left: -1, Right: -1, Title: "title\twith\ttabs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestActiveDirs_FirstHitWins_SkipsSidebarAndInactive(t *testing.T) {
	panes := []Pane{
		{Session: "proj", Dir: "/sidebar", WindowActive: true, Sidebar: true},
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
