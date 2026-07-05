package wire

import (
	"fmt"
	"strings"
	"testing"
)

// Semantics under test mirror parse-hook-payload.ts: required fields
// reject the whole event; malformed optional fields are dropped
// individually.

func TestParseHookPayload_Required(t *testing.T) {
	valid := `{"event":"PreToolUse","session_id":"s1","cwd":"/proj"}`
	if p, ok := ParseHookPayload([]byte(valid)); !ok || p.Event != "PreToolUse" || p.SessionID != "s1" || p.Cwd != "/proj" {
		t.Errorf("minimal valid payload: got %+v ok=%v", p, ok)
	}

	rejects := []string{
		`not json`,
		`[]`,
		`{}`,
		`{"event":"","session_id":"s1","cwd":"/p"}`,                                          // empty event
		`{"event":"E","session_id":"s1"}`,                                                    // missing cwd
		`{"event":"E","session_id":"s1","cwd":""}`,                                           // empty cwd
		`{"event":"E","session_id":123,"cwd":"/p"}`,                                          // wrong type
		fmt.Sprintf(`{"event":"%s","session_id":"s1","cwd":"/p"}`, strings.Repeat("e", 129)), // event over bound
	}
	for _, r := range rejects {
		if _, ok := ParseHookPayload([]byte(r)); ok {
			t.Errorf("must reject: %.60s", r)
		}
	}
}

func TestParseHookPayload_OptionalFieldsDropIndividually(t *testing.T) {
	body := `{
		"event": "Stop",
		"session_id": "s1",
		"cwd": "/proj",
		"agent": "claude-code",
		"tool_name": 42,
		"tool_input": {"file_path": "/a.txt"},
		"stop_reason": "made-up-reason",
		"shutdown_reason": "quit",
		"tool_is_error": false,
		"pid": 1234,
		"process_snapshot": "1 0 init"
	}`
	p, ok := ParseHookPayload([]byte(body))
	if !ok {
		t.Fatal("payload with malformed optionals must still parse")
	}
	if p.Agent != "claude-code" {
		t.Errorf("agent = %q", p.Agent)
	}
	if p.ToolName != "" {
		t.Error("non-string tool_name must be dropped, not fail the event")
	}
	if p.ToolInput == nil || string(p.ToolInput["file_path"]) != `"/a.txt"` {
		t.Errorf("tool_input must decode, got %v", p.ToolInput)
	}
	if p.StopReason != "" {
		t.Error("stop_reason outside the allow-set must be dropped")
	}
	if p.ShutdownReason != "quit" {
		t.Errorf("shutdown_reason = %q", p.ShutdownReason)
	}
	if p.ToolIsError == nil || *p.ToolIsError != false {
		t.Error("tool_is_error false must survive as a present pointer")
	}
	if p.PID != 1234 {
		t.Errorf("pid = %d", p.PID)
	}
	if p.ProcessSnapshot != "1 0 init" {
		t.Errorf("process_snapshot = %q", p.ProcessSnapshot)
	}
}

func TestParseHookPayload_PidEdgeCases(t *testing.T) {
	cases := []struct {
		pidJSON string
		want    int
	}{
		{`2`, 2},
		{`1`, 0},    // init — never an agent
		{`0`, 0},    // kernel
		{`-5`, 0},   // negative
		{`3.5`, 0},  // fractional — Number.isInteger fails on TS side
		{`"77"`, 0}, // string — wrong type
	}
	for _, c := range cases {
		body := fmt.Sprintf(`{"event":"E","session_id":"s","cwd":"/p","pid":%s}`, c.pidJSON)
		p, ok := ParseHookPayload([]byte(body))
		if !ok {
			t.Errorf("pid %s must not reject the event", c.pidJSON)
			continue
		}
		if p.PID != c.want {
			t.Errorf("pid %s = %d, want %d", c.pidJSON, p.PID, c.want)
		}
	}
}
