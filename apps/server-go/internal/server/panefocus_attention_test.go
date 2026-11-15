package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func bindPane(s *Server, threadID, paneID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Tracker.ApplyEvent(wire.AgentEvent{Agent: "claude-code", Session: "work", ThreadID: threadID, PaneID: paneID, Status: status, TS: time.Now().UnixMilli()}, false)
}

func postPaneFocus(t *testing.T, s *Server, paneID string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/pane-focus", strings.NewReader(paneID)))
	if rec.Code != 200 {
		t.Fatalf("pane-focus status = %d", rec.Code)
	}
}

func TestPaneFocusEndsFocusReasonsInThatPaneOnly(t *testing.T) {
	for _, tc := range []struct {
		reason string
		hooks  []wire.HookPayload
	}{
		{"finished", []wire.HookPayload{ccHook("Stop", "a")}},
		{"error", []wire.HookPayload{ccHook("StopFailure", "a")}},
		{"input", []wire.HookPayload{ccHook("Stop", "a"), {Event: "Notification", SessionID: "a", Cwd: "/p", NotificationType: "idle_prompt"}}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			s := newAttentionTestServer(t)
			for _, h := range tc.hooks {
				postHook(t, s, h)
			}
			postHook(t, s, ccHook("Stop", "b"))
			bindPane(s, "a", "%1", agentByThread(t, s, "a").Status)
			bindPane(s, "b", "%2", wire.StatusDone)
			if got := agentByThread(t, s, "a").Attention; got != tc.reason {
				t.Fatalf("before focus a = %q", got)
			}

			postPaneFocus(t, s, "%1")
			if ev := agentByThread(t, s, "a"); ev.Attention != "" || ev.AttentionSince != 0 {
				t.Fatalf("focused pane kept %+v", ev)
			}
			if got := agentByThread(t, s, "b").Attention; got != "finished" {
				t.Fatalf("other pane lost its reason: %q", got)
			}
		})
	}
}

func TestPaneFocusKeepsPermissionAndQuestion(t *testing.T) {
	for _, tc := range []struct {
		reason string
		tool   string
		event  string
	}{
		{"permission", "Bash", "PermissionRequest"},
		{"question", "AskUserQuestion", "PreToolUse"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			s := newAttentionTestServer(t)
			h := ccHook(tc.event, "a")
			h.ToolName = tc.tool
			postHook(t, s, h)
			bindPane(s, "a", "%1", agentByThread(t, s, "a").Status)
			postPaneFocus(t, s, "%1")
			if got := agentByThread(t, s, "a").Attention; got != tc.reason {
				t.Fatalf("after focus = %q, want %q", got, tc.reason)
			}
		})
	}
}
