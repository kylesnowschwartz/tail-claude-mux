package state

import (
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
)

// Uptime rendering must match the bun server exactly: 17d9h / 3h42m / 12m.
func TestFormatUptime(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0m"},
		{59, "0m"},
		{60, "1m"},
		{3600, "1h0m"},
		{3600*3 + 60*42, "3h42m"},
		{86400*17 + 3600*9 + 61, "17d9h"},
		{-5, ""},
	}
	for _, c := range cases {
		if got := formatUptime(c.secs); got != c.want {
			t.Errorf("formatUptime(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
}

func fakeTmux(outputs map[string]string) *tmux.Tmux {
	return &tmux.Tmux{Run: func(args ...string) (string, error) {
		return outputs[args[0]], nil
	}}
}

func testBuilder(outputs map[string]string) *Builder {
	git := gitinfo.NewCache()
	return &Builder{
		Tmux:  fakeTmux(outputs),
		Git:   git,
		Order: sessionorder.Load(""),
	}
}

func TestBuild_SessionAssemblyAndFocus(t *testing.T) {
	b := testBuilder(map[string]string{
		"list-sessions": "$1\tbeta\t200\t0\t2\t/tmp\n$2\talpha\t100\t1\t3\t/tmp",
		"list-clients":  "c0\t/dev/ttys001\t42\talpha\t120\t40",
		"list-panes":    "alpha\t/tmp", // serves both dirs and counts calls
	})
	st := b.Build()

	if st.Type != "state" {
		t.Errorf("type = %q", st.Type)
	}
	// createdAt ascending: alpha (100) before beta (200).
	if len(st.Sessions) != 2 || st.Sessions[0].Name != "alpha" || st.Sessions[1].Name != "beta" {
		t.Fatalf("session order = %+v", st.Sessions)
	}
	// The lone client's session is current, and focus defaults to it.
	if st.CurrentSession == nil || *st.CurrentSession != "alpha" {
		t.Errorf("currentSession = %v", st.CurrentSession)
	}
	if st.FocusedSession == nil || *st.FocusedSession != "alpha" {
		t.Errorf("focusedSession = %v", st.FocusedSession)
	}
	// Skeleton invariants the TUI relies on: agents/eventTimestamps are
	// [] (not null) and agentState is null.
	if st.Sessions[0].Agents == nil || st.Sessions[0].EventTimestamps == nil {
		t.Error("agents/eventTimestamps must be non-nil empty slices")
	}
	if st.Sessions[0].AgentState != nil {
		t.Error("agentState must be null in the skeleton")
	}
	// Active-pane dir overrides session dir when present.
	if st.Sessions[0].Dir != "/tmp" {
		t.Errorf("dir = %q", st.Sessions[0].Dir)
	}
}

func TestBuild_FocusSurvivesWhileSessionExists(t *testing.T) {
	outputs := map[string]string{
		"list-sessions": "$1\ta\t100\t0\t1\t/tmp\n$2\tb\t200\t0\t1\t/tmp",
		"list-clients":  "c0\t/dev/ttys001\t42\ta\t120\t40",
		"list-panes":    "",
	}
	b := testBuilder(outputs)
	b.Build()
	b.SetFocused("b")
	if st := b.Build(); st.FocusedSession == nil || *st.FocusedSession != "b" {
		t.Errorf("explicit focus must survive rebuild, got %v", st.FocusedSession)
	}

	// When the focused session disappears, fall back to current-or-first.
	outputs["list-sessions"] = "$1\ta\t100\t0\t1\t/tmp"
	if st := b.Build(); st.FocusedSession == nil || *st.FocusedSession != "a" {
		t.Errorf("focus fallback = %v", st.FocusedSession)
	}
}

func TestBuild_NoSessions(t *testing.T) {
	b := testBuilder(map[string]string{})
	st := b.Build()
	if len(st.Sessions) != 0 || st.FocusedSession != nil || st.CurrentSession != nil {
		t.Errorf("empty tmux: got %+v", st)
	}
}
