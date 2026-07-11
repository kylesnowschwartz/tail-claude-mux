package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func TestWaitDisambiguatesMultiAgentSession(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "one", PaneID: "%1", Status: wire.StatusDone}, false)
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "two", PaneID: "%2", Status: wire.StatusWaiting}, false)
	s := &Server{Tracker: tr}

	ambiguous := httptest.NewRecorder()
	s.Handler().ServeHTTP(ambiguous, httptest.NewRequest(http.MethodGet, "/wait?session=work", nil))
	if ambiguous.Code != http.StatusBadRequest || !strings.Contains(ambiguous.Body.String(), "session has 2 agents; specify pane") {
		t.Fatalf("ambiguous response = %d %q", ambiguous.Code, ambiguous.Body.String())
	}

	selected := httptest.NewRecorder()
	s.Handler().ServeHTTP(selected, httptest.NewRequest(http.MethodGet, "/wait?session=work&pane=%252", nil))
	if selected.Code != http.StatusOK {
		t.Fatalf("selected status = %d: %s", selected.Code, selected.Body.String())
	}
	var body waitResponse
	if err := json.Unmarshal(selected.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != wire.StatusWaiting {
		t.Fatalf("selected response = %+v", body)
	}
}

func TestWaitSingleInstanceKeepsLegacyResponse(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "one", PaneID: "%1", Status: wire.StatusDone}, false)
	s := &Server{Tracker: tr}

	legacy := httptest.NewRecorder()
	s.Handler().ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/wait?session=work", nil))
	disambiguated := httptest.NewRecorder()
	s.Handler().ServeHTTP(disambiguated, httptest.NewRequest(http.MethodGet, "/wait?session=work&pane=%251", nil))
	if legacy.Code != http.StatusOK || disambiguated.Code != http.StatusOK {
		t.Fatalf("status codes = legacy %d, pane %d", legacy.Code, disambiguated.Code)
	}
	if legacy.Body.String() != disambiguated.Body.String() {
		t.Fatalf("legacy body = %q, pane body = %q", legacy.Body.String(), disambiguated.Body.String())
	}
}

func TestWaitImmediateAndErrors(t *testing.T) {
	tests := []struct {
		name, path, status, wantStatus string
		wantCode                       int
	}{
		{name: "terminal", path: "/wait?session=work", status: wire.StatusDone, wantCode: http.StatusOK, wantStatus: wire.StatusDone},
		{name: "unknown", path: "/wait?session=missing", wantCode: http.StatusNotFound},
		{name: "missing session", path: "/wait", wantCode: http.StatusBadRequest},
		{name: "invalid timeout", path: "/wait?session=work&timeout=nope", status: wire.StatusIdle, wantCode: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := tracker.New()
			if tt.status != "" {
				tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "thread", Status: tt.status}, false)
			}
			s := &Server{Tracker: tr}
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d", response.Code, tt.wantCode)
			}
			if tt.wantStatus != "" {
				var body waitResponse
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Status != tt.wantStatus || !body.Terminal || body.TimedOut {
					t.Fatalf("response = %+v", body)
				}
			}
		})
	}
}

func TestWaitWakeOnTransition(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "thread", Status: wire.StatusIdle}, false)
	s := &Server{Tracker: tr, waitPollInterval: 5 * time.Millisecond}
	done := make(chan waitResponse, 1)
	go func() {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/wait?session=work&timeout=1", nil))
		var body waitResponse
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		done <- body
	}()
	time.Sleep(15 * time.Millisecond)
	s.mu.Lock()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "thread", Status: wire.StatusWaiting}, false)
	s.mu.Unlock()
	select {
	case body := <-done:
		if body.Status != wire.StatusWaiting || body.Terminal || body.TimedOut {
			t.Fatalf("response = %+v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not wake")
	}
}

func TestWaitIdleTimesOut(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: "thread", Status: wire.StatusIdle}, false)
	s := &Server{Tracker: tr, waitPollInterval: 5 * time.Millisecond}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/wait?session=work&timeout=0.01", nil))
	var body waitResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != wire.StatusIdle || body.Terminal || !body.TimedOut {
		t.Fatalf("response = %+v", body)
	}
}
