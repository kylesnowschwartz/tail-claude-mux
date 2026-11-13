package ccwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const workerPrompt = `{"type":"user","timestamp":"2026-11-01T10:00:00Z","isSidechain":true,"message":{"role":"user","content":"go"}}` + "\n"
const workerInterrupt = `{"type":"user","timestamp":"2026-11-01T10:00:02Z","isSidechain":true,"message":{"role":"user","content":"[Request interrupted by user]"}}` + "\n"

func fileWorkerParent(t *testing.T, event string) (*Adapter, *testCtx, string) {
	t.Helper()
	a, tc := workerParent(t, event)
	sessionPath := filepath.Join(t.TempDir(), "project", "parent.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkerFile(t, sessionPath, workerPrompt)
	a.threads["parent"].thinking.trackTranscript(sessionPath, "parent")
	return a, tc, filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
}

func writeWorkerFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func addFileWorker(t *testing.T, dir, id, agentType, lines string, finished bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-"+id+".jsonl")
	writeWorkerFile(t, path, workerPrompt+lines)
	setWorkerMeta(t, dir, id, agentType, finished)
	return path
}

func setWorkerMeta(t *testing.T, dir, id, agentType string, finished bool) {
	t.Helper()
	meta := map[string]any{"agentType": agentType}
	if finished {
		meta["finished"] = true
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	writeWorkerFile(t, filepath.Join(dir, "agent-"+id+".meta.json"), string(data))
}

func appendWorkerLines(t *testing.T, path, lines string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(lines)
	closeErr := f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func fleetStepLine(kind, title string) string {
	data, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": "2026-11-01T10:00:01.000Z", "isSidechain": true,
		"message": map[string]any{"role": "assistant", "model": "gpt-x", "content": []any{
			map[string]any{"type": "tool_use", "id": "s1", "name": "mcp__plugin_fleet_step__step", "input": map[string]any{"kind": kind, "title": title}},
		}},
	})
	return string(data) + "\n"
}

func TestFileWorkerLifecycle(t *testing.T) {
	a, tc, dir := fileWorkerParent(t, "Stop")
	addFileWorker(t, dir, "a1", "Explore", "", false)
	before := len(tc.events)
	a.RecheckWorkers()
	if len(tc.events) != before+1 {
		t.Fatalf("start emits = %d", len(tc.events)-before)
	}
	lastWorkerEvent(t, tc, wire.StatusRunning)
	a.RecheckWorkers()
	if len(tc.events) != before+1 {
		t.Fatal("unchanged worker emitted")
	}
	a.HandleHook(hook("Stop", "parent", "/project"))
	lastWorkerEvent(t, tc, wire.StatusRunning)
	before = len(tc.events)
	setWorkerMeta(t, dir, "a1", "Explore", true)
	a.RecheckWorkers()
	if len(tc.events) != before+1 {
		t.Fatalf("finish emits = %d", len(tc.events)-before)
	}
	lastWorkerEvent(t, tc, wire.StatusDone)
	a.RecheckWorkers()
	if len(tc.events) != before+1 {
		t.Fatal("finished worker emitted again")
	}
}

func TestFileWorkerStatusRules(t *testing.T) {
	for _, tc := range []struct {
		name, event, lines   string
		stale, registry      bool
		wantActive, wantEmit bool
	}{
		{name: "waiting", event: "PermissionRequest", wantActive: true},
		{name: "error", event: "StopFailure", wantActive: true},
		{name: "idle", event: "SessionStart", wantActive: true, wantEmit: true},
		{name: "interrupted", event: "Stop", lines: workerInterrupt},
		{name: "older than process", event: "Stop", stale: true, registry: true},
		{name: "older than first seen", event: "Stop", stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, events, dir := fileWorkerParent(t, tc.event)
			path := addFileWorker(t, dir, "a1", "Explore", tc.lines, false)
			if tc.stale {
				start := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
				a.threads["parent"].firstSeenMS = start.UnixMilli()
				if tc.registry {
					a.threads["parent"].firstSeenMS = start.Add(-time.Hour).UnixMilli()
					writeSessionFile(t, a.SessionsDir, 100, map[string]any{"sessionId": "parent", "startedAt": start.UnixMilli()})
				}
				old := start.Add(-time.Minute)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			before := len(events.events)
			a.RecheckWorkers()
			if got := len(a.threads["parent"].fileWorkers) > 0; got != tc.wantActive {
				t.Fatalf("active = %v", got)
			}
			wantEmits := 0
			if tc.wantEmit {
				wantEmits = 1
			}
			if len(events.events)-before != wantEmits {
				t.Fatalf("emits = %d, want %d", len(events.events)-before, wantEmits)
			}
			if got := a.workerStatus("parent", a.threads["parent"]); tc.name == "waiting" && got != wire.StatusWaiting {
				t.Fatalf("status = %s", got)
			}
		})
	}
}

func TestFileWorkerSteps(t *testing.T) {
	for _, agentType := range []string{"fleet:codex", "fleet:pi"} {
		t.Run(agentType, func(t *testing.T) {
			a, tc, dir := fileWorkerParent(t, "Stop")
			a.RecheckWorkers()
			path := addFileWorker(t, dir, "a1", agentType, "", false)
			a.RecheckWorkers()
			state := a.threads["parent"]
			state.lastToolDescription, state.lastToolVerb = "Parent tool", "read"
			before := len(tc.events)
			appendWorkerLines(t, path, fleetStepLine("execute", "go test ./...")+fleetStepLine("edit", "Edit a.go"))
			a.RecheckWorkers()
			if len(tc.events) != before+2 {
				t.Fatalf("step emits = %d", len(tc.events)-before)
			}
			for i, want := range []struct{ verb, title string }{{"run", "go test ./..."}, {"edit", "Edit a.go"}} {
				ev := tc.events[before+i]
				if ev.ThreadID != "parent" || !ev.ToolInvoked || ev.ToolVerb != want.verb || ev.ToolDescription != want.title {
					t.Fatalf("step = %+v", ev)
				}
			}
			a.RecheckWorkers()
			if len(tc.events) != before+2 {
				t.Fatal("steps replayed")
			}
			if state.lastToolDescription != "Parent tool" || state.lastToolVerb != "read" {
				t.Fatal("parent tool overwritten")
			}
			appendWorkerLines(t, path, fleetStepLine("search", "rg final"))
			setWorkerMeta(t, dir, "a1", agentType, true)
			before = len(tc.events)
			a.RecheckWorkers()
			if len(tc.events) != before+2 {
				t.Fatalf("final emits = %d", len(tc.events)-before)
			}
			ev := tc.events[len(tc.events)-1]
			if !ev.ToolInvoked || ev.ToolVerb != "search" || ev.ToolDescription != "rg final" {
				t.Fatalf("final step = %+v", ev)
			}
			if len(state.fileWorkers) != 0 {
				t.Fatal("finished cursor retained")
			}
			a.RecheckWorkers()
			if len(tc.events) != before+2 {
				t.Fatal("final steps replayed")
			}
		})
	}
}

func TestFileWorkerFirstScan(t *testing.T) {
	a, tc, dir := fileWorkerParent(t, "Stop")
	path := addFileWorker(t, dir, "a1", "fleet:pi", fleetStepLine("execute", "history"), false)
	before := len(tc.events)
	a.RecheckWorkers()
	if len(tc.events) != before+1 || tc.events[before].ToolInvoked {
		t.Fatal("startup replayed history")
	}
	before = len(tc.events)
	appendWorkerLines(t, path, fleetStepLine("read", "cat a.go"))
	a.RecheckWorkers()
	if len(tc.events) != before+1 {
		t.Fatalf("emits = %d", len(tc.events)-before)
	}
	if ev := tc.events[before]; !ev.ToolInvoked || ev.ToolDescription != "cat a.go" {
		t.Fatalf("step = %+v", ev)
	}
	a.RecheckWorkers()
	if len(tc.events) != before+1 {
		t.Fatal("step replayed")
	}
}

func TestNativeWorkerDoesNotReadSteps(t *testing.T) {
	a, tc, dir := fileWorkerParent(t, "Stop")
	a.RecheckWorkers()
	addFileWorker(t, dir, "a1", "Explore", fleetStepLine("execute", "ignored"), false)
	before := len(tc.events)
	a.RecheckWorkers()
	if len(tc.events) != before+1 || tc.events[before].ToolInvoked {
		t.Fatal("native worker step emitted")
	}
	if len(a.threads["parent"].fleetStepOffsets) != 0 {
		t.Fatal("native step cursor advanced")
	}
	if got := a.ProbeLiveStatus(100, "parent", ""); got != tracker.ProbeWorking {
		t.Fatalf("probe = %v", got)
	}
}

func TestFileWorkerMissingDirectory(t *testing.T) {
	a, tc, _ := fileWorkerParent(t, "Stop")
	before := len(tc.events)
	a.RecheckWorkers()
	a.RecheckWorkers()
	if len(tc.events) != before {
		t.Fatal("missing directory emitted")
	}
}

func TestFileWorkerInterruptedFinalSteps(t *testing.T) {
	a, tc, dir := fileWorkerParent(t, "Stop")
	a.RecheckWorkers()
	path := addFileWorker(t, dir, "a1", "fleet:pi", "", false)
	a.RecheckWorkers()
	before := len(tc.events)
	appendWorkerLines(t, path, fleetStepLine("read", "final read")+workerInterrupt)
	a.RecheckWorkers()
	if len(tc.events) != before+2 {
		t.Fatalf("emits = %d", len(tc.events)-before)
	}
	ev := tc.events[before+1]
	if ev.Status != wire.StatusDone || !ev.ToolInvoked || ev.ToolDescription != "final read" {
		t.Fatalf("final event = %+v", ev)
	}
	a.RecheckWorkers()
	if len(tc.events) != before+2 {
		t.Fatal("interrupted steps replayed")
	}
}

func TestFileWorkerTitleWidth(t *testing.T) {
	a, tc, dir := fileWorkerParent(t, "Stop")
	a.RecheckWorkers()
	addFileWorker(t, dir, "a1", "fleet:pi", fleetStepLine("edit", strings.Repeat("a", 60)), false)
	a.RecheckWorkers()
	ev := tc.events[len(tc.events)-1]
	if ev.ToolDescription != strings.Repeat("a", 39)+"…" {
		t.Fatalf("description = %q", ev.ToolDescription)
	}
}

func TestFleetStepOffsetsAcrossRuns(t *testing.T) {
	for _, finishedAtStart := range []bool{true, false} {
		t.Run(fmt.Sprint(finishedAtStart), func(t *testing.T) {
			a, tc, dir := fileWorkerParent(t, "Stop")
			path := addFileWorker(t, dir, "a1", "fleet:pi", fleetStepLine("read", "history"), finishedAtStart)
			a.RecheckWorkers()
			if !finishedAtStart {
				appendWorkerLines(t, path, fleetStepLine("edit", "first run"))
				setWorkerMeta(t, dir, "a1", "fleet:pi", true)
				a.RecheckWorkers()
			}
			before := len(tc.events)
			appendWorkerLines(t, path, fleetStepLine("execute", "follow-up"))
			setWorkerMeta(t, dir, "a1", "fleet:pi", false)
			a.RecheckWorkers()
			invoked := 0
			for _, ev := range tc.events[before:] {
				if ev.ToolInvoked {
					invoked++
					if ev.ToolDescription != "follow-up" {
						t.Fatalf("replayed step: %+v", ev)
					}
				}
			}
			if invoked != 1 {
				t.Fatalf("invoked = %d", invoked)
			}
			before = len(tc.events)
			a.RecheckWorkers()
			if len(tc.events) != before {
				t.Fatal("follow-up replayed")
			}
			setWorkerMeta(t, dir, "a1", "fleet:pi", true)
			a.RecheckWorkers()
			before = len(tc.events)
			appendWorkerLines(t, path, fleetStepLine("read", "between polls"))
			a.RecheckWorkers()
			if len(tc.events) != before+1 || tc.events[before].ToolDescription != "between polls" {
				t.Fatal("completed follow-up missed")
			}
		})
	}
}

func TestFileWorkerQuietLimit(t *testing.T) {
	for _, age := range []time.Duration{31 * time.Minute, 29 * time.Minute, 30 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			a, tc, dir := fileWorkerParent(t, "Stop")
			now := time.Now().Truncate(time.Millisecond)
			a.now = func() int64 { return now.UnixMilli() }
			a.threads["parent"].firstSeenMS = now.Add(-time.Hour).UnixMilli()
			path := addFileWorker(t, dir, "a1", "Explore", "", false)
			mtime := now.Add(-age)
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			before := len(tc.events)
			a.RecheckWorkers()
			wantActive := age <= workerQuietLimit
			if got := a.threads["parent"].fileWorkers["a1"]; got != wantActive {
				t.Fatalf("active = %v", got)
			}
			if wantActive {
				lastWorkerEvent(t, tc, wire.StatusRunning)
				a.now = func() int64 { return now.Add(2 * time.Minute).UnixMilli() }
				a.RecheckWorkers()
				lastWorkerEvent(t, tc, wire.StatusDone)
			} else if len(tc.events) != before {
				t.Fatal("quiet worker emitted")
			}
		})
	}
}

func workerParent(t *testing.T, event string) (*Adapter, *testCtx) {
	t.Helper()
	tc := makeCtx(nil, map[int]string{100: "work"})
	a := newStartedAdapter(t, tc)
	p := hook(event, "parent", "/project")
	p.PID, p.ProcessSnapshot = 100, "100 1 claude"
	a.HandleHook(p)
	a.threads["parent"].threadName = "Parent thread"
	return a, tc
}

func lastWorkerEvent(t *testing.T, tc *testCtx, status string) wire.AgentEvent {
	t.Helper()
	ev := tc.events[len(tc.events)-1]
	if ev.Agent != "claude-code" || ev.Session != "work" || ev.ThreadID != "parent" || ev.PID != 100 || ev.Status != status {
		t.Fatalf("parent event = %+v, want status %s", ev, status)
	}
	return ev
}
