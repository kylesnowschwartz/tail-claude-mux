package codexwatch

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// daemonHookPayload is a hook sent by the shared codex app-server, a codex
// process in no tmux pane.
func daemonHookPayload(threadID, event string) wire.HookPayload {
	return wire.HookPayload{
		Agent: "codex", Event: event, SessionID: threadID, Cwd: "/project",
		PID: 900, ProcessSnapshot: "900 52083 /bin/sh hook\n52083 1 /usr/local/bin/codex app-server --listen unix:// --managed-daemon",
	}
}

func TestDaemonHookRoutesToPaneLinkedToThread(t *testing.T) {
	const threadID = "12345678-1234-1234-1234-123456789abc"
	const panePID = 4100
	h := newHarness(t)
	writeRollout(t, h.adapter.SessionsDir, threadID, `"cli"`, "")
	h.adapter.openFilesForPID = func(int) []string { return nil }
	tr := tracker.New(tracker.WithIsPidAlive(func(int) bool { return true }))
	tr.ApplyPanePresence("pane-session", []tracker.PanePresence{{
		Agent: "codex", PaneID: "%9", PID: panePID, ThreadID: threadID, ThreadName: "fix it",
	}})
	h.adapter.ctx.PaneForThread = func(id string) (ThreadPane, bool) {
		row := tr.PaneRowForThread("codex", id)
		if row == nil {
			return ThreadPane{}, false
		}
		return ThreadPane{Session: row.Session, PID: row.PID}, true
	}
	var emitted []wire.AgentEvent
	h.adapter.ctx.Emit = func(ev wire.AgentEvent) {
		emitted = append(emitted, ev)
		tr.ApplyEvent(ev, false)
	}

	payload := daemonHookPayload(threadID, "PreToolUse")
	payload.ToolName = "Bash"
	payload.ToolInput = map[string]json.RawMessage{"command": json.RawMessage(`"go test ./..."`)}
	h.hook(payload)

	var rows []wire.AgentEvent
	h.locked(func() {
		emitted = append([]wire.AgentEvent(nil), emitted...)
		rows = tr.GetAgents("pane-session")
	})
	if len(emitted) != 1 {
		t.Fatalf("events = %#v, want one", emitted)
	}
	ev := emitted[0]
	if ev.Session != "pane-session" || ev.ThreadID != threadID || ev.PID != panePID || ev.Status != wire.StatusRunning || ev.ToolDescription != "Running go test ./..." {
		t.Fatalf("event = %#v, want pane-session thread pid %d running with tool description", ev, panePID)
	}
	if len(rows) != 1 || rows[0].PaneID != "%9" || rows[0].ToolDescription != "Running go test ./..." || rows[0].Status != wire.StatusRunning {
		t.Fatalf("rows = %#v, want the pane row updated in place", rows)
	}
}

func TestDaemonHookWithoutLinkedPaneDropsThenRecovers(t *testing.T) {
	const threadID = "12345678-1234-1234-1234-123456789abc"
	h := newHarness(t)
	writeRollout(t, h.adapter.SessionsDir, threadID, `"cli"`, "")
	h.adapter.openFilesForPID = func(int) []string { return nil }
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	linked := false
	h.adapter.ctx.PaneForThread = func(id string) (ThreadPane, bool) {
		return ThreadPane{Session: "pane-session", PID: 4100}, linked && id == threadID
	}

	handle := func(event string) {
		h.hook(daemonHookPayload(threadID, event))
	}

	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "PreToolUse"} {
		handle(event)
	}
	if got := h.snapshot(); len(got) != 0 {
		t.Fatalf("unlinked thread emitted %#v", got)
	}
	if got := strings.Count(logs.String(), "does not own thread"); got != 1 {
		t.Fatalf("drop log count = %d, want 1; logs:\n%s", got, logs.String())
	}

	h.locked(func() { linked = true })
	handle("UserPromptSubmit")
	got := h.snapshot()
	if len(got) != 1 || got[0].Session != "pane-session" || got[0].PID != 4100 || got[0].Status != wire.StatusRunning {
		t.Fatalf("events = %#v, want running on pane-session after the pane links", got)
	}
}

func TestPaneOwnedSenderIgnoresThreadLink(t *testing.T) {
	const threadID = "12345678-1234-1234-1234-123456789abc"
	h := newHarness(t)
	path := writeRollout(t, h.adapter.SessionsDir, threadID, `"cli"`, "")
	h.adapter.openFilesForPID = func(int) []string { return []string{path} }
	lookups := 0
	h.adapter.ctx.PaneForThread = func(string) (ThreadPane, bool) {
		lookups++
		return ThreadPane{Session: "other-session", PID: 4100}, true
	}

	h.hook(wire.HookPayload{
		Agent: "codex", Event: "UserPromptSubmit", SessionID: threadID, Cwd: "/project",
		PID: 300, ProcessSnapshot: "300 200 /bin/sh hook\n200 100 /usr/local/bin/codex",
	})

	got := h.snapshot()
	if len(got) != 1 || got[0].Session != "pid-session" || got[0].PID != 200 {
		t.Fatalf("events = %#v, want pid-session pid 200", got)
	}
	if lookups != 0 {
		t.Fatalf("thread link lookups = %d, want 0 for a pane-owned sender", lookups)
	}
}
