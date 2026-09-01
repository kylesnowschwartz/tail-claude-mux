package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux/tmuxtest"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func TestFollowupRefusals(t *testing.T) {
	tests := []struct {
		name, session, agent, status, wantError string
		wantCode                                int
	}{
		{name: "unknown", session: "missing", wantCode: http.StatusNotFound},
		{name: "running", session: "work", agent: "codex", status: wire.StatusRunning, wantCode: http.StatusConflict, wantError: "session is running; refusing to interrupt it"},
		{name: "waiting", session: "work", agent: "codex", status: wire.StatusWaiting, wantCode: http.StatusConflict, wantError: "session is waiting for input; refusing to interrupt it"},
		{name: "non codex", session: "work", agent: "claude-code", status: wire.StatusIdle, wantCode: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := tracker.New()
			if tt.agent != "" {
				tr.ApplyEvent(wire.AgentEvent{Session: tt.session, Agent: tt.agent, ThreadID: "thread", Status: tt.status, PaneID: "%1"}, false)
			}
			s := &Server{Tracker: tr}
			response := httptest.NewRecorder()
			body := fmt.Sprintf(`{"session":%q,"message":"more"}`, tt.session)
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantCode, response.Body.String())
			}
			if tt.wantError != "" {
				var body map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["error"] != tt.wantError {
					t.Fatalf("error = %q, want %q", body["error"], tt.wantError)
				}
			}
		})
	}
}

func TestFollowupSelectorMismatchIsJSON(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "one", PaneID: "%1", Status: wire.StatusDone}, false)
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "two", PaneID: "%2", Status: wire.StatusDone}, false)
	s := &Server{Tracker: tr}
	response := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"session":"work","message":"more","thread":"one","pane":"%2"}`)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", body))
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d, content-type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] != "pane and thread identify different agents" {
		t.Fatalf("error = %q", result["error"])
	}
}

func TestFollowupPinsThreadAndRespawnsPane(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uuid := "22222222-2222-2222-2222-222222222222"
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+uuid+".jsonl")
	if err := os.WriteFile(rolloutPath, []byte(`{"type":"session_meta","payload":{"cwd":"/project"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var respawnArgs []string
	run := func(args ...string) (string, error) {
		if len(args) > 0 && args[0] == "list-panes" {
			return tmuxtest.PaneSpec{Session: "work", ID: "%7", PID: "123", Dir: "/project"}.Row(), nil
		}
		respawnArgs = append([]string(nil), args...)
		return "", nil
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: uuid, Status: wire.StatusDone, PaneID: "%7"}, false)
	tm := &tmux.Tmux{Run: run}
	s := &Server{Tracker: tr, Builder: &state.Builder{Tmux: tm}, CodexWatcher: codexwatch.New(sessionsDir, "")}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(`{"session":"work","message":"please continue"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body followupResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(body.MessageFile)
	if body.UUID != uuid || body.RolloutPath != rolloutPath || body.PaneID != "%7" || body.Mode != "resume" {
		t.Fatalf("response = %+v", body)
	}
	message, err := os.ReadFile(body.MessageFile)
	if err != nil || string(message) != "please continue" {
		t.Fatalf("message file = %q, err = %v", message, err)
	}
	wantPrefix := []string{"respawn-pane", "-k", "-t", "%7"}
	if len(respawnArgs) != 5 || !slices.Equal(respawnArgs[:4], wantPrefix) {
		t.Fatalf("respawn args = %#v", respawnArgs)
	}
	wantCommand := fmt.Sprintf(`codex --profile tcm-delegate -c mcp_servers.just.enabled=false resume %s "Read %s and address it"`, uuid, body.MessageFile)
	if respawnArgs[4] != wantCommand {
		t.Fatalf("command = %q, want %q", respawnArgs[4], wantCommand)
	}
}

func TestFollowupAnswersOneUnansweredAskBeforeRunningRefusal(t *testing.T) {
	threadID := "33333333-3333-3333-3333-333333333333"
	store, delegationID := createFollowupDelegation(t, threadID)
	ask, err := store.AppendMessage(delegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered(delegationID, []string{ask.ID}); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusRunning}, false)
	respawned := false
	s := &Server{
		Tracker: tr, Delegations: store,
		waitPollInterval: 5 * time.Millisecond,
		Builder: &state.Builder{Tmux: &tmux.Tmux{Run: func(args ...string) (string, error) {
			respawned = args[0] == "respawn-pane"
			return "", nil
		}}},
	}
	answerDone := make(chan delegationAnswerResponse, 1)
	go func() {
		answered := httptest.NewRecorder()
		path := "/delegation-answer?delegation=" + delegationID + "&ask=" + ask.ID + "&timeout=1"
		s.Handler().ServeHTTP(answered, httptest.NewRequest(http.MethodGet, path, nil))
		var answerBody delegationAnswerResponse
		_ = json.Unmarshal(answered.Body.Bytes(), &answerBody)
		answerDone <- answerBody
	}()
	waitForAnswerWaiter(t, s, delegationID, ask.ID)

	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"yes"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusOK || respawned {
		t.Fatalf("status = %d, respawned = %v: %s", response.Code, respawned, response.Body.String())
	}
	var result followupResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Mode != "answer" || result.AskID != ask.ID || result.MessageFile != "" || result.RolloutPath != "" {
		t.Fatalf("response = %+v", result)
	}

	answerBody := <-answerDone
	if !answerBody.Answered || answerBody.Message == nil || answerBody.Message.Text != "yes" {
		t.Fatalf("answer response = %#v", answerBody)
	}

	double := httptest.NewRecorder()
	doubleBody := fmt.Sprintf(`{"delegation":%q,"from":"orchestrator","kind":"answer","text":"again","replyTo":%q}`, delegationID, ask.ID)
	s.Handler().ServeHTTP(double, httptest.NewRequest(http.MethodPost, "/delegation-message", bytes.NewBufferString(doubleBody)))
	if double.Code != http.StatusConflict || !strings.Contains(double.Body.String(), "ask is already answered") {
		t.Fatalf("double answer = %d %q", double.Code, double.Body.String())
	}
}

func TestFollowupDoesNotAnswerUndeliveredAsk(t *testing.T) {
	threadID := "33333333-3333-3333-3333-333333333333"
	store, delegationID := createFollowupDelegation(t, threadID)
	ask, err := store.AppendMessage(delegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store, waitPollInterval: 5 * time.Millisecond}
	answerDone := make(chan struct{}, 1)
	go func() {
		response := httptest.NewRecorder()
		path := "/delegation-answer?delegation=" + delegationID + "&ask=" + ask.ID + "&timeout=1"
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		answerDone <- struct{}{}
	}()
	waitForAnswerWaiter(t, s, delegationID, ask.ID)

	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"yes"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session is running; refusing to interrupt it") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if _, answered, err := store.AnswerFor(delegationID, ask.ID); err != nil || answered {
		t.Fatalf("AnswerFor() = (_, %v, %v), want unanswered", answered, err)
	}
	if _, err := store.AppendMessage(delegationID, delegation.Message{From: "orchestrator", Kind: "answer", Text: "cleanup", ReplyTo: ask.ID}); err != nil {
		t.Fatal(err)
	}
	<-answerDone
}

func TestFollowupAskRoutingOffDoesNotAnswer(t *testing.T) {
	threadID := "33333333-3333-3333-3333-333333333333"
	store, delegationID := createFollowupDelegation(t, threadID)
	ask, err := store.AppendMessage(delegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered(delegationID, []string{ask.ID}); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store}
	defer s.registerAnswerWaiter(delegationID, ask.ID)()

	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"retry","askRouting":"off"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session is running; refusing to interrupt it") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if _, answered, err := store.AnswerFor(delegationID, ask.ID); err != nil || answered {
		t.Fatalf("AnswerFor() = (_, %v, %v), want unanswered", answered, err)
	}
}

func TestFollowupRejectsAmbiguousUnansweredAsks(t *testing.T) {
	threadID := "33333333-3333-3333-3333-333333333333"
	store, delegationID := createFollowupDelegation(t, threadID)
	var asks []delegation.Message
	for _, text := range []string{"first?", "second?"} {
		ask, err := store.AppendMessage(delegationID, delegation.Message{From: "delegate", Kind: "ask", Text: text})
		if err != nil {
			t.Fatal(err)
		}
		asks = append(asks, ask)
	}
	if err := store.MarkDelivered(delegationID, []string{asks[0].ID, asks[1].ID}); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store}
	for _, ask := range asks {
		defer s.registerAnswerWaiter(delegationID, ask.ID)()
	}
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"yes"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["code"] != "ambiguous-ask" || result["error"] != "multiple unanswered asks" {
		t.Fatalf("response = %#v", result)
	}
}

func TestFollowupWithNoAskKeepsRunningRefusal(t *testing.T) {
	threadID := "33333333-3333-3333-3333-333333333333"
	store, delegationID := createFollowupDelegation(t, threadID)
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store}
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"continue"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session is running; refusing to interrupt it") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestFollowupSessionPaneDoesNotRoutePersistedAsk(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex", SessionName: "work", PaneID: "%7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(record.DelegationID, "codex", "old-thread"); err != nil {
		t.Fatal(err)
	}
	ask, err := store.AppendMessage(record.DelegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "old question?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered(record.DelegationID, []string{ask.ID}); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "current-thread", PaneID: "%7", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store}
	defer s.registerAnswerWaiter(record.DelegationID, ask.ID)()

	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(`{"session":"work","pane":"%7","message":"continue"}`)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session is running; refusing to interrupt it") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if _, answered, err := store.AnswerFor(record.DelegationID, ask.ID); err != nil || answered {
		t.Fatalf("AnswerFor() = (_, %v, %v), want unanswered", answered, err)
	}
}

func TestFollowupRevalidatesBeforeRespawn(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uuid := "22222222-2222-2222-2222-222222222222"
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+uuid+".jsonl")
	if err := os.WriteFile(rolloutPath, []byte(`{"type":"session_meta","payload":{"cwd":"/project"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: uuid, Status: wire.StatusDone, PaneID: "%7"}, false)
	listCalls := 0
	respawned := false
	var s *Server
	run := func(args ...string) (string, error) {
		if args[0] == "list-panes" {
			listCalls++
			if listCalls == 1 {
				s.mu.Lock()
				tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: uuid, Status: wire.StatusRunning, PaneID: "%7", TS: 1}, false)
				s.mu.Unlock()
			}
			return tmuxtest.PaneSpec{Session: "work", ID: "%7", PID: "123", Dir: "/project"}.Row(), nil
		}
		respawned = true
		return "", nil
	}
	tm := &tmux.Tmux{Run: run}
	s = &Server{Tracker: tr, Builder: &state.Builder{Tmux: tm}, CodexWatcher: codexwatch.New(sessionsDir, "")}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(`{"session":"work","message":"please continue"}`)))
	if response.Code != http.StatusConflict || respawned {
		t.Fatalf("status = %d, respawned = %v: %s", response.Code, respawned, response.Body.String())
	}
}

func TestFollowupBoundDelegationUsesDiskDoneStatus(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "33333333-3333-3333-3333-333333333333"
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+threadID+".jsonl")
	fixture := `{"type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/project"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"done"}]}}` + "\n"
	if err := os.WriteFile(rolloutPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	store, delegationID := createFollowupDelegation(t, threadID)
	staleAsk, err := store.AppendMessage(delegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered(delegationID, []string{staleAsk.ID}); err != nil {
		t.Fatal(err)
	}
	var respawned bool
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		if args[0] == "list-panes" {
			return tmuxtest.PaneSpec{Session: "delegate", ID: "%42", PID: "123", Dir: "/project"}.Row(), nil
		}
		respawned = args[0] == "respawn-pane" && args[3] == "%42"
		return "", nil
	}}
	s := &Server{
		Tracker: tracker.New(), Delegations: store,
		Builder: &state.Builder{Tmux: tm}, CodexWatcher: codexwatch.New(sessionsDir, ""),
	}
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"continue"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusOK || !respawned {
		t.Fatalf("status = %d, respawned = %v: %s", response.Code, respawned, response.Body.String())
	}
	var result followupResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(result.MessageFile)
	if result.UUID != threadID || result.RolloutPath != rolloutPath || result.PaneID != "%42" {
		t.Fatalf("response = %+v", result)
	}
}

func waitForAnswerWaiter(t *testing.T, s *Server, delegationID, askID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.hasAnswerWaiter(delegationID, askID) {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("answer waiter was not registered")
}

func TestFollowupBoundDelegationWithoutTrackerOrDiskRefuses(t *testing.T) {
	store, delegationID := createFollowupDelegation(t, "33333333-3333-3333-3333-333333333333")
	respawned := false
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		if args[0] == "respawn-pane" {
			respawned = true
		}
		return tmuxtest.PaneSpec{Session: "delegate", ID: "%42", PID: "123", Dir: "/project"}.Row(), nil
	}}
	s := &Server{Tracker: tracker.New(), Delegations: store, Builder: &state.Builder{Tmux: tm}}
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"continue"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))

	if response.Code != http.StatusConflict || respawned {
		t.Fatalf("status = %d, respawned = %v: %s", response.Code, respawned, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] != "cannot verify delegate is idle; refusing to interrupt" {
		t.Fatalf("error = %q", result["error"])
	}
}

func TestFollowupBoundDelegationRejectsDeadPane(t *testing.T) {
	threadID := "33333333-3333-3333-3333-333333333333"
	store, delegationID := createFollowupDelegation(t, threadID)
	staleAsk, err := store.AppendMessage(delegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered(delegationID, []string{staleAsk.ID}); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusDone}, false)
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) { return "", nil }}
	s := &Server{Tracker: tr, Delegations: store, Builder: &state.Builder{Tmux: tm}}
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"continue"}`, delegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] != "delegate pane %42 is gone; cannot deliver follow-up" {
		t.Fatalf("error = %q", result["error"])
	}
}

func TestFollowupUnboundDelegationUsesSessionPaneResolution(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "44444444-4444-4444-8444-444444444444"
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+threadID+".jsonl"), []byte(`{"type":"session_meta","payload":{"cwd":"/project"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex", Cwd: "/project", SessionName: "delegate", PaneID: "%42"})
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: threadID, PaneID: "%42", Status: wire.StatusDone}, false)
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		if args[0] == "list-panes" {
			return tmuxtest.PaneSpec{Session: "delegate", ID: "%42", PID: "123", Dir: "/project"}.Row(), nil
		}
		return "", nil
	}}
	s := &Server{Tracker: tr, Delegations: store, Builder: &state.Builder{Tmux: tm}, CodexWatcher: codexwatch.New(sessionsDir, "")}
	response := httptest.NewRecorder()
	body := fmt.Sprintf(`{"delegation":%q,"message":"continue"}`, record.DelegationID)
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var result followupResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(result.MessageFile)
	if result.UUID != threadID || result.PaneID != "%42" {
		t.Fatalf("response = %+v", result)
	}
}

func TestResolveFollowupTargetTreatsNilTrackedStateAsMiss(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Delegations: store}
	target, resolveErr := s.resolveFollowupTarget(followupRequest{Delegation: record.DelegationID})
	if resolveErr != nil || target.State != nil {
		t.Fatalf("target = %+v, error = %+v", target, resolveErr)
	}
}

func TestFollowupDelegationResolversRejectUnboundThreadMismatch(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex", SessionName: "delegate", PaneID: "%42"})
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "delegate", Agent: "codex", ThreadID: "actual-thread", PaneID: "%42", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store}
	req := followupRequest{Delegation: record.DelegationID, Thread: "other-thread"}

	_, delegationErr := s.resolveFollowupDelegation(req)
	_, targetErr := s.resolveFollowupTarget(req)
	for name, resolveErr := range map[string]*agentResolutionError{
		"delegation resolver": delegationErr,
		"target resolver":     targetErr,
	} {
		if resolveErr == nil || resolveErr.status != http.StatusBadRequest || resolveErr.message != "delegation and thread identify different agents" {
			t.Fatalf("%s error = %#v", name, resolveErr)
		}
	}
}

func TestFollowupDelegationAddressErrors(t *testing.T) {
	store, delegationID := createFollowupDelegation(t, "33333333-3333-3333-3333-333333333333")
	tests := []struct {
		name, body, wantError string
		wantCode              int
	}{
		{name: "unknown", body: `{"delegation":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","message":"continue"}`, wantCode: http.StatusNotFound, wantError: "delegation not found"},
		{name: "session conflict", body: fmt.Sprintf(`{"delegation":%q,"session":"other","message":"continue"}`, delegationID), wantCode: http.StatusBadRequest, wantError: "delegation and session identify different agents"},
		{name: "pane conflict", body: fmt.Sprintf(`{"delegation":%q,"pane":"%%7","message":"continue"}`, delegationID), wantCode: http.StatusBadRequest, wantError: "delegation and pane identify different agents"},
		{name: "thread conflict", body: fmt.Sprintf(`{"delegation":%q,"thread":"other","message":"continue"}`, delegationID), wantCode: http.StatusBadRequest, wantError: "delegation and thread identify different agents"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{Tracker: tracker.New(), Delegations: store}
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/followup", bytes.NewBufferString(tt.body)))
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantCode, response.Body.String())
			}
			var result map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["error"] != tt.wantError {
				t.Fatalf("error = %q, want %q", result["error"], tt.wantError)
			}
		})
	}
}

func createFollowupDelegation(t *testing.T, threadID string) (*delegation.Store, string) {
	t.Helper()
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex", Cwd: "/project", SessionName: "delegate", PaneID: "%42"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(record.DelegationID, "codex", threadID); err != nil {
		t.Fatal(err)
	}
	return store, record.DelegationID
}

func TestCleanupOldFollowupMessages(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	oldPath := filepath.Join(dir, "tcm-followup-old.md")
	recentPath := filepath.Join(dir, "tcm-followup-recent.md")
	otherPath := filepath.Join(dir, "other-old.md")
	for _, path := range []string{oldPath, recentPath, otherPath} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-25 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(otherPath, old, old); err != nil {
		t.Fatal(err)
	}

	cleanupOldFollowupMessages(dir, now)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old follow-up still exists: %v", err)
	}
	for _, path := range []string{recentPath, otherPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved file %q: %v", path, err)
		}
	}
}
