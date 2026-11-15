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
