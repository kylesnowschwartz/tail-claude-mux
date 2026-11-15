package ccwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/attention"
)

func TestAttentionChangeOnDedupedEventCallsAttentionChanged(t *testing.T) {
	tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
	calls := 0
	tc.ctx.AttentionChanged = func() { calls++ }
	a := newStartedAdapter(t, tc)

	a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
	a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))
	if got := a.Attention.Get("sess-1").Name; got != attention.Finished {
		t.Fatalf("after Stop attention = %q", got)
	}
	emitted := len(tc.events)

	// idle_prompt resolves to done, the status the thread already has: the
	// status dedup drops the event, and the reason change takes the callback.
	a.HandleHook(notifHook("Notification", "sess-1", "/tmp/myproject", "idle_prompt"))
	if len(tc.events) != emitted {
		t.Fatalf("deduped event emitted: %+v", tc.events[emitted:])
	}
	if got := a.Attention.Get("sess-1").Name; got != attention.Input {
		t.Fatalf("attention = %q, want input", got)
	}
	if calls != 1 {
		t.Fatalf("AttentionChanged calls = %d, want 1", calls)
	}

	// A second idle_prompt changes nothing and calls nothing.
	a.HandleHook(notifHook("Notification", "sess-1", "/tmp/myproject", "idle_prompt"))
	if calls != 1 {
		t.Fatalf("AttentionChanged calls = %d, want 1", calls)
	}
}

func TestAttentionForgottenOnSessionEnd(t *testing.T) {
	tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
	a := newStartedAdapter(t, tc)
	a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))
	a.HandleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"))
	if got := a.Attention.Get("sess-1"); got.Name != "" {
		t.Fatalf("attention after SessionEnd = %+v", got)
	}
}

func TestSeedRestoresPermissionFromOpenDialog(t *testing.T) {
	projectsDir, sessionsDir := t.TempDir(), t.TempDir()
	a := New(projectsDir, sessionsDir)
	const threadID = "seed-dialog"
	projDir := filepath.Join(projectsDir, "-tmp-myproject")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"message":{"role":"user","content":[{"type":"text","text":"go"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, threadID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	dialogAt := time.Now().UnixMilli() - 2000
	writeSessionFile(t, sessionsDir, 4242, map[string]any{
		"pid": 4242, "sessionId": threadID, "status": "waiting", "statusUpdatedAt": dialogAt, "updatedAt": dialogAt,
	})
	tc := makeCtx(nil, map[int]string{4242: "pid-session"})
	a.Start(&tc.ctx)
	if got := a.Attention.Get(threadID); got != (attention.Reason{Name: attention.Permission, SinceMS: dialogAt}) {
		t.Fatalf("seeded attention = %+v, want permission at %d", got, dialogAt)
	}
}
