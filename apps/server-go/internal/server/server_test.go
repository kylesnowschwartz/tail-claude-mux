package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func TestRootIncludesBuildInfo(t *testing.T) {
	s := &Server{BuildInfo: "dev (commit unknown)"}
	response := httptest.NewRecorder()

	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := response.Body.String(), "tcm server (go) dev (commit unknown)"; got != want {
		t.Fatalf("GET / response = %q, want %q", got, want)
	}
}

func TestSetStatusDisambiguatesByThread(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "one", PaneID: "%1", Status: wire.StatusDone}, false)
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "two", PaneID: "%2", Status: wire.StatusDone}, false)
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) { return "", nil }}
	s := New(&state.Builder{Tmux: tm, Order: sessionorder.Load("")}, tr, nil, nil, nil, nil)
	response := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"session":"work","thread":"two","text":"selected"}`)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/set-status", body))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

// A focus hook for an @tcm-ignore'd session (transient popup like a
// revdiff review) must not steal the dashboard focus; a normal session
// still follows focus.
func TestHandleFocus_IgnoredSessionKeepsFocus(t *testing.T) {
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		if args[0] == "list-sessions" {
			return "$1\tproj\t100\t1\t1\t/p\t0\n" +
				"$2\tother\t150\t0\t1\t/p\t0\n" +
				"$3\trevdiff-42\t200\t1\t1\t/p\t1", nil
		}
		return "", nil
	}}
	s := New(&state.Builder{Tmux: tm, Git: gitinfo.NewCache(), Order: sessionorder.Load("")}, nil, nil, nil, nil, nil)
	s.Builder.SetFocused("proj")

	resp := httptest.NewRecorder()
	s.handleFocus(resp, httptest.NewRequest(http.MethodPost, "/focus", strings.NewReader("/dev/ttys001|revdiff-42|@5")))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got := s.Builder.Focused(); got != "proj" {
		t.Errorf("focused = %q, want proj (ignored session must not steal focus)", got)
	}

	s.handleFocus(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/focus", strings.NewReader("/dev/ttys001|other|@6")))
	if got := s.Builder.Focused(); got != "other" {
		t.Errorf("focused = %q, want other (normal session must follow focus)", got)
	}
}

// The tmux pane-focus-in hook POSTs the focused pane id here; the route
// must exist (a missing route falls through to handleRoot and the focus
// signal silently dies — the original Go-port regression).
func TestPaneFocusRouteExists(t *testing.T) {
	s := &Server{clients: map[*client]bool{}}

	for _, body := range []string{"%12", ""} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/pane-focus", strings.NewReader(body)))

		if response.Code != http.StatusOK {
			t.Fatalf("POST /pane-focus body=%q status = %d, want %d", body, response.Code, http.StatusOK)
		}
		if strings.Contains(response.Body.String(), "tcm server") {
			t.Fatalf("POST /pane-focus body=%q fell through to handleRoot", body)
		}
	}
}
