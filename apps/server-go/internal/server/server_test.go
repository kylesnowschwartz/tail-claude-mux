package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootIncludesBuildInfo(t *testing.T) {
	s := &Server{BuildInfo: "dev (commit unknown)"}
	response := httptest.NewRecorder()

	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got, want := response.Body.String(), "tcm server (go) dev (commit unknown)"; got != want {
		t.Fatalf("GET / response = %q, want %q", got, want)
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
