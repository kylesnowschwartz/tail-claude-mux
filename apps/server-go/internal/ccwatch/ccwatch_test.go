// Port of packages/runtime/test/claude-code-hooks.test.ts (plus the
// classifyTitleStatus cases from title-status.test.ts). Subtest names keep
// the TS test names verbatim, grouped by describe block.
package ccwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/claude/registry"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// testCtx is the Go analogue of the TS makeCtx helper: a Context whose Emit
// collects events, with the same session-map suffix matching in both
// directions and a pid map for ResolveSessionByPid.
type testCtx struct {
	events []wire.AgentEvent
	ctx    Context
}

func makeCtx(sessionMap map[string]string, pidMap map[int]string) *testCtx {
	tc := &testCtx{}
	tc.ctx = Context{
		ResolveSession: func(projectDir string) string {
			// Direct match first.
			if v, ok := sessionMap[projectDir]; ok {
				return v
			}
			// Check if any key is a suffix of projectDir (for absolute path matching).
			for k, v := range sessionMap {
				if strings.HasSuffix(projectDir, k) || strings.HasSuffix(k, projectDir) {
					return v
				}
			}
			return ""
		},
		ResolveSessionByPid: func(pid int) string { return pidMap[pid] },
		Emit:                func(ev wire.AgentEvent) { tc.events = append(tc.events, ev) },
		Locked:              func(fn func()) { fn() },
	}
	return tc
}

func hook(event, sessionID, cwd string) wire.HookPayload {
	return wire.HookPayload{Event: event, SessionID: sessionID, Cwd: cwd}
}

func toolHook(event, sessionID, cwd, toolName string, toolInput map[string]json.RawMessage) wire.HookPayload {
	p := hook(event, sessionID, cwd)
	p.ToolName = toolName
	p.ToolInput = toolInput
	return p
}

func notifHook(event, sessionID, cwd, notificationType string) wire.HookPayload {
	p := hook(event, sessionID, cwd)
	p.NotificationType = notificationType
	return p
}

// newStartedAdapter builds an Adapter over empty temp dirs and starts it —
// the equivalent of the TS beforeEach that starts without a seed.
func newStartedAdapter(t *testing.T, tc *testCtx) *Adapter {
	t.Helper()
	a := New(t.TempDir(), t.TempDir())
	a.Start(&tc.ctx)
	t.Cleanup(a.Stop)
	return a
}

func writeSessionFile(t *testing.T, sessionsDir string, pid int, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal session payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, strconv.Itoa(pid)+".json"), raw, 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
}

func wantLen(t *testing.T, events []wire.AgentEvent, n int) {
	t.Helper()
	if len(events) != n {
		t.Fatalf("got %d events, want %d: %+v", len(events), n, events)
	}
}

// describe("ClaudeCodeHookAdapter")
func TestClaudeCodeHookAdapter(t *testing.T) {
	setup := func(t *testing.T) (*Adapter, *testCtx) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
		return newStartedAdapter(t, tc), tc
	}

	// TS "implements HookReceiver" is not ported: it duck-type-checks bun's
	// structural HookReceiver interface, which has no Go counterpart — the
	// server calls HandleHook on the concrete *Adapter, so the signature is
	// already compile-checked.

	t.Run("has name 'claude-code'", func(t *testing.T) {
		a, _ := setup(t)
		if got := a.Name(); got != "claude-code" {
			t.Fatalf("Name() = %q, want %q", got, "claude-code")
		}
	})

	// --- UserPromptSubmit ---

	t.Run("UserPromptSubmit emits running", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		ev := tc.events[0]
		if ev.Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", ev.Status)
		}
		if ev.Session != "myproject" {
			t.Errorf("session = %q, want myproject", ev.Session)
		}
		if ev.ThreadID != "sess-1" {
			t.Errorf("threadId = %q, want sess-1", ev.ThreadID)
		}
		if ev.Agent != "claude-code" {
			t.Errorf("agent = %q, want claude-code", ev.Agent)
		}
	})

	// --- PreToolUse ---

	t.Run("PreToolUse emits running", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read", nil))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
	})

	// TS "PreToolUse does not promote to waiting (no timer heuristic)" is
	// not ported: it slept 3.5s to prove a removed TS timer stayed removed.
	// The Go port never had a timer path (no goroutine emits statuses), and
	// its synchronous assertion duplicates "PreToolUse emits running".

	// --- PostToolUse ---

	t.Run("PostToolUse emits running", func(t *testing.T) {
		a, tc := setup(t)
		// First set to waiting via PermissionRequest, then PostToolUse returns to running.
		a.HandleHook(toolHook("PermissionRequest", "sess-1", "/tmp/myproject", "Bash", nil))
		a.HandleHook(toolHook("PostToolUse", "sess-1", "/tmp/myproject", "Bash", nil))

		wantLen(t, tc.events, 2)
		if tc.events[1].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[1].Status)
		}
	})

	// --- PermissionRequest ---

	t.Run("PermissionRequest emits waiting", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PermissionRequest", "sess-1", "/tmp/myproject", "Bash", nil))

		wantLen(t, tc.events, 1)
		ev := tc.events[0]
		if ev.Status != wire.StatusWaiting {
			t.Errorf("status = %q, want waiting", ev.Status)
		}
		if ev.Session != "myproject" {
			t.Errorf("session = %q, want myproject", ev.Session)
		}
		if ev.ThreadID != "sess-1" {
			t.Errorf("threadId = %q, want sess-1", ev.ThreadID)
		}
	})

	t.Run("PermissionRequest followed by PostToolUse transitions to running", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PermissionRequest", "sess-1", "/tmp/myproject", "Bash", nil))
		a.HandleHook(toolHook("PostToolUse", "sess-1", "/tmp/myproject", "Bash", nil))

		wantLen(t, tc.events, 2)
		if tc.events[0].Status != wire.StatusWaiting {
			t.Errorf("events[0].status = %q, want waiting", tc.events[0].Status)
		}
		if tc.events[1].Status != wire.StatusRunning {
			t.Errorf("events[1].status = %q, want running", tc.events[1].Status)
		}
	})

	// --- Stop ---

	t.Run("Stop emits done", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusDone {
			t.Errorf("status = %q, want done", tc.events[0].Status)
		}
	})

	// --- Notification ---

	t.Run("Notification with permission_prompt emits waiting", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(notifHook("Notification", "sess-1", "/tmp/myproject", "permission_prompt"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusWaiting {
			t.Errorf("status = %q, want waiting", tc.events[0].Status)
		}
	})

	t.Run("Notification with idle_prompt emits done (idle at prompt, not waiting)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(notifHook("Notification", "sess-1", "/tmp/myproject", "idle_prompt"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusDone {
			t.Errorf("status = %q, want done", tc.events[0].Status)
		}
	})

	t.Run("Notification without notification_type is ignored", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("Notification", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 0)
	})

	t.Run("Notification with auth_success is ignored", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(notifHook("Notification", "sess-1", "/tmp/myproject", "auth_success"))

		wantLen(t, tc.events, 0)
	})

	// --- Unknown event ---

	t.Run("unknown event emits nothing", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("SomeNewEvent", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 0)
	})

	// --- Agent discriminator ---

	t.Run("payload with agent: 'pi' is ignored", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("session_start", "sess-pi-1", "/tmp/myproject")
		p.Agent = "pi"
		a.HandleHook(p)

		wantLen(t, tc.events, 0)
	})

	t.Run("payload with agent: 'claude-code' still dispatches", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.Agent = "claude-code"
		a.HandleHook(p)

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusIdle {
			t.Errorf("status = %q, want idle", tc.events[0].Status)
		}
	})

	t.Run("payload without agent field still dispatches (legacy)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("SessionStart", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Agent != "claude-code" {
			t.Errorf("agent = %q, want claude-code", tc.events[0].Agent)
		}
	})

	// --- Unresolved session ---

	t.Run("unresolved cwd emits nothing", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/unknown-project"))

		wantLen(t, tc.events, 0)
	})

	// --- Multiple threads ---

	t.Run("tracks independent threads", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("UserPromptSubmit", "sess-2", "/tmp/myproject"))
		a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		want := []struct{ threadID, status string }{
			{"sess-1", wire.StatusRunning},
			{"sess-2", wire.StatusRunning},
			{"sess-1", wire.StatusDone},
		}
		for i, w := range want {
			if tc.events[i].ThreadID != w.threadID || tc.events[i].Status != w.status {
				t.Errorf("events[%d] = {%q %q}, want {%q %q}", i, tc.events[i].ThreadID, tc.events[i].Status, w.threadID, w.status)
			}
		}
	})

	// --- Deduplication ---

	t.Run("does not emit duplicate status for non-tool events", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		// PostToolUse also maps to "running" and is not a tool-context event.
		a.HandleHook(hook("PostToolUse", "sess-1", "/tmp/myproject"))

		// Both are "running", neither is PreToolUse/PermissionRequest — second suppressed.
		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
	})

	t.Run("PreToolUse still emits even when status unchanged (carries tool description)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/foo.ts"`)}))

		// Both are "running", but PreToolUse carries tool context so it still emits.
		wantLen(t, tc.events, 2)
		if tc.events[0].ToolDescription != "" {
			t.Errorf("events[0].toolDescription = %q, want empty", tc.events[0].ToolDescription)
		}
		if tc.events[1].ToolDescription != "Reading foo.ts" {
			t.Errorf("events[1].toolDescription = %q, want %q", tc.events[1].ToolDescription, "Reading foo.ts")
		}
	})

	t.Run("emits when status changes", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		want := []string{wire.StatusRunning, wire.StatusDone, wire.StatusRunning}
		for i, w := range want {
			if tc.events[i].Status != w {
				t.Errorf("events[%d].status = %q, want %q", i, tc.events[i].Status, w)
			}
		}
	})

	// --- Tool descriptions ---

	t.Run("PreToolUse emits toolDescription for Read", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/home/user/project/src/config.ts"`)}))

		wantLen(t, tc.events, 1)
		if tc.events[0].ToolDescription != "Reading config.ts" {
			t.Errorf("toolDescription = %q, want %q", tc.events[0].ToolDescription, "Reading config.ts")
		}
	})

	t.Run("PreToolUse emits toolDescription for Bash", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Bash",
			map[string]json.RawMessage{"command": json.RawMessage(`"git status"`)}))

		wantLen(t, tc.events, 1)
		if tc.events[0].ToolDescription != "Running git status" {
			t.Errorf("toolDescription = %q, want %q", tc.events[0].ToolDescription, "Running git status")
		}
	})

	t.Run("PermissionRequest includes toolDescription", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PermissionRequest", "sess-1", "/tmp/myproject", "Bash",
			map[string]json.RawMessage{"command": json.RawMessage(`"git push origin main"`)}))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusWaiting {
			t.Errorf("status = %q, want waiting", tc.events[0].Status)
		}
		if tc.events[0].ToolDescription != "Running git push origin main" {
			t.Errorf("toolDescription = %q, want %q", tc.events[0].ToolDescription, "Running git push origin main")
		}
	})

	t.Run("consecutive PreToolUse events with same status still emit (new tool description)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/a.ts"`)}))
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Edit",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/b.ts"`)}))

		wantLen(t, tc.events, 2)
		if tc.events[0].ToolDescription != "Reading a.ts" {
			t.Errorf("events[0].toolDescription = %q, want %q", tc.events[0].ToolDescription, "Reading a.ts")
		}
		if tc.events[1].ToolDescription != "Editing b.ts" {
			t.Errorf("events[1].toolDescription = %q, want %q", tc.events[1].ToolDescription, "Editing b.ts")
		}
	})

	t.Run("UserPromptSubmit clears toolDescription", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/a.ts"`)}))
		a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		if tc.events[2].ToolDescription != "" {
			t.Errorf("events[2].toolDescription = %q, want empty", tc.events[2].ToolDescription)
		}
	})

	t.Run("Stop clears toolDescription", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Bash",
			map[string]json.RawMessage{"command": json.RawMessage(`"npm test"`)}))
		a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 2)
		if tc.events[1].ToolDescription != "" {
			t.Errorf("events[1].toolDescription = %q, want empty", tc.events[1].ToolDescription)
		}
	})

	// --- SessionStart ---

	t.Run("SessionStart emits idle", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("SessionStart", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		ev := tc.events[0]
		if ev.Status != wire.StatusIdle {
			t.Errorf("status = %q, want idle", ev.Status)
		}
		if ev.Session != "myproject" {
			t.Errorf("session = %q, want myproject", ev.Session)
		}
		if ev.ThreadID != "sess-1" {
			t.Errorf("threadId = %q, want sess-1", ev.ThreadID)
		}
	})

	t.Run("SessionStart followed by UserPromptSubmit transitions to running", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("SessionStart", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 2)
		if tc.events[0].Status != wire.StatusIdle {
			t.Errorf("events[0].status = %q, want idle", tc.events[0].Status)
		}
		if tc.events[1].Status != wire.StatusRunning {
			t.Errorf("events[1].status = %q, want running", tc.events[1].Status)
		}
	})

	// --- SessionEnd ---

	t.Run("SessionEnd emits done", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 2)
		if tc.events[1].Status != wire.StatusDone {
			t.Errorf("events[1].status = %q, want done", tc.events[1].Status)
		}
	})

	t.Run("SessionEnd cleans up thread state", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"))
		// New SessionStart for same session_id should create fresh state.
		a.HandleHook(hook("SessionStart", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		if tc.events[2].Status != wire.StatusIdle {
			t.Errorf("events[2].status = %q, want idle", tc.events[2].Status)
		}
	})

	t.Run("SessionEnd emits ended=true", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 2)
		if !tc.events[1].Ended {
			t.Errorf("events[1].ended = false, want true")
		}
	})

	t.Run("SessionEnd after Stop still emits (bypasses dedup)", func(t *testing.T) {
		// Regression: Stop sets status=done, then SessionEnd would be deduped
		// because status is unchanged. SessionEnd must bypass dedup so the
		// tracker receives the ended signal and removes the instance.
		a, tc := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("Stop", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("SessionEnd", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		if tc.events[2].Status != wire.StatusDone {
			t.Errorf("events[2].status = %q, want done", tc.events[2].Status)
		}
		if !tc.events[2].Ended {
			t.Errorf("events[2].ended = false, want true")
		}
	})
}

// describe("ClaudeCodeHookAdapter subagent enrichment")
func TestClaudeCodeHookAdapterSubagentEnrichment(t *testing.T) {
	setup := func(t *testing.T) (*Adapter, *testCtx, string) {
		sessionsDir := t.TempDir()
		a := New(t.TempDir(), sessionsDir)
		tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
		// These tests exercise subagent enrichment, not routing — once
		// refreshSubagent resolves a pid from the sessions file, later hooks
		// route by it, so make pid routing always resolve to the project session.
		tc.ctx.ResolveSessionByPid = func(int) string { return "myproject" }
		a.Start(&tc.ctx)
		t.Cleanup(a.Stop)
		return a, tc, sessionsDir
	}

	t.Run("emits subagent from sessions/<pid>.json when agent field is present", func(t *testing.T) {
		a, tc, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 42000, map[string]any{
			"pid": 42000, "sessionId": "sess-1",
			"procStart": "Sat May 16 09:00:00 2026", "agent": "rb-orchestrator",
		})

		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Subagent != "rb-orchestrator" {
			t.Errorf("subagent = %q, want rb-orchestrator", tc.events[0].Subagent)
		}
	})

	t.Run("omits subagent when sessions/<pid>.json lacks an agent field", func(t *testing.T) {
		a, tc, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 42001, map[string]any{
			"pid": 42001, "sessionId": "sess-1", "procStart": "Sat May 16 09:00:00 2026",
		})

		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Subagent != "" {
			t.Errorf("subagent = %q, want empty", tc.events[0].Subagent)
		}
	})

	t.Run("omits subagent when no sessions file matches the threadId", func(t *testing.T) {
		a, tc, _ := setup(t)
		a.HandleHook(hook("UserPromptSubmit", "sess-orphan", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Subagent != "" {
			t.Errorf("subagent = %q, want empty", tc.events[0].Subagent)
		}
	})

	t.Run("re-reads file across events so subagent transitions reflect", func(t *testing.T) {
		a, tc, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 42002, map[string]any{
			"pid": 42002, "sessionId": "sess-1",
			"procStart": "Sat May 16 09:00:00 2026", "agent": "rb-orchestrator",
		})

		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		if tc.events[0].Subagent != "rb-orchestrator" {
			t.Fatalf("events[0].subagent = %q, want rb-orchestrator", tc.events[0].Subagent)
		}

		// Subagent finishes — agent field cleared by CC.
		writeSessionFile(t, sessionsDir, 42002, map[string]any{
			"pid": 42002, "sessionId": "sess-1", "procStart": "Sat May 16 09:00:00 2026",
		})

		// Re-emission: a new tool description forces an emit.
		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/x.ts"`)}))

		wantLen(t, tc.events, 2)
		if tc.events[1].Subagent != "" {
			t.Errorf("events[1].subagent = %q, want empty", tc.events[1].Subagent)
		}
	})

	t.Run("detects PID reuse via sessionId mismatch", func(t *testing.T) {
		a, tc, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 42003, map[string]any{
			"pid": 42003, "sessionId": "sess-old",
			"procStart": "Sat May 16 09:00:00 2026", "agent": "rb-orchestrator",
		})

		a.HandleHook(hook("UserPromptSubmit", "sess-old", "/tmp/myproject"))
		if tc.events[0].Subagent != "rb-orchestrator" {
			t.Fatalf("events[0].subagent = %q, want rb-orchestrator", tc.events[0].Subagent)
		}

		// PID 42003 reused by a different CC process for sess-new.
		writeSessionFile(t, sessionsDir, 42003, map[string]any{
			"pid": 42003, "sessionId": "sess-new",
			"procStart": "Sat May 16 10:00:00 2026", "agent": "doc-writer",
		})

		a.HandleHook(hook("UserPromptSubmit", "sess-new", "/tmp/myproject"))

		wantLen(t, tc.events, 2)
		if tc.events[1].ThreadID != "sess-new" {
			t.Errorf("events[1].threadId = %q, want sess-new", tc.events[1].ThreadID)
		}
		if tc.events[1].Subagent != "doc-writer" {
			t.Errorf("events[1].subagent = %q, want doc-writer", tc.events[1].Subagent)
		}
	})

	t.Run("file read errors do not propagate (subagent stays undefined)", func(t *testing.T) {
		a, tc, _ := setup(t)
		// No file written — resolvePidFromSessions returns 0, read fails.
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Subagent != "" {
			t.Errorf("subagent = %q, want empty", tc.events[0].Subagent)
		}
	})

	t.Run("malformed sessions file does not throw", func(t *testing.T) {
		a, tc, sessionsDir := setup(t)
		if err := os.WriteFile(filepath.Join(sessionsDir, "42004.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Subagent != "" {
			t.Errorf("subagent = %q, want empty", tc.events[0].Subagent)
		}
	})

	t.Run("disappearance of sessions file mid-flight leaves prior subagent on emitted state intact via tracker", func(t *testing.T) {
		// (Watcher-level) re-emission with file gone should result in "".
		// The preservation behaviour lives in the tracker; this test asserts
		// the watcher contract: on next emit after file removal, subagent is "".
		a, tc, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 42005, map[string]any{
			"pid": 42005, "sessionId": "sess-1",
			"procStart": "Sat May 16 09:00:00 2026", "agent": "rb-orchestrator",
		})

		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject"))
		if tc.events[0].Subagent != "rb-orchestrator" {
			t.Fatalf("events[0].subagent = %q, want rb-orchestrator", tc.events[0].Subagent)
		}

		if err := os.Remove(filepath.Join(sessionsDir, "42005.json")); err != nil {
			t.Fatal(err)
		}

		a.HandleHook(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read",
			map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/x.ts"`)}))

		wantLen(t, tc.events, 2)
		if tc.events[1].Subagent != "" {
			t.Errorf("events[1].subagent = %q, want empty", tc.events[1].Subagent)
		}
	})
}

// describe("toolDescription")
func TestToolDescription(t *testing.T) {
	in := func(kv ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for i := 0; i+1 < len(kv); i += 2 {
			raw, _ := json.Marshal(kv[i+1])
			m[kv[i]] = raw
		}
		return m
	}

	cases := []struct {
		name  string
		tool  string
		input map[string]json.RawMessage
		want  string
	}{
		{"Read with file_path returns basename", "Read", in("file_path", "/home/user/project/src/config.ts"), "Reading config.ts"},
		{"Edit with file_path returns basename", "Edit", in("file_path", "/tmp/main.go"), "Editing main.go"},
		{"Write with file_path returns basename", "Write", in("file_path", "/tmp/out.json"), "Writing out.json"},
		{"Read without file_path returns verb only", "Read", in(), "Reading"},
		{"Bash with command returns truncated command", "Bash", in("command", "git status"), "Running git status"},
		// truncateToWidth reserves one cell for the ellipsis, so a 50-char
		// ASCII command with budget 30 yields 29 chars + "…" = 30 cells.
		{"Bash truncates long commands to 30 cells with ellipsis", "Bash", in("command", strings.Repeat("a", 50)), "Running " + strings.Repeat("a", 29) + "…"},
		{"Bash without command returns fallback", "Bash", in(), "Running command"},
		{"Glob with pattern", "Glob", in("pattern", "**/*.tsx"), "Searching **/*.tsx"},
		{"Grep with pattern", "Grep", in("pattern", "function main"), "Searching function main"},
		{"Agent with description", "Agent", in("description", "Explore codebase structure"), "Explore codebase structure"},
		{"Agent truncates long descriptions to 40 cells with ellipsis", "Agent", in("description", strings.Repeat("a", 60)), strings.Repeat("a", 39) + "…"},
		{"WebFetch returns static string", "WebFetch", in(), "Fetching URL"},
		{"WebSearch with query", "WebSearch", in("query", "bun test runner"), "Search: bun test runner"},
		{"AskUserQuestion with question", "AskUserQuestion", in("question", "Which framework do you prefer?"), "Question: Which framework do you prefer?"},
		{"unknown tool returns tool name", "TodoRead", in(), "TodoRead"},
		{"undefined tool_name returns undefined", "", in(), ""},
		{"undefined tool_input still works", "Bash", nil, "Running command"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToolDescription(c.tool, c.input); got != c.want {
				t.Errorf("ToolDescription(%q, ...) = %q, want %q", c.tool, got, c.want)
			}
		})
	}
}

// describe("ClaudeCodeHookAdapter — pid resolution")
func TestClaudeCodeHookAdapterPidResolution(t *testing.T) {
	setup := func(t *testing.T) (*Adapter, *testCtx) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
		// Pid is the routing channel; route any resolved claude pid to the project.
		tc.ctx.ResolveSessionByPid = func(int) string { return "myproject" }
		return newStartedAdapter(t, tc), tc
	}

	// Helper to build a process_snapshot where pid 400 (the hook) is a
	// descendant of pid 200 (the long-lived claude).
	snapshotWithClaudeAt200 := func() string {
		return strings.Join([]string{
			"  100     1 /sbin/launchd",
			"  200   100 node /Users/kyle/.nvm/versions/node/v20/lib/node_modules/@anthropic-ai/claude-code/cli.js",
			"  300   200 /bin/sh -c hook.sh PreToolUse",
			"  400   300 /bin/bash /Users/kyle/Code/meta-claude/tail-claude-mux/scripts/hook.sh PreToolUse",
		}, "\n")
	}

	t.Run("resolves wrapper-shell pid to the long-lived claude pid", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.PID = 400
		p.ProcessSnapshot = snapshotWithClaudeAt200()
		a.HandleHook(p)

		wantLen(t, tc.events, 1)
		if tc.events[0].PID != 200 {
			t.Errorf("pid = %d, want 200", tc.events[0].PID)
		}
	})

	t.Run("uses payload pid directly when it already matches claude in the snapshot", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.PID = 200
		p.ProcessSnapshot = snapshotWithClaudeAt200()
		a.HandleHook(p)

		wantLen(t, tc.events, 1)
		if tc.events[0].PID != 200 {
			t.Errorf("pid = %d, want 200", tc.events[0].PID)
		}
	})

	t.Run("drops pid when walker gives up and reported pid is not claude itself", func(t *testing.T) {
		a, tc := setup(t)
		// Walker can't reach claude in this snapshot.
		noClaude := strings.Join([]string{
			"  100     1 /sbin/launchd",
			"  200   100 /bin/bash",
			"  400   200 /bin/bash /path/hook.sh",
		}, "\n")
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.PID = 400
		p.ProcessSnapshot = noClaude
		a.HandleHook(p)

		// The wrapper pid would false-fire the liveness sweep, so we drop it.
		wantLen(t, tc.events, 1)
		if tc.events[0].PID != 0 {
			t.Errorf("pid = %d, want 0 (unresolved)", tc.events[0].PID)
		}
	})

	t.Run("subsequent hooks reuse the resolved pid (resolved once per thread)", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.PID = 400
		p.ProcessSnapshot = snapshotWithClaudeAt200()
		a.HandleHook(p)

		// Second hook with a totally different (e.g. stale) pid+snapshot should
		// not re-resolve — pid is per-thread, captured once.
		p2 := toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Bash",
			map[string]json.RawMessage{"command": json.RawMessage(`"ls"`)})
		p2.PID = 999
		p2.ProcessSnapshot = ""
		a.HandleHook(p2)

		last := tc.events[len(tc.events)-1]
		if last.PID != 200 {
			t.Errorf("pid = %d, want 200", last.PID)
		}
	})

	t.Run("works without pid/process_snapshot (legacy payloads)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("SessionStart", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].PID != 0 {
			t.Errorf("pid = %d, want 0 (absent)", tc.events[0].PID)
		}
	})
}

// describe("ClaudeCodeHookAdapter — pid-first session routing")
func TestClaudeCodeHookAdapterPidFirstSessionRouting(t *testing.T) {
	snapshotClaudeAt200 := func() string {
		return strings.Join([]string{
			"  100     1 /sbin/launchd",
			"  200   100 node /path/@anthropic-ai/claude-code/cli.js",
			"  400   200 /bin/sh -c hook.sh PreToolUse",
		}, "\n")
	}

	t.Run("routes by resolved pid, not cwd, when a pid is available", func(t *testing.T) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "cwd-session"}, map[int]string{200: "pid-session"})
		a := newStartedAdapter(t, tc)
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.PID = 400
		p.ProcessSnapshot = snapshotClaudeAt200()
		a.HandleHook(p)

		wantLen(t, tc.events, 1)
		if tc.events[0].Session != "pid-session" { // pid wins over cwd
			t.Errorf("session = %q, want pid-session", tc.events[0].Session)
		}
	})

	t.Run("drops the event when pid resolves but the pane lookup fails", func(t *testing.T) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "cwd-session"}, nil) // empty pidMap
		a := newStartedAdapter(t, tc)
		p := hook("SessionStart", "sess-1", "/tmp/myproject")
		p.PID = 400
		p.ProcessSnapshot = snapshotClaudeAt200()
		a.HandleHook(p)

		wantLen(t, tc.events, 0) // no silent cwd fallback
	})

	t.Run("falls back to cwd routing when no pid is resolved", func(t *testing.T) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "cwd-session"}, nil)
		a := newStartedAdapter(t, tc)
		a.HandleHook(hook("SessionStart", "sess-1", "/tmp/myproject")) // no pid

		wantLen(t, tc.events, 1)
		if tc.events[0].Session != "cwd-session" {
			t.Errorf("session = %q, want cwd-session", tc.events[0].Session)
		}
	})
}

// describe("ClaudeCodeHookAdapter — StopFailure")
func TestClaudeCodeHookAdapterStopFailure(t *testing.T) {
	t.Run("StopFailure maps to error, clearing the running spinner", func(t *testing.T) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
		a := newStartedAdapter(t, tc)
		a.HandleHook(hook("UserPromptSubmit", "sess-1", "/tmp/myproject")) // → running
		a.HandleHook(hook("StopFailure", "sess-1", "/tmp/myproject"))

		last := tc.events[len(tc.events)-1]
		if last.Status != wire.StatusError {
			t.Errorf("status = %q, want error", last.Status)
		}
	})
}

// describe("classifySessionStatus")
func TestClassifySessionStatus(t *testing.T) {
	const now = int64(1_000_000_000_000)
	const hung = int64(30 * 60 * 1000)

	cases := []struct {
		name string
		file *registry.Live
		want tracker.ProbeVerdict
	}{
		{"absent file → null (no signal)", nil, tracker.ProbeNoSignal},
		{"status absent (sdk-cli) → null", &registry.Live{SessionID: "t"}, tracker.ProbeNoSignal},
		{"busy + fresh updatedAt → working",
			&registry.Live{SessionID: "t", Status: "busy", UpdatedAt: registry.EpochMS(now - 1000)}, tracker.ProbeWorking},
		{"busy + updatedAt older than hung ceiling → ended",
			&registry.Live{SessionID: "t", Status: "busy", UpdatedAt: registry.EpochMS(now - hung - 1)}, tracker.ProbeEnded},
		{"busy with no updatedAt → working (no staleness evidence)",
			&registry.Live{SessionID: "t", Status: "busy"}, tracker.ProbeWorking},
		{"sessionId mismatch (pid reused) → ended even if busy",
			&registry.Live{SessionID: "other", Status: "busy", UpdatedAt: registry.EpochMS(now)}, tracker.ProbeEnded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifySessionStatus(c.file, "t", now); got != c.want {
				t.Errorf("ClassifySessionStatus = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("idle / waiting → ended", func(t *testing.T) {
		for _, status := range []string{"idle", "waiting"} {
			file := &registry.Live{SessionID: "t", Status: status}
			if got := ClassifySessionStatus(file, "t", now); got != tracker.ProbeEnded {
				t.Errorf("ClassifySessionStatus(status=%q) = %v, want ProbeEnded", status, got)
			}
		}
	})
}

// describe("ClaudeCodeHookAdapter.probeLiveStatus")
func TestClaudeCodeHookAdapterProbeLiveStatus(t *testing.T) {
	setup := func(t *testing.T) (*Adapter, string) {
		sessionsDir := t.TempDir()
		a := New(t.TempDir(), sessionsDir)
		t.Cleanup(a.Stop)
		return a, sessionsDir
	}
	nowMS := func() int64 { return time.Now().UnixMilli() }

	t.Run("reads sessions/<pid>.json and classifies an idle session as ended", func(t *testing.T) {
		a, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 555, map[string]any{
			"pid": 555, "sessionId": "t-1", "status": "idle", "updatedAt": nowMS(),
		})
		if got := a.ProbeLiveStatus(555, "t-1", ""); got != tracker.ProbeEnded {
			t.Errorf("verdict = %v, want ProbeEnded", got)
		}
	})

	t.Run("classifies a busy fresh session as working", func(t *testing.T) {
		a, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 556, map[string]any{
			"pid": 556, "sessionId": "t-2", "status": "busy", "updatedAt": nowMS(),
		})
		if got := a.ProbeLiveStatus(556, "t-2", ""); got != tracker.ProbeWorking {
			t.Errorf("verdict = %v, want ProbeWorking", got)
		}
	})

	t.Run("missing file → null", func(t *testing.T) {
		a, _ := setup(t)
		if got := a.ProbeLiveStatus(999, "t-3", ""); got != tracker.ProbeNoSignal {
			t.Errorf("verdict = %v, want ProbeNoSignal", got)
		}
	})

	// OSC-title cross-check: the session file is authoritative; the pane title
	// only fills the gap when the file yields no verdict (sdk-cli / absent).
	t.Run("file null + braille title → working (title fills the gap)", func(t *testing.T) {
		a, _ := setup(t)
		// No session file written for this pid → file verdict is null.
		if got := a.ProbeLiveStatus(701, "t-osc-1", "⠋ Reading config.ts"); got != tracker.ProbeWorking {
			t.Errorf("verdict = %v, want ProbeWorking", got)
		}
	})

	t.Run("file null + sparkle title → ended (title fills the gap)", func(t *testing.T) {
		a, _ := setup(t)
		if got := a.ProbeLiveStatus(702, "t-osc-2", "✳ ~/Code/project"); got != tracker.ProbeEnded {
			t.Errorf("verdict = %v, want ProbeEnded", got)
		}
	})

	t.Run("file null + plain title → null (no signal from either source)", func(t *testing.T) {
		a, _ := setup(t)
		if got := a.ProbeLiveStatus(703, "t-osc-3", "~/Code/project"); got != tracker.ProbeNoSignal {
			t.Errorf("verdict = %v, want ProbeNoSignal", got)
		}
	})

	t.Run("file busy wins over a sparkle title — definitive file verdict is never overridden", func(t *testing.T) {
		a, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 704, map[string]any{
			"pid": 704, "sessionId": "t-osc-4", "status": "busy", "updatedAt": nowMS(),
		})
		if got := a.ProbeLiveStatus(704, "t-osc-4", "✳ idle-looking title"); got != tracker.ProbeWorking {
			t.Errorf("verdict = %v, want ProbeWorking", got)
		}
	})

	t.Run("file idle wins over a braille title — file ended is never overridden to working", func(t *testing.T) {
		a, sessionsDir := setup(t)
		writeSessionFile(t, sessionsDir, 705, map[string]any{
			"pid": 705, "sessionId": "t-osc-5", "status": "idle", "updatedAt": nowMS(),
		})
		if got := a.ProbeLiveStatus(705, "t-osc-5", "⠋ busy-looking title"); got != tracker.ProbeEnded {
			t.Errorf("verdict = %v, want ProbeEnded", got)
		}
	})
}

// describe("ClaudeCodeHookAdapter — cold-start seed routes by pid")
func TestClaudeCodeHookAdapterColdStartSeedRoutesByPid(t *testing.T) {
	t.Run("seeded running entry routes by pid (not cwd) and carries the resolved pid", func(t *testing.T) {
		projectsDir := t.TempDir()
		sessionsDir := t.TempDir()
		a := New(projectsDir, sessionsDir)
		t.Cleanup(a.Stop)

		const threadID = "seed-thread-1"
		// A project dir holding one running conversation transcript.
		projDir := filepath.Join(projectsDir, "-tmp-myproject")
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"message":{"role":"user","content":[{"type":"text","text":"do the thing"}]}}` + "\n"
		if err := os.WriteFile(filepath.Join(projDir, threadID+".jsonl"), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		// sessions/<pid>.json lets the seed resolve the long-lived pid from threadID.
		writeSessionFile(t, sessionsDir, 4242, map[string]any{"pid": 4242, "sessionId": threadID})

		// cwd resolves to one session, pid to another — pid must win.
		tc := makeCtx(nil, map[int]string{4242: "pid-session"})
		tc.ctx.ResolveSession = func(string) string { return "cwd-session" }
		// The Go seed runs synchronously inside Start — no settling wait needed.
		a.Start(&tc.ctx)

		var seeded *wire.AgentEvent
		for i := range tc.events {
			if tc.events[i].ThreadID == threadID {
				seeded = &tc.events[i]
				break
			}
		}
		if seeded == nil {
			t.Fatalf("no seeded event for thread %q: %+v", threadID, tc.events)
		}
		if seeded.Session != "pid-session" {
			t.Errorf("session = %q, want pid-session", seeded.Session)
		}
		if seeded.PID != 4242 {
			t.Errorf("pid = %d, want 4242", seeded.PID)
		}
	})
}

// describe("classifyTitleStatus") — ported from title-status.test.ts.
func TestClassifyTitleStatus(t *testing.T) {
	t.Run("leading braille spinner glyph → working", func(t *testing.T) {
		for _, title := range []string{"⠀ doing work", "⠋ Reading config.ts", "⣿ tail of the braille range"} {
			if got := ClassifyTitleStatus(title); got != tracker.ProbeWorking {
				t.Errorf("ClassifyTitleStatus(%q) = %v, want ProbeWorking", title, got)
			}
		}
	})

	t.Run("leading sparkle ✳ → ended", func(t *testing.T) {
		for _, title := range []string{"✳ ~/Code/project", "✳ idle at prompt"} {
			if got := ClassifyTitleStatus(title); got != tracker.ProbeEnded {
				t.Errorf("ClassifyTitleStatus(%q) = %v, want ProbeEnded", title, got)
			}
		}
	})

	t.Run("plain title → null (no signal)", func(t *testing.T) {
		for _, title := range []string{"~/Code/meta-claude", "zsh", "claude — main"} {
			if got := ClassifyTitleStatus(title); got != tracker.ProbeNoSignal {
				t.Errorf("ClassifyTitleStatus(%q) = %v, want ProbeNoSignal", title, got)
			}
		}
	})

	t.Run("empty title → null", func(t *testing.T) {
		if got := ClassifyTitleStatus(""); got != tracker.ProbeNoSignal {
			t.Errorf("ClassifyTitleStatus(\"\") = %v, want ProbeNoSignal", got)
		}
	})

	t.Run("glyph not at the leading position → null", func(t *testing.T) {
		// A braille char mid-string is not Claude's state marker.
		if got := ClassifyTitleStatus("project ⠋"); got != tracker.ProbeNoSignal {
			t.Errorf("ClassifyTitleStatus(%q) = %v, want ProbeNoSignal", "project ⠋", got)
		}
	})

	t.Run("near-range glyphs that are not braille/sparkle → null", func(t *testing.T) {
		// U+27FF is just below the braille block; U+2734 is just past the sparkle.
		for _, title := range []string{"⟿ x", "✴ x"} {
			if got := ClassifyTitleStatus(title); got != tracker.ProbeNoSignal {
				t.Errorf("ClassifyTitleStatus(%q) = %v, want ProbeNoSignal", title, got)
			}
		}
	})
}
