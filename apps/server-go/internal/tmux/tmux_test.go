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

func TestActiveSessionDirs_FirstHitWins(t *testing.T) {
	tm := &Tmux{Run: fake(map[string]string{
		"list-panes": "proj\t/Users/u/proj/sub\nproj\t/Users/u/elsewhere\nother\t/tmp",
	})}
	got := tm.ActiveSessionDirs()
	if got["proj"] != "/Users/u/proj/sub" || got["other"] != "/tmp" {
		t.Errorf("got %v", got)
	}
}

func TestAllPaneCounts(t *testing.T) {
	tm := &Tmux{Run: fake(map[string]string{
		"list-panes": "proj\nproj\nproj\nother",
	})}
	got := tm.AllPaneCounts()
	if got["proj"] != 3 || got["other"] != 1 {
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
