package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
)

func TestHandleSpawnAgent(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		run        tmux.Runner
		wantStatus int
		wantError  string
	}{
		{name: "malformed JSON", body: "{", wantStatus: http.StatusBadRequest, wantError: "request body must be valid JSON"},
		{name: "trailing JSON", body: `{} {}`, wantStatus: http.StatusBadRequest, wantError: "request body must be valid JSON"},
		{name: "validation failure", body: `{}`, wantStatus: http.StatusBadRequest, wantError: "dir is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := tt.run
			if run == nil {
				run = func(args ...string) (string, error) { return "", errors.New("unexpected call") }
			}
			s := &Server{Builder: &state.Builder{Tmux: &tmux.Tmux{Run: run}}}
			req := httptest.NewRequest(http.MethodPost, "/spawn-agent", bytes.NewBufferString(tt.body))
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, req)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tt.wantError {
				t.Errorf("error = %q, want %q", body["error"], tt.wantError)
			}
		})
	}
}
