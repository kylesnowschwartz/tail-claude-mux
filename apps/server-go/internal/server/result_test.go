package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// resolvedTempDir returns a canonical (symlink-resolved) temp dir so it matches
// what codexwatch.New produces after normalizing its SessionsDir. On macOS
// t.TempDir() sits under /var, a symlink to /private/var; production rollout
// paths arrive already resolved, so a SessionsDir passed to codexwatch.New must
// start from the resolved form or the rollout paths it returns won't equal the
// ones the test wrote.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestResultReturnsBoundDelegationFromDiskWithoutTracker(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "44444444-4444-4444-4444-444444444444"
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
	delegationID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.CreateWithID(delegationID, delegation.CreateParams{
		Agent: "codex", Cwd: "/project", SessionName: "work", PaneID: "%7",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(delegationID, "codex", threadID); err != nil {
		t.Fatal(err)
	}

	s := &Server{Tracker: tracker.New(), Delegations: store, CodexWatcher: codexwatch.New(sessionsDir, "")}
	for _, path := range []string{"/result?delegation=" + delegationID, "/result?thread=" + threadID} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var body resultResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ThreadID != threadID || body.RolloutPath != rolloutPath || body.Status != wire.StatusDone {
				t.Fatalf("response = %+v", body)
			}
			if !body.HasFinal || body.FinalMessage != "durable answer" {
				t.Fatalf("final response = %+v", body)
			}
		})
	}
}

func TestResultTrackerStatusWinsOverStaleDiskStatus(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "45454545-4545-4454-8454-454545454545"
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+threadID+".jsonl")
	fixture := `{"type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/project"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"stale final"}]}}` + "\n"
	if err := os.WriteFile(rolloutPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delegationID := "abababab-abab-4bab-8bab-abababababab"
	if _, err := store.CreateWithID(delegationID, delegation.CreateParams{
		Agent: "codex", Cwd: "/project", SessionName: "work", PaneID: "%7",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind(delegationID, "codex", threadID); err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: threadID, PaneID: "%7", Status: wire.StatusRunning}, false)
	s := &Server{Tracker: tr, Delegations: store, CodexWatcher: codexwatch.New(sessionsDir, "")}

	paths := []string{
		"/result?delegation=" + delegationID,
		"/result?session=work&thread=" + threadID,
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var body resultResponse
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != wire.StatusRunning || body.FinalMessage != "stale final" || body.RolloutPath != rolloutPath {
				t.Fatalf("response = %+v", body)
			}
		})
	}
}

func TestResultUnboundDelegationIsNotYetBound(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	delegationID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, err := store.CreateWithID(delegationID, delegation.CreateParams{
		Agent: "codex", SessionName: "work", PaneID: "%7",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{Tracker: tracker.New(), Delegations: store}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?delegation="+delegationID, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "delegation not yet bound" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestResultRejectsConflictingDelegationAndPane(t *testing.T) {
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

	s := &Server{Tracker: tracker.New(), Delegations: store}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?delegation="+delegationID+"&pane=%258", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func TestResultReturnsPinnedRolloutFinalMessage(t *testing.T) {
	sessionsDir := resolvedTempDir(t)
	rolloutDir := filepath.Join(sessionsDir, "2026", "07", "11")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	threadID := "22222222-2222-2222-2222-222222222222"
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-07-11T00-00-00-"+threadID+".jsonl")
	fixture := `{"type":"session_meta","payload":{"id":"` + threadID + `","cwd":"/project"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"delegate answer"}]}}` + "\n"
	if err := os.WriteFile(rolloutPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: threadID, Status: wire.StatusDone}, false)
	s := &Server{Tracker: tr, CodexWatcher: codexwatch.New(sessionsDir, "")}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?session=work", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body resultResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Session != "work" || body.Status != wire.StatusDone || !body.HasFinal || body.FinalMessage != "delegate answer" {
		t.Fatalf("response = %+v", body)
	}
	if body.RolloutPath != rolloutPath || body.ThreadID != threadID || body.Cwd != "/project" {
		t.Fatalf("provenance = %+v", body)
	}
	if body.Identification != "response_item.message.role=assistant.phase=final_answer" {
		t.Fatalf("identification = %q", body.Identification)
	}
}

func TestResultUnknownSessionIsJSON404(t *testing.T) {
	s := &Server{Tracker: tracker.New()}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?session=missing", nil))
	if response.Code != http.StatusNotFound || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d, content-type = %q", response.Code, response.Header().Get("Content-Type"))
	}
}

func TestResultDelegationIncludesMessageLog(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex", SessionName: "work", PaneID: "%1"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AppendMessage(record.DelegationID, delegation.Message{From: "delegate", Kind: "note", Text: "review complete"})
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "thread", PaneID: "%1", Status: wire.StatusDone}, false)
	s := &Server{Tracker: tr, Delegations: store}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?delegation="+record.DelegationID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body resultResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 || body.Messages[0].ID != message.ID {
		t.Fatalf("messages = %#v, want %s", body.Messages, message.ID)
	}
}

func TestResultSelectsPaneInMultiAgentSession(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "one", PaneID: "%1", Status: wire.StatusDone}, false)
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "two", PaneID: "%2", Status: wire.StatusWaiting}, false)
	s := &Server{Tracker: tr}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?session=work&pane=%252", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var body resultResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != wire.StatusWaiting || body.ThreadID != "two" {
		t.Fatalf("response = %+v", body)
	}
}

func TestResultResolutionErrorIsJSON(t *testing.T) {
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "one", PaneID: "%1", Status: wire.StatusDone}, false)
	tr.ApplyEvent(wire.AgentEvent{Session: "work", Agent: "codex", ThreadID: "two", PaneID: "%2", Status: wire.StatusDone}, false)
	s := &Server{Tracker: tr}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/result?session=work&thread=one&pane=%252", nil))
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d, content-type = %q", response.Code, response.Header().Get("Content-Type"))
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "pane and thread identify different agents" {
		t.Fatalf("error = %q", body["error"])
	}
}
