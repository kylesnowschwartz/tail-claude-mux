package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// newAttentionTestServer is a server with one tmux session "work", a
// started Claude Code watcher routing every hook to it, and a GET /state
// that always rebuilds.
func newAttentionTestServer(t *testing.T) *Server {
	t.Helper()
	run := func(args ...string) (string, error) {
		if args[0] == "list-sessions" {
			return "$1\twork\t100\t1\t1\t/p\t0", nil
		}
		return "", nil
	}
	b := &state.Builder{Tmux: &tmux.Tmux{Run: run}, Git: gitinfo.NewCache(), Order: sessionorder.Load("")}
	s := New(b, tracker.New(), ccwatch.New(t.TempDir(), t.TempDir()), nil, nil)
	s.watchersSeeded = true
	s.stateMaxAge = time.Nanosecond
	// A pending broadcast timer keeps emitLocked from scheduling a real one.
	s.broadcastTimer = time.NewTimer(time.Hour)
	s.broadcastTimer.Stop()
	locked := func(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); fn() }
	s.Watcher.Start(&ccwatch.Context{
		ResolveSession:      func(string) string { return "work" },
		ResolveSessionByPid: func(int) string { return "work" },
		Emit:                s.emitLocked,
		Locked:              locked,
		AttentionChanged:    s.debouncedBroadcastLocked,
	})
	return s
}

func agentByThread(t *testing.T, s *Server, threadID string) wire.AgentEvent {
	t.Helper()
	st := getState(t, s)
	for _, sess := range st.Sessions {
		for _, ev := range sess.Agents {
			if ev.ThreadID == threadID {
				return ev
			}
		}
	}
	t.Fatalf("thread %s not in state", threadID)
	return wire.AgentEvent{}
}

func ccHook(event, threadID string) wire.HookPayload {
	return wire.HookPayload{Event: event, SessionID: threadID, Cwd: "/p"}
}

func TestStateCarriesAttentionFromHooks(t *testing.T) {
	s := newAttentionTestServer(t)
	before := time.Now().UnixMilli()
	postHook(t, s, ccHook("UserPromptSubmit", "t1"))
	if ev := agentByThread(t, s, "t1"); ev.Attention != "" || ev.AttentionSince != 0 {
		t.Fatalf("running conversation has attention %+v", ev)
	}
	postHook(t, s, ccHook("Stop", "t1"))
	ev := agentByThread(t, s, "t1")
	if ev.Attention != "finished" || ev.AttentionSince < before {
		t.Fatalf("after Stop: attention=%q since=%d", ev.Attention, ev.AttentionSince)
	}
	st := getState(t, s)
	if as := st.Sessions[0].AgentState; as == nil || as.Attention != "finished" {
		t.Fatalf("agentState = %+v", as)
	}

	// idle_prompt keeps status done (deduped) but turns finished into input.
	idle := ccHook("Notification", "t1")
	idle.NotificationType = "idle_prompt"
	postHook(t, s, idle)
	if ev := agentByThread(t, s, "t1"); ev.Attention != "input" {
		t.Fatalf("after idle_prompt: %q", ev.Attention)
	}

	q := ccHook("PreToolUse", "t1")
	q.ToolName = "AskUserQuestion"
	postHook(t, s, q)
	if ev := agentByThread(t, s, "t1"); ev.Attention != "question" {
		t.Fatalf("after AskUserQuestion: %q", ev.Attention)
	}

	// The JSON carries both fields under their contract names.
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/state", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"attention":"question"`) || !strings.Contains(body, `"attentionSince":`) {
		t.Fatalf("state JSON lacks attention fields: %s", body)
	}

	postHook(t, s, ccHook("UserPromptSubmit", "t1"))
	if ev := agentByThread(t, s, "t1"); ev.Attention != "" || ev.AttentionSince != 0 {
		t.Fatalf("after prompt: %+v", ev)
	}
}
