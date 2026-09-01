package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
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

	consistent := httptest.NewRecorder()
	s.Handler().ServeHTTP(consistent, httptest.NewRequest(http.MethodGet, "/wait?session=work&thread=two&pane=%252", nil))
	if consistent.Code != http.StatusOK {
		t.Fatalf("consistent selectors status = %d: %s", consistent.Code, consistent.Body.String())
	}

	mismatched := httptest.NewRecorder()
	s.Handler().ServeHTTP(mismatched, httptest.NewRequest(http.MethodGet, "/wait?session=work&thread=one&pane=%252", nil))
	if mismatched.Code != http.StatusBadRequest || !strings.Contains(mismatched.Body.String(), "pane and thread identify different agents") {
		t.Fatalf("mismatched selectors response = %d %q", mismatched.Code, mismatched.Body.String())
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
	want := "{\"session\":\"work\",\"status\":\"done\",\"terminal\":true,\"timedOut\":false,\"elapsedSeconds\":0}\n"
	if legacy.Body.String() != want {
		t.Fatalf("legacy body = %q, want %q", legacy.Body.String(), want)
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

func TestWaitDelegationPicksUpMidWaitBind(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delegationID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.CreateWithID(delegationID, delegation.CreateParams{
		Agent: "codex", SessionName: "work", PaneID: "%7",
	}); err != nil {
		t.Fatal(err)
	}

	tr := tracker.New()
	s := &Server{Tracker: tr, Delegations: store, waitPollInterval: 5 * time.Millisecond}
	type result struct {
		code int
		body waitResponse
	}
	done := make(chan result, 1)
	go func() {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/wait?delegation="+delegationID+"&timeout=1", nil))
		var body waitResponse
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		done <- result{code: response.Code, body: body}
	}()

	select {
	case got := <-done:
		t.Fatalf("unbound wait returned before binding: status=%d body=%+v", got.code, got.body)
	case <-time.After(25 * time.Millisecond):
	}

	threadID := "44444444-4444-4444-8444-444444444444"
	if _, err := store.Bind(delegationID, "codex", threadID); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: threadID, Status: wire.StatusDone}, false)
	s.mu.Unlock()

	select {
	case got := <-done:
		if got.code != http.StatusOK || got.body.Status != wire.StatusDone || !got.body.Terminal || got.body.TimedOut {
			t.Fatalf("bound wait response: status=%d body=%+v", got.code, got.body)
		}
	case <-time.After(time.Second):
		t.Fatal("delegation wait did not pick up binding")
	}
}

func TestWaitDelegationReturnsEachMessageOnce(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex", SessionName: "work", PaneID: "%7"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AppendMessage(record.DelegationID, delegation.Message{From: "delegate", Kind: "note", Text: "review complete"})
	if err != nil {
		t.Fatal(err)
	}
	threadID := "44444444-4444-4444-8444-444444444444"
	if _, err := store.Bind(record.DelegationID, "codex", threadID); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Agent: "codex", Session: "work", ThreadID: threadID, PaneID: "%7", Status: wire.StatusDone}, false)
	s := &Server{Tracker: tr, Delegations: store}

	first := httptest.NewRecorder()
	s.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/wait?delegation="+record.DelegationID, nil))
	var firstBody waitResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusOK || firstBody.Status != messageWaitStatus || firstBody.Terminal || firstBody.TimedOut ||
		len(firstBody.Messages) != 1 || firstBody.Messages[0].ID != message.ID || firstBody.Messages[0].DeliveredAt == nil {
		t.Fatalf("first response = %d %+v", first.Code, firstBody)
	}

	second := httptest.NewRecorder()
	s.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/wait?delegation="+record.DelegationID, nil))
	var secondBody waitResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if second.Code != http.StatusOK || secondBody.Status != wire.StatusDone || len(secondBody.Messages) != 0 {
		t.Fatalf("second response = %d %+v", second.Code, secondBody)
	}
}

func TestWaitBoundDelegationFallsBackToDisk(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "55555555-5555-4555-8555-555555555555"
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+threadID+".jsonl")
	fixture := `{"type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/project"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"durable answer"}]}}` + "\n"
	if err := os.WriteFile(rolloutPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delegationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, err := store.CreateWithID(delegationID, delegation.CreateParams{
		Agent: "codex", Cwd: "/project", SessionName: "work", PaneID: "%8",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(delegationID, "codex", threadID); err != nil {
		t.Fatal(err)
	}

	s := &Server{Tracker: tracker.New(), Delegations: store, CodexWatcher: codexwatch.New(sessionsDir, "")}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/wait?delegation="+delegationID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body waitResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Session != "work" || body.Status != wire.StatusDone || !body.Terminal || body.TimedOut {
		t.Fatalf("response = %+v", body)
	}
}

func TestWaitDelegationAddressErrors(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delegationID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if _, err := store.CreateWithID(delegationID, delegation.CreateParams{
		Agent: "codex", SessionName: "work", PaneID: "%7",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(delegationID, "codex", "thread-one"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Tracker: tracker.New(), Delegations: store}
	tests := []struct {
		name string
		path string
		code int
		text string
	}{
		{name: "unknown", path: "/wait?delegation=dddddddd-dddd-4ddd-8ddd-dddddddddddd", code: http.StatusNotFound, text: "delegation not found"},
		{name: "session conflict", path: "/wait?delegation=" + delegationID + "&session=other", code: http.StatusBadRequest, text: "delegation and session identify different agents"},
		{name: "pane conflict", path: "/wait?delegation=" + delegationID + "&pane=%258", code: http.StatusBadRequest, text: "delegation and pane identify different agents"},
		{name: "thread conflict", path: "/wait?delegation=" + delegationID + "&thread=thread-two", code: http.StatusBadRequest, text: "delegation and thread identify different agents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != tt.code || !strings.Contains(response.Body.String(), tt.text) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestResolveDelegationWaitTreatsNilTrackedStateAsMiss(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Delegations: store}
	state, resolveErr := s.resolveDelegationWait(record.DelegationID, "", "", "")
	if resolveErr != nil || state.status != "" {
		t.Fatalf("state = %+v, error = %+v", state, resolveErr)
	}
}
