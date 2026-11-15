package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/activity"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func openActivity(t *testing.T, dir string, now func() time.Time) *activity.Writer {
	t.Helper()
	w, err := activity.Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func postHook(t *testing.T, s *Server, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/hook", strings.NewReader(string(body))))
	if rec.Code != 200 {
		t.Fatalf("hook status = %d", rec.Code)
	}
}

func readRecord(t *testing.T, dir string) []map[string]any {
	t.Helper()
	page, err := activity.Read(dir, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, raw := range page.Events {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func recordFileText(t *testing.T, dir string) string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var b strings.Builder
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		b.Write(data)
	}
	return b.String()
}

func TestUnmatchedHookEventIsRecorded(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Activity: openActivity(t, dir, nil)}
	postHook(t, s, wire.HookPayload{Event: "Stop", SessionID: "sess-1", Cwd: "/project", PID: 77, StopReason: "stop", TranscriptPath: "/t.jsonl"})

	lines := readRecord(t, dir)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	got := lines[0]
	if got["kind"] != "hook" || got["event"] != "Stop" || got["session_id"] != "sess-1" || got["seq"] != 1.0 || got["pid"] != 77.0 || got["stop_reason"] != "stop" {
		t.Fatalf("line = %v", got)
	}
	t.Logf("hook line: %s", strings.TrimSpace(recordFileText(t, dir)))
}

func TestHookLineHoldsNoConversationContent(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Activity: openActivity(t, dir, nil)}
	postHook(t, s, map[string]any{
		"event": "UserPromptSubmit", "session_id": "sess-1", "cwd": "/project",
		"prompt": "secret-prompt-text-1234",
	})
	postHook(t, s, map[string]any{
		"event": "PreToolUse", "session_id": "sess-1", "cwd": "/project", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "echo secret-tool-input-5678"},
	})
	text := recordFileText(t, dir)
	if len(readRecord(t, dir)) != 2 {
		t.Fatalf("record = %s", text)
	}
	for _, secret := range []string{"secret-prompt-text-1234", "secret-tool-input-5678"} {
		if strings.Contains(text, secret) {
			t.Fatalf("record holds %q:\n%s", secret, text)
		}
	}
}

func TestMatchedHookEventRecordsHookThenStatus(t *testing.T) {
	dir := t.TempDir()
	s := New(&state.Builder{Tmux: &tmux.Tmux{Run: func(...string) (string, error) { return "", nil }}}, tracker.New(), ccwatch.New(t.TempDir(), t.TempDir()), nil, nil)
	s.Activity = openActivity(t, dir, nil)
	s.watchersSeeded = true
	// A pending broadcast timer keeps emitLocked from scheduling a real one.
	s.broadcastTimer = time.NewTimer(time.Hour)
	s.broadcastTimer.Stop()
	locked := func(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); fn() }
	s.Watcher.Start(&ccwatch.Context{
		ResolveSession:      func(string) string { return "work" },
		ResolveSessionByPid: func(int) string { return "work" },
		Emit:                s.emitLocked,
		Locked:              locked,
	})
	postHook(t, s, wire.HookPayload{Event: "UserPromptSubmit", SessionID: "sess-1", Cwd: "/project", Prompt: "hello"})

	lines := readRecord(t, dir)
	if len(lines) < 2 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0]["kind"] != "hook" || lines[0]["seq"] != 1.0 || lines[1]["kind"] != "status" || lines[1]["seq"] != 2.0 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[1]["tmux_session"] != "work" || lines[1]["session_id"] != "sess-1" || lines[1]["status"] == "" {
		t.Fatalf("status line = %v", lines[1])
	}
	t.Logf("record:\n%s", recordFileText(t, dir))
}

func getEvents(t *testing.T, s *Server, query string) activity.Page {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/events"+query, nil))
	if rec.Code != 200 {
		t.Fatalf("GET /events%s = %d %s", query, rec.Code, rec.Body)
	}
	t.Logf("GET /events%s -> %s", query, strings.TrimSpace(rec.Body.String()))
	var page activity.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func pageSeqs(t *testing.T, page activity.Page) []float64 {
	t.Helper()
	var out []float64
	for _, raw := range page.Events {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out = append(out, m["seq"].(float64))
	}
	return out
}

func TestEventsRouteReadsAcrossDaysAndRestart(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 15, 23, 59, 0, 0, time.Local)
	now := func() time.Time { return clock }
	first, _ := activity.Open(dir, now)
	s := &Server{Activity: first}
	postHook(t, s, wire.HookPayload{Event: "A", SessionID: "s", Cwd: "/p"})
	postHook(t, s, wire.HookPayload{Event: "B", SessionID: "s", Cwd: "/p"})
	_ = first.Close()

	// Restart on the next day.
	clock = clock.Add(2 * time.Minute)
	s = &Server{Activity: openActivity(t, dir, now)}
	postHook(t, s, wire.HookPayload{Event: "C", SessionID: "s", Cwd: "/p"})
	postHook(t, s, wire.HookPayload{Event: "D", SessionID: "s", Cwd: "/p"})
	// A last line with no newline is still being written.
	f, _ := os.OpenFile(filepath.Join(dir, "2026-11-16.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"seq":5,"kind":"hook","rece`)
	_ = f.Close()

	page := getEvents(t, s, "?since=0")
	if got := pageSeqs(t, page); len(got) != 4 || got[3] != 4 || page.OldestSeq != 1 {
		t.Fatalf("page = %v oldest=%d", got, page.OldestSeq)
	}
	page = getEvents(t, s, "?since=1&limit=2")
	if got := pageSeqs(t, page); len(got) != 2 || got[0] != 2 || got[1] != 3 || page.OldestSeq != 1 {
		t.Fatalf("limited page = %v oldest=%d", got, page.OldestSeq)
	}
}

func TestEventsRouteSinceBeforeOldestStartsAtOldest(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, time.Now().Format("2006-01-02")+".jsonl"),
		[]byte(`{"seq":41,"kind":"hook","received_ms":1,"event":"A"}`+"\n"+`{"seq":42,"kind":"status","received_ms":2,"status":"idle"}`+"\n"), 0o600)
	s := &Server{Activity: openActivity(t, dir, nil)}

	page := getEvents(t, s, "?since=3")
	if got := pageSeqs(t, page); len(got) != 2 || got[0] != 41 || page.OldestSeq != 41 {
		t.Fatalf("page = %v oldest=%d", got, page.OldestSeq)
	}
}

func TestEventsRouteRejectsBadQuery(t *testing.T) {
	s := &Server{Activity: openActivity(t, t.TempDir(), nil)}
	for _, q := range []string{"?since=x", "?since=-1", "?limit=0", "?limit=abc"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/events"+q, nil))
		if rec.Code != 400 {
			t.Fatalf("%s = %d", q, rec.Code)
		}
	}
}

func TestStatusLinesOnlyOnChangeAndNotWhileSeeding(t *testing.T) {
	dir := t.TempDir()
	s := New(&state.Builder{Tmux: &tmux.Tmux{Run: func(...string) (string, error) { return "", nil }}}, tracker.New(), nil, nil, nil)
	s.Activity = openActivity(t, dir, nil)
	s.broadcastTimer = time.NewTimer(time.Hour)
	s.broadcastTimer.Stop()
	emit := func(status string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.emitLocked(wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: "t1", PaneID: "%3", Status: status})
	}
	emit(wire.StatusIdle) // start-up rebuild: not recorded
	s.watchersSeeded = true
	emit(wire.StatusRunning)
	emit(wire.StatusRunning)
	emit(wire.StatusIdle)

	var got []any
	for _, l := range readRecord(t, dir) {
		got = append(got, l["status"])
	}
	if len(got) != 2 || got[0] != wire.StatusRunning || got[1] != wire.StatusIdle {
		t.Fatalf("statuses = %v, want [running idle]", got)
	}
}

func TestClaudeStatusLineCarriesKnownPaneID(t *testing.T) {
	dir := t.TempDir()
	s := New(&state.Builder{Tmux: &tmux.Tmux{Run: func(...string) (string, error) { return "", nil }}}, tracker.New(), ccwatch.New(t.TempDir(), t.TempDir()), nil, nil)
	s.Activity = openActivity(t, dir, nil)
	s.watchersSeeded = true
	s.broadcastTimer = time.NewTimer(time.Hour)
	s.broadcastTimer.Stop()
	locked := func(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); fn() }
	s.Watcher.Start(&ccwatch.Context{
		ResolveSession:      func(string) string { return "work" },
		ResolveSessionByPid: func(int) string { return "work" },
		Emit:                s.emitLocked,
		Locked:              locked,
	})
	postHook(t, s, wire.HookPayload{Event: "UserPromptSubmit", SessionID: "sess-1", Cwd: "/project"})
	locked(func() {
		s.Tracker.ApplyPanePresence("work", []tracker.PanePresence{{Agent: "claude-code", PaneID: "%9", ThreadID: "sess-1"}})
	})
	if ev := s.Tracker.GetEvent("work", "claude-code", "sess-1", ""); ev == nil || ev.PaneID != "%9" {
		t.Fatalf("tracker row = %+v", ev)
	}
	postHook(t, s, wire.HookPayload{Event: "Stop", SessionID: "sess-1", Cwd: "/project"})

	var statuses []map[string]any
	for _, l := range readRecord(t, dir) {
		if l["kind"] == "status" {
			statuses = append(statuses, l)
		}
	}
	if len(statuses) != 2 || statuses[1]["pane_id"] != "%9" || statuses[1]["status"] == statuses[0]["status"] {
		t.Fatalf("status lines = %v", statuses)
	}
}
