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
