// Port of packages/runtime/test/pi-hooks.test.ts. Subtest names keep the
// TS test names verbatim, grouped by describe block.
package piwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

// hook builds a pi HookPayload. Agent defaults to "pi" so tests stay concise.
func hook(event, sessionID, cwd string) wire.HookPayload {
	return wire.HookPayload{Agent: "pi", Event: event, SessionID: sessionID, Cwd: cwd}
}

func toolHook(event, sessionID, cwd, toolName string, toolInput map[string]json.RawMessage) wire.HookPayload {
	p := hook(event, sessionID, cwd)
	p.ToolName = toolName
	p.ToolInput = toolInput
	return p
}

func stopHook(event, sessionID, cwd, stopReason string) wire.HookPayload {
	p := hook(event, sessionID, cwd)
	p.StopReason = stopReason
	return p
}

// in builds a tool_input map from key/value string pairs.
func in(kv ...string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		raw, _ := json.Marshal(kv[i+1])
		m[kv[i]] = raw
	}
	return m
}

// newStartedAdapter builds an Adapter over an empty temp sessions dir and
// starts it — the equivalent of the TS beforeEach that points the adapter at
// a nonexistent dir so the cold-start seed is a no-op.
func newStartedAdapter(t *testing.T, tc *testCtx) *Adapter {
	t.Helper()
	a := New(t.TempDir())
	a.Start(&tc.ctx)
	return a
}

func wantLen(t *testing.T, events []wire.AgentEvent, n int) {
	t.Helper()
	if len(events) != n {
		t.Fatalf("got %d events, want %d: %+v", len(events), n, events)
	}
}

// describe("PiHookAdapter")
func TestPiHookAdapter(t *testing.T) {
	setup := func(t *testing.T) (*Adapter, *testCtx) {
		tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
		return newStartedAdapter(t, tc), tc
	}

	// TS "implements HookReceiver" is not ported: it duck-type-checks bun's
	// structural HookReceiver interface, which has no Go counterpart — the
	// server calls HandleHook on the concrete *Adapter, so the signature is
	// already compile-checked.

	t.Run("has name 'pi'", func(t *testing.T) {
		a, _ := setup(t)
		if got := a.Name(); got != "pi" {
			t.Fatalf("Name() = %q, want %q", got, "pi")
		}
	})

	// --- Agent discriminator ---

	t.Run("payload with agent: 'claude-code' is ignored", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("SessionStart", "sess-cc-1", "/tmp/myproject")
		p.Agent = "claude-code"
		a.HandleHook(p)

		wantLen(t, tc.events, 0)
	})

	t.Run("payload without agent field is ignored (default routes to Claude Code)", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("session_start", "sess-1", "/tmp/myproject")
		p.Agent = ""
		a.HandleHook(p)

		wantLen(t, tc.events, 0)
	})

	// --- session_start ---

	t.Run("session_start emits idle", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("session_start", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		ev := tc.events[0]
		if ev.Agent != "pi" {
			t.Errorf("agent = %q, want pi", ev.Agent)
		}
		if ev.Session != "myproject" {
			t.Errorf("session = %q, want myproject", ev.Session)
		}
		if ev.Status != wire.StatusIdle {
			t.Errorf("status = %q, want idle", ev.Status)
		}
		if ev.ThreadID != "sess-1" {
			t.Errorf("threadId = %q, want sess-1", ev.ThreadID)
		}
	})

	t.Run("session_start propagates session_name as threadName", func(t *testing.T) {
		a, tc := setup(t)
		p := hook("session_start", "sess-1", "/tmp/myproject")
		p.SessionName = "Refactor auth"
		a.HandleHook(p)

		wantLen(t, tc.events, 1)
		if tc.events[0].ThreadName != "Refactor auth" {
			t.Errorf("threadName = %q, want %q", tc.events[0].ThreadName, "Refactor auth")
		}
	})

	// --- agent_start / agent_end ---

	t.Run("agent_start emits running with no toolDescription", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
		if tc.events[0].ToolDescription != "" {
			t.Errorf("toolDescription = %q, want empty", tc.events[0].ToolDescription)
		}
	})

	t.Run("agent_end stop_reason 'stop' emits done", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		a.HandleHook(stopHook("agent_end", "sess-1", "/tmp/myproject", "stop"))

		wantLen(t, tc.events, 2)
		if tc.events[1].Status != wire.StatusDone {
			t.Errorf("status = %q, want done", tc.events[1].Status)
		}
	})

	t.Run("agent_end stop_reason 'length' emits done", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		a.HandleHook(stopHook("agent_end", "sess-1", "/tmp/myproject", "length"))

		if tc.events[1].Status != wire.StatusDone {
			t.Errorf("status = %q, want done", tc.events[1].Status)
		}
	})

	t.Run("agent_end stop_reason 'toolUse' emits done (turn boundary)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		a.HandleHook(stopHook("agent_end", "sess-1", "/tmp/myproject", "toolUse"))

		if tc.events[1].Status != wire.StatusDone {
			t.Errorf("status = %q, want done", tc.events[1].Status)
		}
	})

	t.Run("agent_end stop_reason 'aborted' emits interrupted", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		a.HandleHook(stopHook("agent_end", "sess-1", "/tmp/myproject", "aborted"))

		if tc.events[1].Status != wire.StatusInterrupted {
			t.Errorf("status = %q, want interrupted", tc.events[1].Status)
		}
		if tc.events[1].ToolDescription != "" {
			t.Errorf("toolDescription = %q, want empty", tc.events[1].ToolDescription)
		}
	})

	t.Run("agent_end stop_reason 'error' emits error with truncated error_message", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		p := stopHook("agent_end", "sess-1", "/tmp/myproject", "error")
		p.ErrorMessage = "boom: provider returned 500"
		a.HandleHook(p)

		if tc.events[1].Status != wire.StatusError {
			t.Errorf("status = %q, want error", tc.events[1].Status)
		}
		if tc.events[1].ToolDescription != "boom: provider returned 500" {
			t.Errorf("toolDescription = %q, want %q", tc.events[1].ToolDescription, "boom: provider returned 500")
		}
	})

	t.Run("agent_end stop_reason 'error' truncates long error_message to 80 chars", func(t *testing.T) {
		a, tc := setup(t)
		long := strings.Repeat("oops ", 40) // 200 chars
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		p := stopHook("agent_end", "sess-1", "/tmp/myproject", "error")
		p.ErrorMessage = long
		a.HandleHook(p)

		desc := tc.events[1].ToolDescription
		if utf8.RuneCountInString(desc) != 80 {
			t.Errorf("len(desc) = %d, want 80: %q", utf8.RuneCountInString(desc), desc)
		}
		if !strings.HasSuffix(desc, "…") {
			t.Errorf("desc = %q, want ellipsis suffix", desc)
		}
	})

	// --- tool_execution_start / tool_execution_end ---

	t.Run("tool_execution_start for read emits 'Reading <basename>'", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "read",
			in("path", "/tmp/myproject/x.ts")))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
		if tc.events[0].ToolDescription != "Reading x.ts" {
			t.Errorf("toolDescription = %q, want %q", tc.events[0].ToolDescription, "Reading x.ts")
		}
	})

	t.Run("tool_execution_start for bash emits 'Running <command>'", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "bash",
			in("command", "git status")))

		wantLen(t, tc.events, 1)
		if tc.events[0].ToolDescription != "Running git status" {
			t.Errorf("toolDescription = %q, want %q", tc.events[0].ToolDescription, "Running git status")
		}
	})

	t.Run("tool_execution_end with tool_is_error keeps status running and description", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "bash",
			in("command", "npm test")))
		isErr := true
		p := toolHook("tool_execution_end", "sess-1", "/tmp/myproject", "bash", nil)
		p.ToolIsError = &isErr
		a.HandleHook(p)

		// Both running, same description — dedup suppresses the second emission.
		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
		if tc.events[0].ToolDescription != "Running npm test" {
			t.Errorf("toolDescription = %q, want %q", tc.events[0].ToolDescription, "Running npm test")
		}
	})

	t.Run("consecutive tool_execution_start events with new descriptions both emit", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "read",
			in("path", "/tmp/a.ts")))
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "edit",
			in("path", "/tmp/b.ts")))

		wantLen(t, tc.events, 2)
		if tc.events[0].ToolDescription != "Reading a.ts" {
			t.Errorf("events[0].toolDescription = %q, want %q", tc.events[0].ToolDescription, "Reading a.ts")
		}
		if tc.events[1].ToolDescription != "Editing b.ts" {
			t.Errorf("events[1].toolDescription = %q, want %q", tc.events[1].ToolDescription, "Editing b.ts")
		}
	})

	t.Run("consecutive identical tool_execution_start events both emit with ToolInvoked", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "read",
			in("path", "/tmp/a.ts")))
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "read",
			in("path", "/tmp/a.ts")))

		wantLen(t, tc.events, 2)
		for i, ev := range tc.events {
			if !ev.ToolInvoked {
				t.Errorf("events[%d].toolInvoked = false, want true", i)
			}
		}
	})

	t.Run("tool_execution_end does not mark ToolInvoked", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "bash",
			in("command", "npm test")))
		a.HandleHook(hook("agent_start", "sess-2", "/tmp/myproject")) // unrelated thread, breaks nothing
		p := toolHook("tool_execution_end", "sess-1", "/tmp/myproject", "bash", nil)
		a.HandleHook(p)

		// start emits (invoked), sess-2 agent_start emits, end dedups away
		// (running→running, description kept, not an invocation).
		wantLen(t, tc.events, 2)
		if !tc.events[0].ToolInvoked {
			t.Errorf("tool_execution_start toolInvoked = false, want true")
		}
		if tc.events[1].ToolInvoked {
			t.Errorf("agent_start toolInvoked = true, want false")
		}
	})

	t.Run("agent_start after tool_execution_start clears toolDescription", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(toolHook("tool_execution_start", "sess-1", "/tmp/myproject", "read",
			in("path", "/tmp/a.ts")))
		a.HandleHook(stopHook("agent_end", "sess-1", "/tmp/myproject", "stop"))
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		if tc.events[2].ToolDescription != "" {
			t.Errorf("events[2].toolDescription = %q, want empty", tc.events[2].ToolDescription)
		}
	})

	// --- session_shutdown ---

	t.Run("session_shutdown emits done with ended: true and drops state", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		p := hook("session_shutdown", "sess-1", "/tmp/myproject")
		p.ShutdownReason = "quit"
		a.HandleHook(p)

		wantLen(t, tc.events, 2)
		if tc.events[1].Status != wire.StatusDone {
			t.Errorf("status = %q, want done", tc.events[1].Status)
		}
		if !tc.events[1].Ended {
			t.Errorf("ended = false, want true")
		}

		// A new session_start for the same UUID should be treated as a fresh thread.
		a.HandleHook(hook("session_start", "sess-1", "/tmp/myproject"))
		wantLen(t, tc.events, 3)
		if tc.events[2].Status != wire.StatusIdle {
			t.Errorf("events[2].status = %q, want idle", tc.events[2].Status)
		}
	})

	t.Run("session_shutdown after agent_end still emits (bypasses dedup)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		a.HandleHook(stopHook("agent_end", "sess-1", "/tmp/myproject", "stop"))
		a.HandleHook(hook("session_shutdown", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		if tc.events[2].Status != wire.StatusDone {
			t.Errorf("events[2].status = %q, want done", tc.events[2].Status)
		}
		if !tc.events[2].Ended {
			t.Errorf("events[2].ended = false, want true")
		}
	})

	// --- Multiple threads ---

	t.Run("two concurrent session_ids in same cwd track separately", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("session_start", "sess-a", "/tmp/myproject"))
		a.HandleHook(hook("session_start", "sess-b", "/tmp/myproject"))
		a.HandleHook(hook("agent_start", "sess-a", "/tmp/myproject"))

		wantLen(t, tc.events, 3)
		want := []struct{ threadID, status string }{
			{"sess-a", wire.StatusIdle},
			{"sess-b", wire.StatusIdle},
			{"sess-a", wire.StatusRunning},
		}
		for i, w := range want {
			if tc.events[i].ThreadID != w.threadID || tc.events[i].Status != w.status {
				t.Errorf("events[%d] = {%q %q}, want {%q %q}", i, tc.events[i].ThreadID, tc.events[i].Status, w.threadID, w.status)
			}
		}
	})

	// --- Deduplication ---

	t.Run("consecutive agent_start events for same thread emit once", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))
		a.HandleHook(hook("agent_start", "sess-1", "/tmp/myproject"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
	})

	// --- Routing: pid-first, cwd-fallback ---
	//
	// Live hook routing prefers pid because pi sends its long-lived
	// process.pid in every payload and the answer is independent of the
	// active pane's cwd. Cwd is the fallback only when pid is absent (older
	// watchers / malformed payloads); when pid is present and pid lookup
	// fails, the event is dropped rather than silently re-routed.

	t.Run("hook with pid routes via pid — ignores cwd map", func(t *testing.T) {
		// Pid-only ctx: cwd map is empty, so a cwd-based attempt would drop the hook.
		tc := makeCtx(nil, map[int]string{89555: "pi-dev"})
		a := newStartedAdapter(t, tc)
		p := hook("agent_start", "sess-pid-1", "/unrelated/path")
		p.PID = 89555
		a.HandleHook(p)

		wantLen(t, tc.events, 1)
		ev := tc.events[0]
		if ev.Session != "pi-dev" {
			t.Errorf("session = %q, want pi-dev", ev.Session)
		}
		if ev.ThreadID != "sess-pid-1" {
			t.Errorf("threadId = %q, want sess-pid-1", ev.ThreadID)
		}
		if ev.Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", ev.Status)
		}
		if ev.PID != 89555 {
			t.Errorf("pid = %d, want 89555", ev.PID)
		}
	})

	t.Run("hook with pid is dropped when pid lookup fails — no cwd fallback", func(t *testing.T) {
		// The whole point: if pid is provided and we can't resolve it, that is
		// a routing failure. We refuse to silently fall through to cwd — doing
		// so would mask future pid-resolution regressions.
		tc := makeCtx(
			map[string]string{"/Users/kyle/Code/my-projects/kylesnowschwartz.github.io": "pi-dev"},
			nil, // no pid mapping — lookup will fail
		)
		a := newStartedAdapter(t, tc)
		p := hook("agent_start", "sess-pid-fail-1",
			"/Users/kyle/Code/my-projects/kylesnowschwartz.github.io") // would resolve via cwd
		p.PID = 89555
		a.HandleHook(p)

		wantLen(t, tc.events, 0)
	})

	t.Run("hook without pid falls through to cwd — backward compat for malformed payloads", func(t *testing.T) {
		// Pi extension always sends pid, but the contract allows missing pid
		// for forward compat with watchers that don't carry it. In that case
		// there is no pid path to fail — cwd is the only available channel.
		tc := makeCtx(map[string]string{"/tmp/legacy-project": "legacy"}, nil)
		a := newStartedAdapter(t, tc)
		a.HandleHook(hook("agent_start", "sess-cwd-1", "/tmp/legacy-project"))

		wantLen(t, tc.events, 1)
		if tc.events[0].Session != "legacy" {
			t.Errorf("session = %q, want legacy", tc.events[0].Session)
		}
		if tc.events[0].ThreadID != "sess-cwd-1" {
			t.Errorf("threadId = %q, want sess-cwd-1", tc.events[0].ThreadID)
		}
		if tc.events[0].Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", tc.events[0].Status)
		}
	})

	// --- Unresolved session (cwd path, no pid) ---

	t.Run("unresolved cwd emits nothing", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("session_start", "sess-1", "/tmp/unknown-project"))

		wantLen(t, tc.events, 0)
	})

	// --- Unknown event ---

	t.Run("unknown event emits nothing (after creating thread)", func(t *testing.T) {
		a, tc := setup(t)
		a.HandleHook(hook("some_future_event", "sess-1", "/tmp/myproject"))

		// The adapter registers the thread but the event itself is ignored,
		// so no emission goes through.
		wantLen(t, tc.events, 0)
	})
}

// describe("PiHookAdapter — JSONL cold-start seed")
func TestPiHookAdapterJSONLColdStartSeed(t *testing.T) {
	// writeSession lays out sessionsDir/--project--/<timestamp>_<uuid>.jsonl.
	// Pi encodes project dirs as `--<path-with-slashes-as-dashes>--`, but the
	// seed no longer depends on the folder name — it reads SessionHeader.cwd
	// directly. Any per-project directory name works here. The just-written
	// mtime is fresh, so the seed considers the file current.
	writeSession := func(t *testing.T, sessionsDir, uuid string, lines ...string) {
		t.Helper()
		dir := filepath.Join(sessionsDir, "--project--")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "2026-01-01T00-00-00-000Z_"+uuid+".jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	header := func(uuid, cwd string) string {
		return fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"2026-01-01T00:00:00Z","cwd":%q}`, uuid, cwd)
	}
	setup := func(t *testing.T) (sessionsDir, projectDir string) {
		return t.TempDir(), t.TempDir()
	}

	t.Run("seeds a running pi session without any hook", func(t *testing.T) {
		sessionsDir, projectDir := setup(t)
		writeSession(t, sessionsDir, "uuid-1",
			header("uuid-1", projectDir),
			`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"Hello, please help me"}}`,
			`{"type":"message","id":"b","parentId":"a","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"Sure"}],"stopReason":"toolUse"}}`,
		)

		a := New(sessionsDir)
		tc := makeCtx(map[string]string{projectDir: "myproject"}, nil)
		// The Go seed runs synchronously inside Start — no settling wait needed.
		a.Start(&tc.ctx)

		var seeded *wire.AgentEvent
		for i := range tc.events {
			if tc.events[i].ThreadID == "uuid-1" {
				seeded = &tc.events[i]
				break
			}
		}
		if seeded == nil {
			t.Fatalf("no seeded event for uuid-1: %+v", tc.events)
		}
		if seeded.Status != wire.StatusRunning {
			t.Errorf("status = %q, want running", seeded.Status)
		}
		if seeded.ThreadName != "Hello, please help me" {
			t.Errorf("threadName = %q, want %q", seeded.ThreadName, "Hello, please help me")
		}
	})

	t.Run("does not re-seed a thread already populated by a hook", func(t *testing.T) {
		// TS raced an async seed against an early hook; the Go seed runs
		// synchronously in Start, so the seed lands first and the later hook
		// dedups against it. The assertions are the same: one running row for
		// the thread and no idle leak.
		sessionsDir, projectDir := setup(t)
		writeSession(t, sessionsDir, "uuid-2",
			header("uuid-2", projectDir),
			`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"First message"}}`,
			`{"type":"message","id":"b","parentId":"a","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"toolUse"}}`,
		)

		a := New(sessionsDir)
		tc := makeCtx(map[string]string{projectDir: "myproject"}, nil)
		a.Start(&tc.ctx)
		a.HandleHook(hook("agent_start", "uuid-2", projectDir))

		running := 0
		idle := 0
		for _, ev := range tc.events {
			if ev.ThreadID != "uuid-2" {
				continue
			}
			switch ev.Status {
			case wire.StatusRunning:
				running++
			case wire.StatusIdle:
				idle++
			}
		}
		if running < 1 {
			t.Errorf("running events = %d, want >= 1", running)
		}
		if idle != 0 {
			t.Errorf("idle events = %d, want 0", idle)
		}
	})

	t.Run("skips sessions whose trailing status is terminal", func(t *testing.T) {
		sessionsDir, projectDir := setup(t)
		writeSession(t, sessionsDir, "uuid-3",
			header("uuid-3", projectDir),
			`{"type":"message","id":"a","parentId":null,"timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"Done already"}}`,
			`{"type":"message","id":"b","parentId":"a","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"All done"}],"stopReason":"stop"}}`,
		)

		a := New(sessionsDir)
		tc := makeCtx(map[string]string{projectDir: "myproject"}, nil)
		a.Start(&tc.ctx)

		for _, ev := range tc.events {
			if ev.ThreadID == "uuid-3" {
				t.Fatalf("unexpected seeded event: %+v", ev)
			}
		}
	})
}

// describe("piToolDescription")
func TestToolDescription(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input map[string]json.RawMessage
		want  string
	}{
		{"read with path returns basename", "read", in("path", "/home/kyle/project/src/config.ts"), "Reading config.ts"},
		{"edit with path returns basename", "edit", in("path", "/tmp/main.go"), "Editing main.go"},
		{"write with path returns basename", "write", in("path", "/tmp/out.json"), "Writing out.json"},
		{"ls with path returns basename", "ls", in("path", "/tmp/dir"), "Listing dir"},
		{"read without path returns verb only", "read", in(), "Reading"},
		// truncateToWidth reserves one cell for the ellipsis, so a 50-char
		// ASCII command with budget 30 yields 29 chars + "…" = 30 cells.
		{"bash truncates long commands to 30 cells with ellipsis", "bash", in("command", strings.Repeat("a", 50)), "Running " + strings.Repeat("a", 29) + "…"},
		{"bash without command returns fallback", "bash", in(), "Running command"},
		{"grep shares the searching shape", "grep", in("pattern", "TODO"), "Searching TODO"},
		{"glob shares the searching shape", "glob", in("pattern", "**/*.ts"), "Searching **/*.ts"},
		{"find shares the searching shape", "find", in("pattern", "*.md"), "Searching *.md"},
		{"agent truncates long descriptions to 40 cells with ellipsis", "agent", in("description", strings.Repeat("a", 60)), strings.Repeat("a", 39) + "…"},
		{"web_fetch returns static string", "web_fetch", in(), "Fetching URL"},
		{"web_search with query", "web_search", in("query", "bun docs"), "Search: bun docs"},
		{"ask_user_question with question", "ask_user_question", in("question", "Which theme?"), "Question: Which theme?"},
		{"todo_write returns static string", "todo_write", in(), "Updating todos"},
		{"unknown tool returns its name", "my_custom_tool", in(), "my_custom_tool"},
		{"undefined tool_name returns undefined", "", in(), ""},
		{"undefined tool_input still works", "bash", nil, "Running command"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ToolDescription(c.tool, c.input); got != c.want {
				t.Errorf("ToolDescription(%q, ...) = %q, want %q", c.tool, got, c.want)
			}
		})
	}
}
