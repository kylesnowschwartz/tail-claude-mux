package server

import (
	"errors"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// focusedOn answers list-clients as one attached client looking at pane.
func focusedOn(pane string) func() (string, error) {
	return func() (string, error) { return pane, nil }
}

func TestFocusedPaneSuppressesFinishedAndError(t *testing.T) {
	for _, hook := range []string{"Stop", "StopFailure"} {
		t.Run(hook, func(t *testing.T) {
			s := newAttentionTestServerWithClients(t, focusedOn("%1"))
			postHook(t, s, ccHook("UserPromptSubmit", "a"))
			postHook(t, s, ccHook("UserPromptSubmit", "b"))
			bindPane(s, "a", "%1", wire.StatusRunning)
			bindPane(s, "b", "%2", wire.StatusRunning)
			postHook(t, s, ccHook(hook, "a"))
			postHook(t, s, ccHook(hook, "b"))
			if got := s.Watcher.Attention.Get("a"); got.Name != "" {
				t.Fatalf("focused pane got %+v", got)
			}
			if got := s.Watcher.Attention.Get("b"); got.Name == "" {
				t.Fatal("unfocused pane got no reason")
			}
		})
	}
}

func TestFocusedPaneStillStartsPermissionAndQuestion(t *testing.T) {
	s := newAttentionTestServerWithClients(t, focusedOn("%1"))
	postHook(t, s, ccHook("UserPromptSubmit", "a"))
	bindPane(s, "a", "%1", wire.StatusRunning)
	p := ccHook("PermissionRequest", "a")
	p.ToolName = "Bash"
	postHook(t, s, p)
	if got := s.Watcher.Attention.Get("a").Name; got != "permission" {
		t.Fatalf("got %q", got)
	}
	q := ccHook("PreToolUse", "a")
	q.ToolName = "AskUserQuestion"
	postHook(t, s, q)
	if got := s.Watcher.Attention.Get("a").Name; got != "question" {
		t.Fatalf("got %q", got)
	}
}

func TestFocusedPaneAtIdlePromptKeepsFinished(t *testing.T) {
	focused := ""
	s := newAttentionTestServerWithClients(t, func() (string, error) { return focused, nil })
	postHook(t, s, ccHook("UserPromptSubmit", "a"))
	bindPane(s, "a", "%1", wire.StatusRunning)
	postHook(t, s, ccHook("Stop", "a"))
	held := s.Watcher.Attention.Get("a")
	if held.Name != "finished" {
		t.Fatalf("not finished: %+v", held)
	}
	focused = "%1"
	idle := ccHook("Notification", "a")
	idle.NotificationType = "idle_prompt"
	postHook(t, s, idle)
	if got := s.Watcher.Attention.Get("a"); got != held {
		t.Fatalf("got %+v, want %+v kept", got, held)
	}
}

func TestUnknownFocusStartsReason(t *testing.T) {
	s := newAttentionTestServerWithClients(t, func() (string, error) { return "", errors.New("no server") })
	postHook(t, s, ccHook("UserPromptSubmit", "a"))
	bindPane(s, "a", "%1", wire.StatusRunning)
	postHook(t, s, ccHook("Stop", "a"))
	if got := s.Watcher.Attention.Get("a").Name; got != "finished" {
		t.Fatalf("got %q", got)
	}
}
