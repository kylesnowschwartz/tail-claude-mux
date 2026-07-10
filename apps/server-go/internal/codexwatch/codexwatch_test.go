package codexwatch

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

type harness struct {
	adapter *Adapter
	events  []wire.AgentEvent
	mu      sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{adapter: New(t.TempDir(), filepath.Join(t.TempDir(), "index.jsonl"))}
	h.adapter.now = func() int64 { return 1_000_000 }
	h.adapter.ctx = &Context{
		ResolveSession: func(cwd string) string {
			if cwd == "/project" {
				return "cwd-session"
			}
			return ""
		},
		ResolveSessionByPid: func(pid int) string {
			if pid == 200 {
				return "pid-session"
			}
			return ""
		},
		Emit: func(ev wire.AgentEvent) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.events = append(h.events, ev)
		},
		Locked: func(fn func()) { fn() },
	}
	return h
}

func (h *harness) snapshot() []wire.AgentEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.AgentEvent(nil), h.events...)
}

func waitForNameLookup(t *testing.T, h *harness, threadID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if state := h.adapter.threads[threadID]; state != nil && !state.nameLookupInFlight {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("name lookup did not finish for %q", threadID)
}

func writeIndexName(t *testing.T, path, threadID, name string) {
	t.Helper()
	line := "{\"id\":\"" + threadID + "\",\"thread_name\":\"" + name + "\"}\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHookStatusMapAndStrictAgentFilter(t *testing.T) {
	wants := map[string]string{
		"SessionStart": wire.StatusIdle, "UserPromptSubmit": wire.StatusRunning,
		"PreToolUse": wire.StatusRunning, "PostToolUse": wire.StatusRunning,
		"PermissionRequest": wire.StatusWaiting, "Stop": wire.StatusIdle,
	}
	for event, want := range wants {
		t.Run(event, func(t *testing.T) {
			h := newHarness(t)
			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: event, SessionID: "thread", Cwd: "/project"})
			got := h.snapshot()
			if len(got) != 1 || got[0].Status != want || got[0].Agent != "codex" {
				t.Fatalf("events = %#v, want status %s", got, want)
			}
		})
	}
	for _, agent := range []string{"", "claude-code", "pi"} {
		h := newHarness(t)
		h.adapter.HandleHook(wire.HookPayload{Agent: agent, Event: "Stop", SessionID: "thread", Cwd: "/project"})
		if len(h.snapshot()) != 0 {
			t.Fatalf("agent %q was accepted", agent)
		}
	}
	if _, ok := hookStatusMap["Unknown"]; ok {
		t.Fatal("unknown event unexpectedly mapped")
	}
}

func TestDedupAndToolDescriptionLifecycle(t *testing.T) {
	h := newHarness(t)
	input := map[string]json.RawMessage{"command": json.RawMessage(`"go test ./..."`)}
	base := wire.HookPayload{Agent: "codex", SessionID: "thread", Cwd: "/project"}

	base.Event = "UserPromptSubmit"
	h.adapter.HandleHook(base)
	h.adapter.HandleHook(base)
	base.Event, base.ToolName, base.ToolInput = "PreToolUse", "Bash", input
	h.adapter.HandleHook(base)
	h.adapter.HandleHook(base)
	base.Event = "PostToolUse"
	h.adapter.HandleHook(base)
	retainedAfterPost := h.adapter.threads["thread"].lastToolDescription
	base.Event, base.ToolName, base.ToolInput = "Stop", "", nil
	h.adapter.HandleHook(base)

	got := h.snapshot()
	if len(got) != 4 {
		t.Fatalf("got %d events: %#v", len(got), got)
	}
	if !got[1].ToolInvoked || !got[2].ToolInvoked {
		t.Fatal("each PreToolUse must mark a fresh invocation")
	}
	if got[1].ToolDescription != "Running go test ./..." || got[1].ToolVerb != "run" {
		t.Fatalf("tool metadata = %#v", got[1])
	}
	if state := h.adapter.threads["thread"]; state.lastToolDescription != "" {
		t.Fatal("Stop did not clear retained tool description")
	}
	if got[3].ToolDescription != "" || got[3].ToolVerb != "" {
		t.Fatal("Stop did not emit cleared tool metadata")
	}
	if retainedAfterPost != got[1].ToolDescription {
		t.Fatal("PostToolUse did not retain the tool description")
	}
}

func TestPidResolutionBranchesAndAuthoritativeRouting(t *testing.T) {
	cases := []struct {
		name        string
		reported    int
		snapshot    string
		wantPID     int
		wantSession string
		wantEvents  int
	}{
		{"ancestor", 300, "300 200 /bin/sh hook\n200 100 /usr/local/bin/codex", 200, "pid-session", 1},
		{"direct", 200, "200 100 /opt/codex", 200, "pid-session", 1},
		{"unresolved uses cwd", 300, "300 100 /bin/sh hook", 0, "cwd-session", 1},
		{"resolved pid does not fall back", 201, "201 100 /opt/codex", 201, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: "thread", Cwd: "/project", PID: tc.reported, ProcessSnapshot: tc.snapshot})
			state := h.adapter.threads["thread"]
			if state == nil || state.pid != tc.wantPID {
				t.Fatalf("pid = %v, want %d", state, tc.wantPID)
			}
			got := h.snapshot()
			if len(got) != tc.wantEvents {
				t.Fatalf("events = %#v", got)
			}
			if len(got) != 0 && got[0].Session != tc.wantSession {
				t.Fatalf("session = %q, want %q", got[0].Session, tc.wantSession)
			}
		})
	}
}

func TestHandleHookLogsUnroutableThreadOnce(t *testing.T) {
	h := newHarness(t)
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	payload := wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: "unroutable-thread", Cwd: "/missing"}
	for range 4 {
		h.adapter.HandleHook(payload)
	}

	if got := strings.Count(logs.String(), "dropped, no pid and cwd"); got != 1 {
		t.Fatalf("drop log count = %d, want 1; logs:\n%s", got, logs.String())
	}
	if got := h.snapshot(); len(got) != 0 {
		t.Fatalf("unroutable hooks emitted events: %#v", got)
	}
}

func TestPromptFallbackName(t *testing.T) {
	h := newHarness(t)
	prompt := "  first line\x1b[31m\nsecond line  " + strings.Repeat("x", 100)
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: "thread", Cwd: "/project", Prompt: prompt})
	got := h.snapshot()
	if len(got) != 1 || got[0].ThreadName != "first line" {
		t.Fatalf("thread name = %q", got[0].ThreadName)
	}
}

func TestStopRetriesSessionIndexNameResolution(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "session_index.jsonl")
	h := newHarness(t)
	h.adapter.SessionIndexPath = index
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: "thread", Cwd: "/project", Prompt: "Prompt fallback"})
	waitForNameLookup(t, h, "thread")
	writeIndexName(t, index, "thread", "Indexed name")
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: "thread", Cwd: "/project"})
	waitForNameLookup(t, h, "thread")

	got := h.snapshot()
	if len(got) != 3 {
		t.Fatalf("got %d events, want prompt + Stop + one name update: %#v", len(got), got)
	}
	if got[0].ThreadName != "Prompt fallback" || got[1].ThreadName != "Prompt fallback" || got[2].ThreadName != "Indexed name" {
		t.Fatalf("thread name progression = %q, %q, %q", got[0].ThreadName, got[1].ThreadName, got[2].ThreadName)
	}
	if state := h.adapter.threads["thread"]; !state.nameFromIndex {
		t.Fatal("resolved name was not marked as index-sourced")
	}
}

func TestStopNameRetryDoesNotEmitForMissingOrUnchangedName(t *testing.T) {
	for _, tc := range []struct {
		name       string
		indexName  string
		wantSource bool
	}{
		{name: "missing"},
		{name: "unchanged", indexName: "Prompt fallback", wantSource: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			index := filepath.Join(dir, "session_index.jsonl")
			h := newHarness(t)
			h.adapter.SessionIndexPath = index
			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: "thread", Cwd: "/project", Prompt: "Prompt fallback"})
			waitForNameLookup(t, h, "thread")
			if tc.indexName != "" {
				writeIndexName(t, index, "thread", tc.indexName)
			}
			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: "thread", Cwd: "/project"})
			waitForNameLookup(t, h, "thread")

			if got := h.snapshot(); len(got) != 2 {
				t.Fatalf("got %d events, want prompt + Stop only: %#v", len(got), got)
			}
			if state := h.adapter.threads["thread"]; state.nameFromIndex != tc.wantSource {
				t.Fatalf("nameFromIndex = %v, want %v", state.nameFromIndex, tc.wantSource)
			}
		})
	}
}

func TestSessionIndexNameAtCreationOutranksPrompt(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "session_index.jsonl")
	writeIndexName(t, index, "thread", "Indexed name")
	h := newHarness(t)
	h.adapter.SessionIndexPath = index
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "SessionStart", SessionID: "thread", Cwd: "/project"})
	waitForNameLookup(t, h, "thread")
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: "thread", Cwd: "/project", Prompt: "Prompt must not win"})

	state := h.adapter.threads["thread"]
	if state.threadName != "Indexed name" || !state.nameFromIndex {
		t.Fatalf("thread state = name %q, fromIndex %v", state.threadName, state.nameFromIndex)
	}
	got := h.snapshot()
	if got[len(got)-1].ThreadName != "Indexed name" {
		t.Fatalf("prompt overwrote index name: %#v", got)
	}
}

func TestStopRefreshesRenamedSessionIndexName(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "session_index.jsonl")
	writeIndexName(t, index, "thread", "First name")
	h := newHarness(t)
	h.adapter.SessionIndexPath = index
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "SessionStart", SessionID: "thread", Cwd: "/project"})
	waitForNameLookup(t, h, "thread")

	beforeUnchangedStop := len(h.snapshot())
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: "thread", Cwd: "/project"})
	waitForNameLookup(t, h, "thread")
	if got := len(h.snapshot()); got != beforeUnchangedStop {
		t.Fatalf("unchanged index name emitted %d extra events", got-beforeUnchangedStop)
	}

	writeIndexName(t, index, "thread", "Renamed")
	beforeRenameStop := len(h.snapshot())
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: "thread", Cwd: "/project"})
	waitForNameLookup(t, h, "thread")
	got := h.snapshot()
	if len(got) != beforeRenameStop+1 {
		t.Fatalf("rename emitted %d events, want 1: %#v", len(got)-beforeRenameStop, got)
	}
	if got[len(got)-1].ThreadName != "Renamed" {
		t.Fatalf("last thread name = %q, want Renamed", got[len(got)-1].ThreadName)
	}
}

func TestSeedNestedRecentRollout(t *testing.T) {
	dir := t.TempDir()
	threadID := "12345678-1234-1234-1234-123456789abc"
	rolloutDir := filepath.Join(dir, "sessions", "2026", "07", "10")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rolloutDir, "rollout-2026-07-10T00-00-00-"+threadID+".jsonl")
	text := "{\"type\":\"turn_context\",\"payload\":{\"cwd\":\"/project\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call\"}}\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.UnixMilli(999_000)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, "session_index.jsonl")
	if err := os.WriteFile(index, []byte("{\"id\":\""+threadID+"\",\"thread_name\":\"Seed name\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)
	h.adapter.SessionsDir = filepath.Join(dir, "sessions")
	h.adapter.SessionIndexPath = index
	h.adapter.Start(h.adapter.ctx)
	got := h.snapshot()
	if len(got) != 1 || got[0].Status != wire.StatusRunning || got[0].ThreadID != threadID || got[0].ThreadName != "Seed name" || got[0].TS != 999_000 {
		t.Fatalf("seed = %#v", got)
	}
	if state := h.adapter.threads[threadID]; state == nil || !state.nameFromIndex {
		t.Fatalf("seeded name was not marked index-sourced: %#v", state)
	}
}

func TestRolloutStatusMapping(t *testing.T) {
	cases := []struct{ typ, payload, want string }{
		{"event_msg", `{"type":"task_complete"}`, wire.StatusDone},
		{"event_msg", `{"type":"turn_aborted"}`, wire.StatusInterrupted},
		{"event_msg", `{"type":"user_message"}`, wire.StatusRunning},
		{"event_msg", `{"type":"agent_message","phase":"commentary"}`, wire.StatusRunning},
		{"event_msg", `{"type":"agent_message","phase":"final"}`, wire.StatusDone},
		{"event_msg", `{"type":"error"}`, wire.StatusError},
		{"response_item", `{"type":"message","role":"user"}`, wire.StatusRunning},
		{"response_item", `{"type":"message","role":"assistant","phase":"final"}`, wire.StatusDone},
		{"response_item", `{"type":"function_call_output"}`, wire.StatusRunning},
		{"response_item", `{"type":"reasoning"}`, wire.StatusRunning},
	}
	for _, tc := range cases {
		var entry rolloutEntry
		if err := json.Unmarshal([]byte(`{"type":"`+tc.typ+`","payload":`+tc.payload+`}`), &entry); err != nil {
			t.Fatal(err)
		}
		if got := rolloutStatus(entry); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.typ, tc.payload, got, tc.want)
		}
	}
}
