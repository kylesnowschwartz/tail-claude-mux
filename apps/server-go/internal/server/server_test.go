package server

import (
	"net/http"
	"net/http/httptest"
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
