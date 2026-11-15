package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/panescan"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux/tmuxtest"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
)

// GET /state serves the liaison link from the pane options tmux lists, for
// the conversation the pane names as the worker and for no other. The fake
// tmux lists one pane per case; a fake ps puts a claude process in each
// pane except the shell-only one.
func TestStateServesLiaisonLinkFromPaneOptions(t *testing.T) {
	const (
		parent = "11111111-2222-4333-8444-555555555555"
		worker = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		other  = "99999999-8888-4777-8666-555555555555"
	)
	// One tmux session per case, so each case has its own agentState.
	cases := []struct {
		session              string
		work, parent, worker string
		threadID             string // "" with shell: no conversation in the pane
		shell                bool
		wantLink             bool
	}{
		{session: "valid", work: "w7", parent: parent, worker: worker, threadID: worker, wantLink: true},
		{session: "no-options", threadID: worker},
		{session: "tab", work: "w\t7", parent: parent + "\t", worker: worker, threadID: worker},
		{session: "line-break", work: "w7\n", parent: "\n" + parent, worker: worker, threadID: worker},
		{session: "wrong-form", work: "work-7", parent: "liaison", worker: worker, threadID: worker},
		{session: "shell", work: "w7", parent: parent, worker: worker, shell: true},
		{session: "other-conversation", work: "w7", parent: parent, worker: worker, threadID: other},
	}

	var rows, sessions, ps []string
	for i, tc := range cases {
		shellPID, agentPID := 100+i, 200+i
		rows = append(rows, tmuxtest.PaneSpec{
			Session: tc.session, ID: fmt.Sprintf("%%%d", i), PID: fmt.Sprint(shellPID), Dir: "/p",
			WindowActive: "1", WindowIndex: "0", PaneIndex: "0", WindowID: fmt.Sprintf("@%d", i),
			LiaisonWork: tc.work, LiaisonParent: tc.parent, LiaisonWorker: tc.worker, Title: "t",
		}.Row())
		sessions = append(sessions, fmt.Sprintf("$%d\t%s\t100\t1\t1\t/p\t0", i, tc.session))
		ps = append(ps, fmt.Sprintf("%d 1 zsh", shellPID))
		if !tc.shell {
			ps = append(ps, fmt.Sprintf("%d %d claude", agentPID, shellPID))
		}
	}
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return strings.Join(sessions, "\n"), nil
		case "list-panes":
			return tmuxtest.Listing(rows...), nil
		}
		return "", nil
	}}
	scanner := &panescan.Scanner{Run: func(name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "-eo" {
			return strings.Join(ps, "\n"), nil
		}
		return "", nil
	}}

	tr := tracker.New(tracker.WithIsPidAlive(func(int) bool { return true }))
	s := New(&state.Builder{Tmux: tm, Git: gitinfo.NewCache(), Order: sessionorder.Load("")}, tr, nil, nil, nil)
	threadBySession := map[string]string{}
	for _, tc := range cases {
		threadBySession[tc.session] = tc.threadID
	}
	for session, presences := range scanner.Scan(tm.ListAllPanes()) {
		for i := range presences {
			presences[i].ThreadID = threadBySession[session]
		}
		tr.ApplyPanePresence(session, presences)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /state = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "iaisonWorker") {
		t.Errorf("state JSON serves the worker option: %s", rec.Body.String())
	}
	var body struct {
		Sessions []struct {
			Name       string
			AgentState map[string]any
			Agents     []map[string]any
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]struct {
		agentState map[string]any
		agents     []map[string]any
	}{}
	for _, sess := range body.Sessions {
		got[sess.Name] = struct {
			agentState map[string]any
			agents     []map[string]any
		}{sess.AgentState, sess.Agents}
	}

	for _, tc := range cases {
		t.Run(tc.session, func(t *testing.T) {
			sess, ok := got[tc.session]
			if !ok {
				t.Fatalf("session missing from state: %+v", got)
			}
			if tc.shell {
				if sess.agentState != nil || len(sess.agents) != 0 {
					t.Fatalf("shell pane has a conversation: %+v", sess)
				}
				return
			}
			if len(sess.agents) != 1 || sess.agentState == nil {
				t.Fatalf("want one conversation, got %+v", sess)
			}
			for where, row := range map[string]map[string]any{"agents[]": sess.agents[0], "agentState": sess.agentState} {
				work, hasWork := row["liaisonWorkId"]
				par, hasParent := row["liaisonParentThreadId"]
				if tc.wantLink {
					if work != "w7" || par != parent {
						t.Errorf("%s link = %v %v, want w7 %s", where, work, par, parent)
					}
					continue
				}
				if hasWork || hasParent {
					t.Errorf("%s has link keys %v %v, want both absent", where, work, par)
				}
			}
		})
	}
}
