package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexwatch"
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
	sessionsDir := t.TempDir()
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
	if body.UUID != uuid || body.RolloutPath != rolloutPath || body.PaneID != "%7" {
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
	wantCommand := fmt.Sprintf(`codex -c mcp_servers.just.enabled=false resume %s "Read %s and address it"`, uuid, body.MessageFile)
	if respawnArgs[4] != wantCommand {
		t.Fatalf("command = %q, want %q", respawnArgs[4], wantCommand)
	}
}

func TestFollowupRevalidatesBeforeRespawn(t *testing.T) {
	sessionsDir := t.TempDir()
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
