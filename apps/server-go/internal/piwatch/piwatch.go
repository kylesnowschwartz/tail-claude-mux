// Package piwatch ports packages/runtime/src/agents/watchers/pi-hooks.ts —
// the hook-based pi agent watcher.
//
// Lifecycle events arrive via POST /hook from pi (see
// integrations/pi-extension/). Each payload carries `agent: "pi"` so the
// single /hook endpoint can fan out to multiple adapters.
//
// Pi-specific behavior worth knowing:
//   - Tool-level errors keep status `running`. The LLM routinely recovers
//     from tool failures, so surfacing them as `error` creates noise.
//   - Thread-level errors come in via `agent_end` with `stop_reason: "error"`
//     and carry `error_message` — that is surfaced as `toolDescription`.
//   - `stop_reason: "aborted"` → `interrupted` (Escape during a turn).
//   - `session_shutdown` is treated like Claude Code's SessionEnd: it
//     bypasses dedup and drops the thread so the tracker cleans up
//     immediately instead of waiting for the terminal-prune window.
//
// JSONL is read for two bounded purposes only:
//  1. Cold-start seed: scan recent pi session files once on startup so the
//     sidebar shows pre-existing pi instances before any new hook arrives.
//  2. Thread name resolution: one-time read when a hook introduces an
//     unknown session UUID and we want a better label than "pi:<uuid>".
//
// The TS adapter's dbg() tracing to /tmp/tcm-debug.log is dropped: the Go
// ccwatch port carries no debug logging and this package mirrors it.
//
// Concurrency contract: the Adapter is NOT safe for concurrent use — the
// server serializes HandleHook / Seed under its state lock, the same way
// the bun server's single JS thread does. The one async path, thread-name
// resolution, does its file IO lockless in a goroutine and re-enters
// through Context.Locked.
package piwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// staleMS bounds the cold-start seed to recently-touched session files.
const staleMS = 5 * 60 * 1000

// errorMessageLimit caps the agent_end error_message surfaced as a tool
// description.
const errorMessageLimit = 80

// Context is what the server provides the adapter: session routing, event
// delivery, and re-entry into the serialized state loop for async work.
type Context struct {
	// ResolveSession maps a project cwd to a tmux session ("" = no match).
	ResolveSession func(projectDir string) string
	// ResolveSessionByPid walks the OS process tree from an agent pid up to
	// the owning pane's session ("" = no match).
	ResolveSessionByPid func(pid int) string
	// Emit delivers one agent event. Called with the server lock held.
	Emit func(ev wire.AgentEvent)
	// Locked runs fn under the server's state lock (async re-entry).
	Locked func(fn func())
}

// threadState mirrors ThreadState in the TS adapter.
type threadState struct {
	status       string
	threadName   string
	projectDir   string
	nameResolved bool
	// pid is pi's process.pid, reported directly (the extension runs
	// in-process — no ancestor walk required). Captured once per thread;
	// used by the tracker's liveness sweep. 0 = unresolved.
	pid int
	// lastToolDescription from tool_execution_start; kept across
	// tool_execution_end.
	lastToolDescription string
}

// Adapter is the PiHookAdapter port.
type Adapter struct {
	// SessionsDir defaults to ~/.pi/agent/sessions in the server; tests
	// inject a temp dir.
	SessionsDir string

	ctx     *Context
	threads map[string]*threadState
	now     func() int64 // epoch ms, injectable for tests
}

// New returns an Adapter reading the given pi sessions dir.
func New(sessionsDir string) *Adapter {
	return &Adapter{
		SessionsDir: sessionsDir,
		threads:     map[string]*threadState{},
		now:         func() int64 { return time.Now().UnixMilli() },
	}
}

// Name is the watcher/agent discriminator.
func (a *Adapter) Name() string { return "pi" }

// Start binds the context and runs the cold-start seed. Call with the
// server lock held. (The TS seed is async; here it runs synchronously
// under the lock, same as the ccwatch port.)
func (a *Adapter) Start(ctx *Context) {
	a.ctx = ctx
	a.seedFromJSONL()
}

// HandleHook routes one validated hook payload. Call with the server lock
// held.
func (a *Adapter) HandleHook(payload wire.HookPayload) {
	// Only accept payloads explicitly marked as pi. Missing/other agents
	// belong to other adapters.
	if payload.Agent != "pi" {
		return
	}
	if a.ctx == nil {
		return
	}
	if payload.SessionID == "" || payload.Cwd == "" {
		return
	}

	// Routing: pid is the authoritative channel — pi's extension runs
	// in-process and reports process.pid directly, and the answer is
	// independent of the active pane's cwd (which the cwd resolver keys on
	// and which drifts as the user navigates). When pid is present and pid
	// lookup fails, we drop the event rather than silently fall through to
	// cwd: a fallback there would mask future pid-resolution regressions,
	// re-introducing the same class of silent-drop bug from a different
	// direction. Cwd remains the only channel when pid is absent —
	// backward-compat for malformed payloads from non-pi hook clients.
	var session string
	if payload.PID != 0 {
		session = a.ctx.ResolveSessionByPid(payload.PID)
	} else {
		session = a.ctx.ResolveSession(payload.Cwd)
	}
	if session == "" {
		return
	}

	threadID := payload.SessionID
	state := a.threads[threadID]
	isNewThread := state == nil
	if isNewThread {
		// status idle is overwritten below if the event maps to something
		// else. If session_start carries a name, it already counts as
		// resolved.
		state = &threadState{status: wire.StatusIdle, projectDir: payload.Cwd}
		if payload.Event == "session_start" && payload.SessionName != "" {
			state.threadName = payload.SessionName
			state.nameResolved = true
		}
		a.threads[threadID] = state
		if !state.nameResolved {
			a.resolveThreadNameAsync(threadID, state)
		}
	} else if payload.Event == "session_start" && payload.SessionName != "" && state.threadName == "" {
		state.threadName = payload.SessionName
		state.nameResolved = true
	}

	// Capture pid once per thread. Pi reports its own process.pid, which is
	// the long-lived agent process — no ancestor walk required.
	if state.pid == 0 && payload.PID != 0 {
		state.pid = payload.PID
	}

	// session_shutdown is the definitive end signal and must bypass dedup
	// so the tracker releases the instance immediately rather than waiting
	// out the terminal-prune window.
	if payload.Event == "session_shutdown" {
		state.status = wire.StatusDone
		state.lastToolDescription = ""
		a.emit(threadID, state, session, emitEnded)
		delete(a.threads, threadID)
		return
	}

	newStatus, desc := resolveEvent(payload)
	if newStatus == "" {
		return
	}

	// A "description update" is a visible change in the user-facing label.
	// Clearing an already-empty description, or setting the same value, does
	// not count and should be suppressed by dedup.
	prevDescription := state.lastToolDescription
	newDescription := prevDescription
	switch desc.kind {
	case descSet:
		newDescription = desc.value
	case descClear:
		newDescription = ""
	}
	hadToolUpdate := newDescription != prevDescription
	state.lastToolDescription = newDescription

	// An invocation starts a NEW call even when its description matches the
	// previous one — repeated identical calls must each reach the activity
	// log. Empty descriptions are excluded: the log discards them anyway, so
	// emitting would be a pure no-op.
	kind := emitUpdate
	if desc.invoked && newDescription != "" {
		kind = emitInvoked
	}

	// Dedup: suppress emission when nothing meaningful changed. Always emit
	// for a new thread, for status changes, a visible description update, or
	// a fresh tool invocation.
	if state.status == newStatus && !isNewThread && !hadToolUpdate && kind != emitInvoked {
		return
	}

	state.status = newStatus
	a.emit(threadID, state, session, kind)
}

// descDirective is the tool-description directive attached to an event:
// keep the current description, set a new one, or clear it.
type descKind int

const (
	descKeep descKind = iota
	descSet
	descClear
)

type descDirective struct {
	kind  descKind
	value string
	// invoked marks the event as the start of a NEW tool call (→ the
	// emitted AgentEvent's ToolInvoked). Owned here so resolveEvent is the
	// single place that decides what a pi event means — a descSet without
	// invoked (agent_end's error label) is a label update, not a call.
	invoked bool
}

// resolveEvent maps a pi hook payload to a new status plus a
// tool-description directive. Returning "" means ignore. (The TS
// resolveEvent took an unused _state param — dropped.)
func resolveEvent(payload wire.HookPayload) (string, descDirective) {
	switch payload.Event {
	case "session_start":
		return wire.StatusIdle, descDirective{kind: descClear}

	case "agent_start":
		return wire.StatusRunning, descDirective{kind: descClear}

	case "tool_execution_start":
		return wire.StatusRunning, descDirective{
			kind:    descSet,
			value:   ToolDescription(payload.ToolName, payload.ToolInput),
			invoked: true,
		}

	case "tool_execution_end":
		// Tool-level errors are routine; keep the user-visible description so
		// they can still see which tool the agent was running.
		return wire.StatusRunning, descDirective{kind: descKeep}

	case "agent_end":
		switch payload.StopReason {
		case "aborted":
			return wire.StatusInterrupted, descDirective{kind: descClear}
		case "error":
			return wire.StatusError, descDirective{kind: descSet, value: truncateError(payload.ErrorMessage)}
		default:
			// stop | length | toolUse | absent → done
			return wire.StatusDone, descDirective{kind: descClear}
		}

	default:
		return "", descDirective{kind: descKeep}
	}
}

// emitKind is what one emitted AgentEvent represents: a plain state update,
// the start of a new tool call, or the definitive end of the thread. The
// three are mutually exclusive — a kind, not a pair of independent bools.
type emitKind int

const (
	emitUpdate  emitKind = iota // status/name/description refresh
	emitInvoked                 // a new tool call started (→ ToolInvoked)
	emitEnded                   // thread definitively ended (→ Ended)
)

func (a *Adapter) emit(threadID string, state *threadState, session string, kind emitKind) {
	if a.ctx == nil {
		return
	}
	a.ctx.Emit(wire.AgentEvent{
		Agent:           "pi",
		Session:         session,
		Status:          state.status,
		TS:              a.now(),
		ThreadID:        threadID,
		ThreadName:      state.threadName,
		ToolDescription: state.lastToolDescription,
		ToolInvoked:     kind == emitInvoked,
		PID:             state.pid,
		Ended:           kind == emitEnded,
	})
}

// --- Cold-start seed from JSONL files ---

// seedFromJSONL bootstraps thread state from pi session files touched
// within staleMS: idle/terminal outcomes are skipped, active ones emit with
// the file's mtime as ts. The SessionHeader's cwd routes the seed —
// hooks always win over the seed for a thread already known.
func (a *Adapter) seedFromJSONL() {
	if a.ctx == nil {
		return
	}
	dirs, err := os.ReadDir(a.SessionsDir)
	if err != nil {
		return
	}
	nowMS := a.now()

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		dirPath := filepath.Join(a.SessionsDir, dir.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, file := range files {
			threadID := extractSessionIDFromFilename(file.Name())
			if threadID == "" {
				continue
			}
			info, err := file.Info()
			if err != nil {
				continue
			}
			mtimeMS := info.ModTime().UnixMilli()
			if nowMS-mtimeMS > staleMS {
				continue
			}
			// Hooks always win: if we already know this thread, skip the seed.
			if _, ok := a.threads[threadID]; ok {
				continue
			}

			raw, err := os.ReadFile(filepath.Join(dirPath, file.Name()))
			if err != nil {
				continue
			}
			status, threadName, projectDir := parseJSONLTrailingStatus(string(raw))
			if status == wire.StatusIdle || wire.IsTerminalStatus(status) {
				continue
			}
			if projectDir == "" {
				continue
			}
			session := a.ctx.ResolveSession(projectDir)
			if session == "" {
				continue
			}

			a.threads[threadID] = &threadState{
				status:       status,
				threadName:   threadName,
				projectDir:   projectDir,
				nameResolved: true,
			}
			a.ctx.Emit(wire.AgentEvent{
				Agent:      "pi",
				Session:    session,
				Status:     status,
				TS:         mtimeMS,
				ThreadID:   threadID,
				ThreadName: threadName,
			})
		}
	}
}

// --- One-time thread name resolution ---

// resolveThreadNameAsync finds the thread's `*_<threadId>.jsonl` file across
// session dirs, reads it off-lock, and re-emits with the resolved name under
// Context.Locked. Fire-once per thread (nameResolved), same as the TS
// adapter's async path.
func (a *Adapter) resolveThreadNameAsync(threadID string, state *threadState) {
	if state.nameResolved {
		return
	}
	state.nameResolved = true
	ctx := a.ctx
	sessionsDir := a.SessionsDir
	go func() {
		dirs, err := os.ReadDir(sessionsDir)
		if err != nil {
			return
		}
		suffix := "_" + threadID + ".jsonl"
		for _, dir := range dirs {
			dirPath := filepath.Join(sessionsDir, dir.Name())
			files, err := os.ReadDir(dirPath)
			if err != nil {
				continue
			}
			match := ""
			for _, f := range files {
				if strings.HasSuffix(f.Name(), suffix) {
					match = f.Name()
					break
				}
			}
			if match == "" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dirPath, match))
			if err != nil {
				continue
			}
			_, threadName, _ := parseJSONLTrailingStatus(string(raw))
			if threadName != "" && ctx != nil {
				ctx.Locked(func() {
					// The thread may have ended while we read.
					if cur, ok := a.threads[threadID]; ok && cur == state {
						state.threadName = threadName
						if session := ctx.ResolveSession(state.projectDir); session != "" {
							a.emit(threadID, state, session, emitUpdate)
						}
					}
				})
			}
			return // found the file, done
		}
	}()
}

// --- JSONL parsing (shared between seed + thread name resolution) ---

// piMessage is the message envelope inside a pi journal entry. The TS
// PiMessage declared errorMessage too, but nothing read it — dropped.
type piMessage struct {
	Role       string          `json:"role"`       // "user" | "assistant" | "toolResult" | ...
	Content    json.RawMessage `json:"content"`    // string or []{type,text}
	StopReason string          `json:"stopReason"` // stop|length|toolUse|error|aborted
}

// piJournalEntry is one pi JSONL line. The TS PiJournalEntry declared the
// SessionHeader's id too, but nothing read it — dropped.
type piJournalEntry struct {
	Type    string     `json:"type"` // "session" | "message" | "session_info" | ...
	Name    string     `json:"name"` // session_info display name
	Message *piMessage `json:"message"`
	Cwd     string     `json:"cwd"` // SessionHeader (type == "session")
}

// statusFromStopReason maps a pi assistant stopReason to a status.
// Absent → running (the turn is mid-flight); unknown values → "" (ignore).
func statusFromStopReason(sr string) string {
	switch sr {
	case "":
		return wire.StatusRunning
	case "stop", "length":
		return wire.StatusDone
	case "toolUse":
		// toolUse means a tool call is about to be executed → still working.
		return wire.StatusRunning
	case "aborted":
		return wire.StatusInterrupted
	case "error":
		return wire.StatusError
	default:
		return ""
	}
}

// determineStatusFromEntry determines status from a JSONL entry. Returns ""
// for non-conversational entries.
func determineStatusFromEntry(e piJournalEntry) string {
	if e.Type != "message" || e.Message == nil {
		return ""
	}
	switch e.Message.Role {
	case "assistant":
		return statusFromStopReason(e.Message.StopReason)
	case "user", "toolResult":
		return wire.StatusRunning
	default:
		return ""
	}
}

// extractThreadNameFromUser extracts a thread name from the first text of a
// user message, rejecting markup/JSON lines, sanitized and width-capped at
// 80 cells.
func extractThreadNameFromUser(e piJournalEntry) string {
	if e.Type != "message" || e.Message == nil || e.Message.Role != "user" {
		return ""
	}
	text := firstContentText(e.Message.Content)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "<") || strings.HasPrefix(text, "{") {
		return ""
	}
	return textutil.TruncateToWidth(textutil.SanitizeForDisplay(text), 80)
}

// firstContentText decodes a pi message content field: a JSON string is the
// text itself; an array yields the first {type:"text"} item's text.
func firstContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	case '[':
		var items []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &items) == nil {
			for _, c := range items {
				if c.Type == "text" && c.Text != "" {
					return c.Text
				}
			}
		}
	}
	return ""
}

// extractSessionInfoName is the session display name set via
// `pi.setSessionName()` / `/name`.
func extractSessionInfoName(e piJournalEntry) string {
	if e.Type == "session_info" {
		return e.Name
	}
	return ""
}

// extractSessionIDFromFilename extracts the UUID suffix from a pi session
// filename. Format is `<timestamp>_<uuid>.jsonl`; we take the part after
// the final `_`. "" = not a session file.
func extractSessionIDFromFilename(filename string) string {
	base, ok := strings.CutSuffix(filename, ".jsonl")
	if !ok {
		return ""
	}
	us := strings.LastIndex(base, "_")
	if us < 0 {
		return ""
	}
	return base[us+1:]
}

// parseJSONLTrailingStatus walks a pi JSONL text and returns the trailing
// conversational status, the best thread-name we can derive (session_info
// wins, else first user msg), and the cwd recorded in the SessionHeader.
//
// Using SessionHeader.cwd avoids decoding pi's lossy directory-name scheme
// (e.g. `--Users-kyle-meta-claude-tcm--` can't reliably distinguish
// `meta/claude` from `meta-claude`). The header is authoritative.
func parseJSONLTrailingStatus(text string) (status, threadName, cwd string) {
	status = wire.StatusIdle
	var sessionInfoName, firstUserText string

	for line := range strings.SplitSeq(text, "\n") {
		if line == "" {
			continue
		}
		var entry piJournalEntry
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}

		if entry.Type == "session" {
			cwd = entry.Cwd
			continue
		}

		if info := extractSessionInfoName(entry); info != "" {
			sessionInfoName = info
		} else if firstUserText == "" {
			if name := extractThreadNameFromUser(entry); name != "" {
				firstUserText = name
			}
		}

		if s := determineStatusFromEntry(entry); s != "" {
			status = s
		}
	}

	threadName = sessionInfoName
	if threadName == "" {
		threadName = firstUserText
	}
	return status, threadName, cwd
}

// --- Tool descriptions (pi-native snake_case tool names) ---

// ToolDescription generates a human-readable description of the current
// tool activity. Every interpolated value is sanitized + width-capped at
// the leaf, so pasted ANSI or wide-char paths can't disturb the row budget.
func ToolDescription(toolName string, toolInput map[string]json.RawMessage) string {
	if toolName == "" {
		return ""
	}
	switch toolName {
	case "read":
		return pathDesc("Reading", toolInput)
	case "edit":
		return pathDesc("Editing", toolInput)
	case "write":
		return pathDesc("Writing", toolInput)
	case "ls":
		return pathDesc("Listing", toolInput)
	case "bash":
		if cmd := safeStr(toolInput, "command"); cmd != "" {
			return "Running " + textutil.TruncateToWidth(cmd, 30)
		}
		return "Running command"
	case "find", "glob", "grep":
		if pattern := safeStr(toolInput, "pattern"); pattern != "" {
			return "Searching " + textutil.TruncateToWidth(pattern, 30)
		}
		return "Searching"
	case "agent":
		if desc := safeStr(toolInput, "description"); desc != "" {
			return textutil.TruncateToWidth(desc, 40)
		}
		return "Agent"
	case "web_fetch":
		return "Fetching URL"
	case "web_search":
		if query := safeStr(toolInput, "query"); query != "" {
			return "Search: " + textutil.TruncateToWidth(query, 30)
		}
		return "Searching web"
	case "ask_user_question":
		if q := safeStr(toolInput, "question"); q != "" {
			return "Question: " + textutil.TruncateToWidth(q, 50)
		}
		return "Asking question"
	case "todo_write":
		return "Updating todos"
	default:
		return toolName
	}
}

// safeStr reads a string field from tool input, sanitized; "" if missing or
// non-string.
func safeStr(input map[string]json.RawMessage, key string) string {
	raw, ok := input[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return textutil.SanitizeForDisplay(s)
}

// pathDesc builds "<verb> <basename>" from pi's `path` input key (pi tools
// use `path`, not Claude's `file_path`).
func pathDesc(verb string, input map[string]json.RawMessage) string {
	if p := safeStr(input, "path"); p != "" {
		return verb + " " + filepath.Base(p)
	}
	return verb
}

// truncateError trims + sanitizes an agent_end error_message and caps it at
// errorMessageLimit cells; "" when nothing displayable remains.
func truncateError(msg string) string {
	trimmed := strings.TrimSpace(textutil.SanitizeForDisplay(msg))
	if trimmed == "" {
		return ""
	}
	return textutil.TruncateToWidth(trimmed, errorMessageLimit)
}
