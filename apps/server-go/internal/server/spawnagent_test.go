package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
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
		{name: "missing owner session", body: `{"dir":"/tmp","agent":"codex","prompt":"task"}`, wantStatus: http.StatusBadRequest, wantError: "ownerSession is required"},
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

func TestHandleSpawnAgentCreatesDelegation(t *testing.T) {
	dir := t.TempDir()
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var spawnArgs []string
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "$1\towner\t0\t0\t1\t/tmp\t0", nil
		case "list-windows":
			return "shell", nil
		default:
			spawnArgs = append([]string(nil), args...)
			return "owner %42 @7", nil
		}
	}}
	s := &Server{Builder: &state.Builder{Tmux: tm}, Delegations: store, Port: 7392}
	body := `{"dir":` + mustMarshalString(t, dir) + `,"agent":"codex","prompt":"task","ownerSession":"owner"}`
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/spawn-agent", bytes.NewBufferString(body)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var result tmux.SpawnAgentResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.DelegationID == "" {
		t.Fatal("delegationId is empty")
	}
	if !slices.Contains(spawnArgs, "TCM_DELEGATION_ID="+result.DelegationID) {
		t.Errorf("spawn args = %#v, want delegation environment", spawnArgs)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spawnArgs, "TCM_BIN="+executable) || !slices.Contains(spawnArgs, "TCM_PORT="+strconv.Itoa(s.Port)) {
		t.Errorf("spawn args = %#v, want binary and port environment", spawnArgs)
	}
	command := spawnArgs[len(spawnArgs)-1]
	if !strings.Contains(command, "[TCM delegate context]") || !strings.Contains(command, result.DelegationID) || !strings.Contains(command, "task") {
		t.Errorf("spawn command = %q, want delegation context and task", command)
	}
	record, err := store.Get(result.DelegationID)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", result.DelegationID, err)
	}
	if record.Agent != "codex" || record.Cwd != dir || record.OwnerSession != "owner" ||
		record.SessionName != "owner" || record.PaneID != "%42" || record.WindowID != "@7" {
		t.Errorf("record = %#v", record)
	}
}

func TestPrependDelegateContext(t *testing.T) {
	delegationID := "123e4567-e89b-42d3-a456-426614174000"
	want := `[TCM delegate context] You are a delegate agent. An orchestrator in tmux session "owner" spawned you and is watching this delegation (ID 123e4567-e89b-42d3-a456-426614174000). You can message it at any time:
  "$TCM_BIN" tell -message "<text>"   — deliver a note or result upstream (fire-and-forget)
  "$TCM_BIN" ask -message "<question>" — ask and wait for the orchestrator's answer
Both read TCM_DELEGATION_ID and TCM_PORT from your environment. Multiline text: use -message-file <path>. Never claim you have no upstream. Your task brief follows.
---
task`
	if got := prependDelegateContext("task", "owner", delegationID); got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestHandleSpawnAgentFailureCreatesNoDelegation(t *testing.T) {
	dir := t.TempDir()
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "$1\towner\t0\t0\t1\t/tmp\t0", nil
		case "list-windows":
			return "", nil
		default:
			return "", errors.New("spawn failed")
		}
	}}
	s := &Server{Builder: &state.Builder{Tmux: tm}, Delegations: store}
	body := `{"dir":` + mustMarshalString(t, dir) + `,"agent":"codex","prompt":"task","ownerSession":"owner"}`
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/spawn-agent", bytes.NewBufferString(body)))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("records = %#v, want none", records)
	}
}

func TestHandleSpawnAgentStoreFailureReturnsSpawnAddress(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := delegation.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(storeDir); err != nil {
		t.Fatal(err)
	}
	tm := &tmux.Tmux{Run: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "$1\towner\t0\t0\t1\t/tmp\t0", nil
		case "list-windows":
			return "shell", nil
		default:
			return "owner %42 @7", nil
		}
	}}
	s := &Server{Builder: &state.Builder{Tmux: tm}, Delegations: store}
	body := `{"dir":` + mustMarshalString(t, dir) + `,"agent":"codex","prompt":"task","ownerSession":"owner"}`
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/spawn-agent", bytes.NewBufferString(body)))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var result tmux.SpawnAgentResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionName != "owner" || result.PaneID != "%42" || result.WindowID != "@7" || result.DelegationID != "" {
		t.Fatalf("response = %#v", result)
	}
}

func mustMarshalString(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
