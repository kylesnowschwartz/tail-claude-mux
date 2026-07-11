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

	"github.com/kylesnowschwartz/agent-ouija/codex/rollout"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
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

func writeRollout(t *testing.T, dir, threadID, source, entries string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-07-10T00-00-00-"+threadID+".jsonl")
	meta := `{"type":"session_meta","payload":{"id":"` + threadID + `","source":` + source + `}}` + "\n"
	if err := os.WriteFile(path, []byte(meta+entries), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
	if _, ok := rollout.ClaimForHookEvent("Unknown"); ok {
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
	const threadID = "12345678-1234-1234-1234-123456789abc"
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
			if tc.wantPID != 0 {
				path := writeRollout(t, h.adapter.SessionsDir, threadID, `"cli"`, "")
				h.adapter.openFilesForPID = func(int) []string { return []string{path} }
			}
			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: threadID, Cwd: "/project", PID: tc.reported, ProcessSnapshot: tc.snapshot})
			state := h.adapter.threads[threadID]
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

func TestHookPidRequiresRolloutOwnership(t *testing.T) {
	const parentPID = 200
	parentThreadID := "12345678-1234-1234-1234-123456789abc"
	foreignThreadID := "87654321-4321-4321-4321-cba987654321"
	payload := wire.HookPayload{
		Agent:           "codex",
		Event:           "UserPromptSubmit",
		Cwd:             "/project",
		PID:             300,
		ProcessSnapshot: "300 200 /bin/sh hook\n200 100 /usr/local/bin/codex",
	}

	t.Run("foreign thread cannot steal parent row", func(t *testing.T) {
		h := newHarness(t)
		parentRollout := writeRollout(t, h.adapter.SessionsDir, parentThreadID, `"cli"`, "")
		h.adapter.openFilesForPID = func(pid int) []string {
			if pid != parentPID {
				t.Fatalf("pid = %d, want %d", pid, parentPID)
			}
			return []string{parentRollout}
		}
		tr := tracker.New()
		tr.ApplyEvent(wire.AgentEvent{
			Agent: "codex", Session: "pid-session", ThreadID: parentThreadID,
			Status: wire.StatusRunning, PaneID: "%7", PID: parentPID,
		}, false)
		h.adapter.ctx.Emit = func(ev wire.AgentEvent) { tr.ApplyEvent(ev, false) }

		payload.SessionID = foreignThreadID
		h.adapter.HandleHook(payload)

		if state := h.adapter.threads[foreignThreadID]; state == nil || state.pid != 0 || state.routingPID != parentPID {
			t.Fatalf("foreign thread state = %#v, want identity pid 0 and routing pid %d", state, parentPID)
		}
		got := tr.GetEvent("pid-session", "codex", parentThreadID, "")
		if got == nil || got.ThreadID != parentThreadID || got.PaneID != "%7" || got.PID != parentPID {
			t.Fatalf("parent state = %#v, want original parent row and pane binding", got)
		}
	})

	t.Run("owned thread keeps pid", func(t *testing.T) {
		h := newHarness(t)
		parentRollout := writeRollout(t, h.adapter.SessionsDir, parentThreadID, `"cli"`, "")
		h.adapter.openFilesForPID = func(int) []string { return []string{parentRollout} }

		payload.SessionID = parentThreadID
		h.adapter.HandleHook(payload)

		got := h.snapshot()
		if len(got) != 1 || got[0].PID != parentPID || got[0].Session != "pid-session" {
			t.Fatalf("events = %#v, want owned pid %d routed to pid-session", got, parentPID)
		}
	})
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

func TestSessionInfoForPidResolvesPrimaryRollout(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions", "2026", "07", "10")
	primaryID := "12345678-1234-1234-1234-123456789abc"
	subagentID := "87654321-4321-4321-4321-cba987654321"
	primary := writeRollout(t, sessions, primaryID, `"cli"`, "")
	subagent := writeRollout(t, sessions, subagentID, `{"subagent":{"other":"guardian"}}`, "")
	index := filepath.Join(dir, "session_index.jsonl")
	writeIndexName(t, index, primaryID, "Primary task")

	a := New(filepath.Join(dir, "sessions"), index)
	a.openFilesForPID = func(pid int) []string {
		if pid != 4242 {
			t.Fatalf("pid = %d, want 4242", pid)
		}
		return []string{subagent, primary}
	}

	threadID, name := a.SessionInfoForPid(4242)
	if threadID != primaryID || name != "Primary task" {
		t.Fatalf("SessionInfoForPid = (%q, %q), want (%q, %q)", threadID, name, primaryID, "Primary task")
	}
}

func TestScanStateForPidReusesRolloutLookup(t *testing.T) {
	dir := t.TempDir()
	threadID := "12345678-1234-1234-1234-123456789abc"
	path := writeRollout(t, filepath.Join(dir, "sessions"), threadID, `"cli"`,
		`{"type":"response_item","payload":{"type":"reasoning"}}`+"\n")
	index := filepath.Join(dir, "session_index.jsonl")
	writeIndexName(t, index, threadID, "Primary task")

	a := New(filepath.Join(dir, "sessions"), index)
	lookups := 0
	a.openFilesForPID = func(pid int) []string {
		lookups++
		return []string{path}
	}

	gotID, gotName, verdict := a.ScanStateForPid(4242, "ignored")
	if gotID != threadID || gotName != "Primary task" || verdict != tracker.ProbeWorking {
		t.Fatalf("ScanStateForPid = (%q, %q, %v), want (%q, %q, ProbeWorking)", gotID, gotName, verdict, threadID, "Primary task")
	}
	if lookups != 1 {
		t.Fatalf("rollout lookups = %d, want 1", lookups)
	}
}

func TestScanStateForPidClassifiesCompletedRollout(t *testing.T) {
	dir := t.TempDir()
	threadID := "12345678-1234-1234-1234-123456789abc"
	path := writeRollout(t, filepath.Join(dir, "sessions"), threadID, `"cli"`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
	a := New(filepath.Join(dir, "sessions"), filepath.Join(dir, "index.jsonl"))
	a.openFilesForPID = func(int) []string { return []string{path} }

	gotID, _, verdict := a.ScanStateForPid(4242, "")
	if gotID != threadID || verdict != tracker.ProbeDone {
		t.Fatalf("ScanStateForPid = (%q, %v), want (%q, ProbeDone)", gotID, verdict, threadID)
	}
}

func TestProbeLiveStatusFromRollout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries string
		want    tracker.ProbeVerdict
	}{
		{
			name:    "working",
			entries: `{"type":"response_item","payload":{"type":"reasoning"}}` + "\n",
			want:    tracker.ProbeWorking,
		},
		{
			name:    "idle",
			entries: `{"type":"turn_context","payload":{"cwd":"/project"}}` + "\n",
			want:    tracker.ProbeNoSignal,
		},
		{
			name:    "completed",
			entries: `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n",
			want:    tracker.ProbeEnded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			threadID := "12345678-1234-1234-1234-123456789abc"
			path := writeRollout(t, filepath.Join(dir, "sessions"), threadID, `"cli"`, tc.entries)
			a := New(filepath.Join(dir, "sessions"), filepath.Join(dir, "index.jsonl"))
			a.openFilesForPID = func(int) []string { return []string{path} }
			if got := a.ProbeLiveStatus(4242, threadID, "ignored"); got != tc.want {
				t.Fatalf("ProbeLiveStatus = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStopUsesDurableTurnStatusAndNewPromptRestarts(t *testing.T) {
	tests := []struct {
		name, entries, want string
	}{
		{name: "completed", entries: `{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n", want: wire.StatusDone},
		{name: "interrupted", entries: `{"type":"event_msg","payload":{"type":"turn_aborted"}}` + "\n", want: wire.StatusInterrupted},
		{name: "error", entries: `{"type":"event_msg","payload":{"type":"error"}}` + "\n", want: wire.StatusError},
		{name: "idle without completion evidence", entries: `{"type":"turn_context","payload":{"cwd":"/project"}}` + "\n", want: wire.StatusIdle},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			threadID := "12345678-1234-1234-1234-123456789abc"
			writeRollout(t, h.adapter.SessionsDir, threadID, `"cli"`, tc.entries)

			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: threadID, Cwd: "/project"})
			got := h.snapshot()
			if len(got) != 1 || got[0].Status != tc.want {
				t.Fatalf("Stop events = %#v, want status %q", got, tc.want)
			}

			h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: threadID, Cwd: "/project", Prompt: "continue"})
			got = h.snapshot()
			if got[len(got)-1].Status != wire.StatusRunning {
				t.Fatalf("new prompt status = %q, want running", got[len(got)-1].Status)
			}
		})
	}
}

func TestCompletedStopPreservesAlivePaneState(t *testing.T) {
	dir := t.TempDir()
	threadID := "12345678-1234-1234-1234-123456789abc"
	adapter := New(dir, filepath.Join(dir, "index.jsonl"))
	writeRollout(t, dir, threadID, `"cli"`, `{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
	tr := tracker.New()
	tr.ApplyEvent(wire.AgentEvent{
		Agent: "codex", Session: "work", ThreadID: threadID, Status: wire.StatusRunning,
		PaneID: "%7", PID: 200, Liveness: wire.LivenessAlive,
	}, false)
	adapter.ctx = &Context{
		ResolveSession: func(string) string { return "work" },
		Emit:           func(ev wire.AgentEvent) { tr.ApplyEvent(ev, false) },
		Locked:         func(fn func()) { fn() },
	}

	adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: threadID, Cwd: "/project"})
	got := tr.GetState("work")
	if got == nil || got.Status != wire.StatusDone || got.Liveness != wire.LivenessAlive || got.PaneID != "%7" {
		t.Fatalf("tracked state = %#v, want alive done state on pane %%7", got)
	}

	adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: threadID, Cwd: "/project"})
	if got := tr.GetState("work"); got == nil || got.Status != wire.StatusRunning || got.Liveness != wire.LivenessAlive {
		t.Fatalf("tracked state after new turn = %#v, want alive running", got)
	}
}

func TestAutoReviewedApprovalDoesNotBecomeWaitingAndCompletedTurnBecomesDone(t *testing.T) {
	dir := t.TempDir()
	threadID := "12345678-1234-1234-1234-123456789abc"
	path := writeRollout(t, dir, threadID, `"cli"`,
		`{"type":"turn_context","payload":{"cwd":"/project","approvals_reviewer":"auto_review"}}`+"\n"+
			`{"type":"response_item","payload":{"type":"reasoning"}}`+"\n")

	h := newHarness(t)
	h.adapter.SessionsDir = dir
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: threadID, Cwd: "/project"})
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "PermissionRequest", SessionID: threadID, Cwd: "/project"})
	for _, ev := range h.snapshot() {
		if ev.Status == wire.StatusWaiting {
			t.Fatalf("auto-reviewed approval emitted waiting: %#v", h.snapshot())
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"RESULT: complete"}]}}` + "\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append final assistant message: write=%v close=%v", writeErr, closeErr)
	}

	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "Stop", SessionID: threadID, Cwd: "/project"})
	got := h.snapshot()
	if got[len(got)-1].Status != wire.StatusDone {
		t.Fatalf("completed turn status = %q, want done; events=%#v", got[len(got)-1].Status, got)
	}
}

func TestManualApprovalStillBecomesWaitingPromptly(t *testing.T) {
	dir := t.TempDir()
	threadID := "12345678-1234-1234-1234-123456789abc"
	writeRollout(t, dir, threadID, `"cli"`,
		`{"type":"turn_context","payload":{"cwd":"/project"}}`+"\n"+
			`{"type":"response_item","payload":{"type":"reasoning"}}`+"\n")

	h := newHarness(t)
	h.adapter.SessionsDir = dir
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "UserPromptSubmit", SessionID: threadID, Cwd: "/project"})
	h.adapter.HandleHook(wire.HookPayload{Agent: "codex", Event: "PermissionRequest", SessionID: threadID, Cwd: "/project"})
	got := h.snapshot()
	if got[len(got)-1].Status != wire.StatusWaiting {
		t.Fatalf("manual approval status = %q, want waiting", got[len(got)-1].Status)
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
		state, err := rollout.TrailingState(strings.NewReader(`{"type":"` + tc.typ + `","payload":` + tc.payload + "}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := wireStatus(state.Status); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.typ, tc.payload, got, tc.want)
		}
	}
}
