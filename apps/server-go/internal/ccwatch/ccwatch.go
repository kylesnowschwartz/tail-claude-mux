// Package ccwatch ports packages/runtime/src/agents/watchers/
// claude-code-hooks.ts — the hook-based Claude Code agent watcher.
//
// Lifecycle events (SessionStart, UserPromptSubmit, PreToolUse,
// PermissionRequest, PostToolUse, Stop, StopFailure, Notification,
// SessionEnd) arrive via POST /hook. JSONL reading is kept for two bounded
// purposes: a cold-start seed of recent files, and a one-time thread-name
// read when a new session_id appears.
//
// Claude's on-disk formats decode through agent-ouija: registry.Live for
// ~/.claude/sessions/<pid>.json (the authoritative liveness map, incl. the
// active subagent), transcript.ParseEntryLenient for transcript lines. The
// status heuristics over that data are tcm policy and stay here.
//
// Concurrency contract: the Adapter is NOT safe for concurrent use — the
// server serializes HandleHook / ProbeLiveStatus / Seed under its state
// lock, the same way the bun server's single JS thread does. The one async
// path, thread-name resolution, does its file IO lockless in a goroutine
// and re-enters through Context.Locked.
package ccwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/claude/registry"
	"github.com/kylesnowschwartz/agent-ouija/claude/transcript"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/procwalk"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// claudeCmdRE is the path-segment aware matcher for the long-lived claude
// process: `claude` or `claude-code` preceded by ^ or /, followed by
// whitespace, /, or end — so `meta-claude` never false-positives. (The TS
// original uses a lookahead; RE2 has none, but a consuming group is
// equivalent for a boolean match.)
var claudeCmdRE = regexp.MustCompile(`(?i)(?:^|/)claude(?:-code)?($|[\s/])`)

// staleMS bounds the cold-start seed to recently-touched transcripts.
const staleMS = 5 * 60 * 1000

// busyHungMS: a `busy` session whose updatedAt is older than this is hung /
// abandoned rather than genuinely working. Longer than any realistic single
// tool call so a long Bash/build is never misread as stuck.
const busyHungMS = 30 * 60 * 1000

// hookStatusMap is HOOK_STATUS_MAP: hook event → agent status.
// StopFailure (API-error turn death) surfaces as "error" — the truthful
// terminal state; without it the "running" spinner would never clear.
// Notification is handled separately (status depends on notification_type).
var hookStatusMap = map[string]string{
	"UserPromptSubmit":  wire.StatusRunning,
	"SessionStart":      wire.StatusIdle,
	"PreToolUse":        wire.StatusRunning,
	"PermissionRequest": wire.StatusWaiting,
	"PostToolUse":       wire.StatusRunning,
	"Stop":              wire.StatusDone,
	"StopFailure":       wire.StatusError,
	"SessionEnd":        wire.StatusDone,
}

var terminalStatuses = map[string]bool{
	wire.StatusDone: true, wire.StatusError: true, wire.StatusInterrupted: true,
}

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
	// pid is the resolved long-lived agent pid: ancestor walk against the
	// hook's process_snapshot (preferred) or the sessions/-walk fallback.
	// 0 = unresolved.
	pid int
	// lastToolDescription from PreToolUse/PermissionRequest — cleared on
	// non-tool events.
	lastToolDescription string
	// subagent from sessions/<pid>.json `agent`, "" when the parent thread
	// is in control.
	subagent string
}

// Adapter is the ClaudeCodeHookAdapter port.
type Adapter struct {
	// ProjectsDir and SessionsDir default to claudedir paths in the server.
	ProjectsDir string
	SessionsDir string

	ctx     *Context
	threads map[string]*threadState
	now     func() int64 // epoch ms, injectable for tests
}

// New returns an Adapter reading the given Claude dirs.
func New(projectsDir, sessionsDir string) *Adapter {
	return &Adapter{
		ProjectsDir: projectsDir,
		SessionsDir: sessionsDir,
		threads:     map[string]*threadState{},
		now:         func() int64 { return time.Now().UnixMilli() },
	}
}

// Name is the watcher/agent discriminator.
func (a *Adapter) Name() string { return "claude-code" }

// Start binds the context and runs the cold-start seed. Call with the
// server lock held.
func (a *Adapter) Start(ctx *Context) {
	a.ctx = ctx
	a.seedFromJSONL()
}

// Stop clears all state.
func (a *Adapter) Stop() {
	a.threads = map[string]*threadState{}
	a.ctx = nil
}

// HandleHook routes one validated hook payload. Call with the server lock
// held.
func (a *Adapter) HandleHook(payload wire.HookPayload) {
	// Filter on the optional agent discriminator; missing falls through as
	// Claude Code (legacy hook payloads have no agent field).
	if payload.Agent != "" && payload.Agent != "claude-code" {
		return
	}
	if a.ctx == nil {
		return
	}

	newStatus := a.resolveStatus(payload)
	if newStatus == "" {
		return
	}

	threadID := payload.SessionID
	state := a.threads[threadID]
	isNewThread := state == nil
	if isNewThread {
		state = &threadState{status: wire.StatusIdle, projectDir: payload.Cwd}
		a.threads[threadID] = state
		a.resolveThreadNameAsync(threadID, state)
	}

	// Resolve the long-lived agent pid once per thread: the hook's reported
	// pid ($PPID) is the `sh -c` wrapper; ancestry against the snapshot
	// finds the actual claude process. Re-resolve on every hook until a pid
	// lands (a hook without process_snapshot can arrive first). Must
	// precede session resolution — pid is the authoritative routing channel.
	if state.pid == 0 && payload.PID != 0 && payload.ProcessSnapshot != "" {
		proc := procwalk.ParseProcessSnapshot(payload.ProcessSnapshot)
		resolved := procwalk.ResolveAgentSessionPid(payload.PID, claudeCmdRE, proc)
		if resolved != payload.PID {
			state.pid = resolved // walked up to a claude ancestor
		} else if info, ok := proc[payload.PID]; ok && claudeCmdRE.MatchString(info.Command) {
			// Walker gave up; trust the reported pid only when its OWN
			// command matches — otherwise it's the wrapper shell and the
			// liveness sweep would false-fire.
			state.pid = payload.PID
		}
	}

	// Routing: pid is authoritative. When pid resolves but the pane lookup
	// fails, drop rather than fall through to cwd — a fallback there would
	// mask pid-resolution regressions. Cwd remains the channel only while
	// no pid has resolved yet.
	var session string
	if state.pid != 0 {
		session = a.ctx.ResolveSessionByPid(state.pid)
	} else {
		session = a.ctx.ResolveSession(payload.Cwd)
	}
	if session == "" {
		return
	}

	// Refresh subagent from sessions/<pid>.json; failures leave the prior
	// value in place (preserved through transient errors).
	a.refreshSubagent(threadID, state)

	// SessionEnd bypasses the dedup below: a prior Stop already set done,
	// so dedup would swallow the end signal and leave a ghost until prune.
	if payload.Event == "SessionEnd" {
		state.status = newStatus
		state.lastToolDescription = ""
		a.emit(threadID, state, session, true)
		delete(a.threads, threadID)
		return
	}

	hasToolContext := payload.Event == "PreToolUse" || payload.Event == "PermissionRequest"
	if hasToolContext {
		state.lastToolDescription = ToolDescription(payload.ToolName, payload.ToolInput)
	} else if payload.Event != "PostToolUse" {
		// Clear on non-tool events; PostToolUse keeps the prior description
		// (a tool just finished).
		state.lastToolDescription = ""
	}

	// Dedup: skip when status is unchanged — except new threads and tool
	// events (fresh tool description).
	if state.status == newStatus && !isNewThread && !hasToolContext {
		return
	}

	state.status = newStatus
	a.emit(threadID, state, session, false)
}

// waiting/idle Notification subtypes (user must act / agent idle at prompt).
var (
	waitingNotificationTypes = map[string]bool{"permission_prompt": true}
	idleNotificationTypes    = map[string]bool{"idle_prompt": true}
)

// resolveStatus maps a hook payload to a status, "" = ignore.
func (a *Adapter) resolveStatus(payload wire.HookPayload) string {
	if payload.Event == "Notification" {
		if waitingNotificationTypes[payload.NotificationType] {
			return wire.StatusWaiting
		}
		if idleNotificationTypes[payload.NotificationType] {
			return wire.StatusDone
		}
		return "" // unknown subtypes — ignore rather than guess
	}
	return hookStatusMap[payload.Event]
}

func (a *Adapter) emit(threadID string, state *threadState, session string, ended bool) {
	if a.ctx == nil {
		return
	}
	a.ctx.Emit(wire.AgentEvent{
		Agent:           "claude-code",
		Session:         session,
		Status:          state.status,
		TS:              a.now(),
		ThreadID:        threadID,
		ThreadName:      state.threadName,
		ToolDescription: state.lastToolDescription,
		PID:             state.pid,
		Subagent:        state.subagent,
		Ended:           ended,
	})
}

// --- sessions/<pid>.json (agent-ouija registry format) ---

// readSessionFile decodes one sessions/<pid>.json through registry.Live.
func (a *Adapter) readSessionFile(pid int) *registry.Live {
	raw, err := os.ReadFile(filepath.Join(a.SessionsDir, strconv.Itoa(pid)+".json"))
	if err != nil {
		return nil
	}
	var l registry.Live
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	return &l
}

// resolvePidFromSessions walks the sessions registry once and returns the
// pid whose file matches threadID (0 = none).
func (a *Adapter) resolvePidFromSessions(threadID string) int {
	for _, l := range registry.Read(a.SessionsDir) {
		if l.SessionID == threadID && l.PID > 0 {
			return l.PID
		}
	}
	return 0
}

// ClassifySessionStatus decides whether a stale `running` instance is
// genuinely working from its session file. Pure — unit-testable off disk.
//
//   - file nil / no status (sdk-cli) → ProbeNoSignal
//   - sessionId mismatch (pid reused for another session) → ProbeEnded
//   - busy + fresh updatedAt → ProbeWorking
//   - busy + updatedAt older than busyHungMS → ProbeEnded (hung)
//   - idle | waiting | done → ProbeEnded
func ClassifySessionStatus(file *registry.Live, threadID string, nowMS int64) tracker.ProbeVerdict {
	if file == nil {
		return tracker.ProbeNoSignal
	}
	if file.SessionID != "" && threadID != "" && file.SessionID != threadID {
		return tracker.ProbeEnded
	}
	switch file.Status {
	case "busy":
		if file.UpdatedAt != 0 && nowMS-int64(file.UpdatedAt) > busyHungMS {
			return tracker.ProbeEnded
		}
		return tracker.ProbeWorking
	case "idle", "waiting", "done":
		return tracker.ProbeEnded
	default:
		return tracker.ProbeNoSignal // status absent — defer to prune ceiling
	}
}

// ClassifyTitleStatus classifies a decoded tmux pane_title into a turn-state
// verdict: leading braille glyph (U+2800–U+28FF, Claude's spinner) →
// working; leading sparkle (U+2733, idle at prompt) → ended; anything else
// → no signal. Fill-gaps-only: the session file always wins when definitive.
func ClassifyTitleStatus(title string) tracker.ProbeVerdict {
	for _, r := range title {
		switch {
		case r >= 0x2800 && r <= 0x28ff:
			return tracker.ProbeWorking
		case r == 0x2733:
			return tracker.ProbeEnded
		}
		break
	}
	return tracker.ProbeNoSignal
}

// ProbeLiveStatus is the authoritative liveness probe for the tracker's
// reconcile pass. The session file verdict wins when definitive; only a
// null verdict (sdk-cli / absent file) falls back to the pane's OSC title —
// so a mid-turn title can't manufacture a false "ended". On a definitive
// ended verdict the cached thread state drops, letting a resumed session
// re-emit cleanly instead of staying pinned to "running". Call with the
// server lock held.
func (a *Adapter) ProbeLiveStatus(pid int, threadID, paneTitle string) tracker.ProbeVerdict {
	verdict := ClassifySessionStatus(a.readSessionFile(pid), threadID, a.now())
	if verdict == tracker.ProbeNoSignal && paneTitle != "" {
		verdict = ClassifyTitleStatus(paneTitle)
	}
	if verdict == tracker.ProbeEnded {
		delete(a.threads, threadID)
	}
	return verdict
}

// refreshSubagent refreshes state.subagent from sessions/<pid>.json.
//
// PID precedence: (1) state.pid from the process-ancestry walker; (2) the
// sessions/-walk fallback. PID-reuse detection: the file's sessionId must
// match the thread — a mismatch clears the cached pid for re-resolution. A
// missing file is NOT reuse (may be transient) and never clobbers the pid.
func (a *Adapter) refreshSubagent(threadID string, state *threadState) {
	if state.pid == 0 {
		resolved := a.resolvePidFromSessions(threadID)
		if resolved == 0 {
			state.subagent = ""
			return
		}
		state.pid = resolved
	}

	cached := a.readSessionFile(state.pid)
	if cached == nil {
		state.subagent = ""
		return
	}
	if cached.SessionID != threadID {
		state.pid = 0
		state.subagent = ""
		return
	}
	state.subagent = cached.Agent
}

// --- Cold-start seed from JSONL files ---

// seedFromJSONL bootstraps thread state from transcripts touched within
// staleMS: idle/terminal outcomes are skipped, active ones emit with the
// file's mtime as ts. Seeded entries route by pid where sessions/*.json
// resolves one — the same channel live hooks use — so the seed and the
// first hook can't split one conversation into two rows.
func (a *Adapter) seedFromJSONL() {
	if a.ctx == nil {
		return
	}
	dirs, err := os.ReadDir(a.ProjectsDir)
	if err != nil {
		return
	}
	nowMS := a.now()

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		dirPath := filepath.Join(a.ProjectsDir, dir.Name())
		projectDir := decodeProjectDir(dir.Name())

		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, file := range files {
			name := file.Name()
			if !strings.HasSuffix(name, ".jsonl") {
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
			threadID := strings.TrimSuffix(name, ".jsonl")
			if _, ok := a.threads[threadID]; ok {
				continue // hooks already established state
			}

			latestStatus, threadName := scanTranscript(filepath.Join(dirPath, name))
			if latestStatus == wire.StatusIdle || terminalStatuses[latestStatus] {
				continue
			}

			pid := a.resolvePidFromSessions(threadID)
			session := ""
			if pid != 0 {
				session = a.ctx.ResolveSessionByPid(pid)
			}
			if session == "" {
				session = a.ctx.ResolveSession(projectDir)
			}
			if session == "" {
				continue
			}

			a.threads[threadID] = &threadState{
				status:       latestStatus,
				threadName:   threadName,
				projectDir:   projectDir,
				nameResolved: true,
				pid:          pid,
			}
			a.ctx.Emit(wire.AgentEvent{
				Agent:      "claude-code",
				Session:    session,
				Status:     latestStatus,
				TS:         mtimeMS,
				ThreadID:   threadID,
				ThreadName: threadName,
				PID:        pid,
			})
		}
	}
}

// scanTranscript folds determineStatus/name extraction over every line of a
// transcript, returning the last definitive status and the best name.
func scanTranscript(path string) (status, threadName string) {
	status = wire.StatusIdle
	raw, err := os.ReadFile(path)
	if err != nil {
		return status, ""
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line == "" {
			continue
		}
		entry, ok := transcript.ParseEntryLenient([]byte(line))
		if !ok {
			continue
		}
		if title := extractCustomTitle(entry); title != "" {
			threadName = title
		} else if threadName == "" {
			if name := extractThreadName(entry); name != "" {
				threadName = name
			}
		}
		if s := determineStatus(entry); s != "" {
			status = s
		}
	}
	return status, threadName
}

// --- One-time thread name resolution ---

// resolveThreadNameAsync finds the thread's transcript, reads it off-lock,
// and re-emits with the resolved name under Context.Locked. Fire-once per
// thread (nameResolved), same as the TS adapter's async path.
func (a *Adapter) resolveThreadNameAsync(threadID string, state *threadState) {
	if state.nameResolved {
		return
	}
	state.nameResolved = true
	ctx := a.ctx
	projectsDir := a.ProjectsDir
	go func() {
		dirs, err := os.ReadDir(projectsDir)
		if err != nil {
			return
		}
		for _, dir := range dirs {
			path := filepath.Join(projectsDir, dir.Name(), threadID+".jsonl")
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var threadName string
			for line := range strings.SplitSeq(string(raw), "\n") {
				if line == "" {
					continue
				}
				entry, ok := transcript.ParseEntryLenient([]byte(line))
				if !ok {
					continue
				}
				if title := extractCustomTitle(entry); title != "" {
					threadName = title
					break
				}
				if threadName == "" {
					threadName = extractThreadName(entry)
				}
			}
			if threadName != "" && ctx != nil {
				ctx.Locked(func() {
					// The thread may have ended while we read.
					if cur, ok := a.threads[threadID]; ok && cur == state {
						state.threadName = threadName
						if session := ctx.ResolveSession(state.projectDir); session != "" {
							a.emit(threadID, state, session, false)
						}
					}
				})
			}
			return // found the file, done
		}
	}()
}

// --- Transcript heuristics (tcm policy over agent-ouija entries) ---

var interruptPatterns = []string{
	"[Request interrupted by user",
	"[Request interrupted",
}

const (
	exitCommandPattern  = "<command-name>/exit</command-name>"
	slashCommandPattern = "<command-name>/"
)

var noiseUserPrefixes = []string{
	"<local-command-caveat>",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"<bash-input>",
	"<bash-stdout>",
	"<bash-stderr>",
	"<system-reminder>",
	"<task-notification>",
}

// contentItem is the minimal content-block view the heuristics need.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// contentItems decodes Entry.Message.Content: a JSON string becomes one
// text item; an array decodes leniently; anything else yields nothing.
func contentItems(raw json.RawMessage) []contentItem {
	if len(raw) == 0 {
		return nil
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []contentItem{{Type: "text", Text: s}}
		}
	case '[':
		var items []contentItem
		if json.Unmarshal(raw, &items) == nil {
			return items
		}
	}
	return nil
}

// determineStatus is the seed's status heuristic over one entry ("" for
// control/metadata entries) — a direct port of the TS determineStatus.
func determineStatus(e transcript.Entry) string {
	role := e.Message.Role
	if role == "" {
		return ""
	}
	items := contentItems(e.Message.Content)

	if role == "assistant" {
		for _, c := range items {
			if c.Type == "tool_use" || c.Type == "thinking" {
				return wire.StatusRunning
			}
		}
		switch {
		case e.Message.StopReason == nil || *e.Message.StopReason == "":
			return wire.StatusRunning
		case *e.Message.StopReason == "end_turn":
			return wire.StatusDone
		case *e.Message.StopReason == "tool_use":
			return wire.StatusRunning
		default:
			return wire.StatusDone
		}
	}

	if role == "user" {
		text := firstText(items)
		if text != "" {
			for _, p := range interruptPatterns {
				if strings.HasPrefix(text, p) {
					return wire.StatusInterrupted
				}
			}
			if strings.Contains(text, exitCommandPattern) {
				return wire.StatusDone
			}
			if strings.Contains(text, slashCommandPattern) {
				return ""
			}
			for _, p := range noiseUserPrefixes {
				if strings.HasPrefix(text, p) {
					return ""
				}
			}
		}
		return wire.StatusRunning
	}

	return ""
}

func firstText(items []contentItem) string {
	for _, c := range items {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// extractThreadName pulls a display name from a user entry, rejecting
// markup/JSON/interrupt lines, sanitized and width-capped at 80 cells.
func extractThreadName(e transcript.Entry) string {
	if e.Message.Role != "user" {
		return ""
	}
	text := firstText(contentItems(e.Message.Content))
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "<") || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[Request") {
		return ""
	}
	return textutil.TruncateToWidth(textutil.SanitizeForDisplay(text), 80)
}

func extractCustomTitle(e transcript.Entry) string {
	if e.Type == "custom-title" {
		return e.CustomTitle
	}
	return ""
}

// decodeProjectDir decodes Claude's encoded project dir name back to a
// path: the naive dash→slash form when it exists on disk, otherwise the
// __encoded__: sentinel the server's resolver matches by re-encoding.
func decodeProjectDir(encoded string) string {
	naive := strings.ReplaceAll(encoded, "-", "/")
	if info, err := os.Stat(naive); err == nil && info.IsDir() {
		return naive
	}
	return "__encoded__:" + encoded
}

// --- Tool descriptions (ported from seance ctl.zig via the TS adapter) ---

// ToolDescription generates a human-readable description of the current
// tool activity. Every interpolated value is sanitized + width-capped at
// the leaf, so pasted ANSI or wide-char paths can't disturb the row budget.
func ToolDescription(toolName string, toolInput map[string]json.RawMessage) string {
	if toolName == "" {
		return ""
	}
	switch toolName {
	case "Read":
		return fileDesc("Reading", toolInput)
	case "Edit":
		return fileDesc("Editing", toolInput)
	case "Write":
		return fileDesc("Writing", toolInput)
	case "Bash":
		if cmd := safeStr(toolInput, "command"); cmd != "" {
			return "Running " + textutil.TruncateToWidth(cmd, 30)
		}
		return "Running command"
	case "Glob", "Grep":
		if pattern := safeStr(toolInput, "pattern"); pattern != "" {
			return "Searching " + textutil.TruncateToWidth(pattern, 30)
		}
		return "Searching"
	case "Agent":
		if desc := safeStr(toolInput, "description"); desc != "" {
			return textutil.TruncateToWidth(desc, 40)
		}
		return "Agent"
	case "WebFetch":
		return "Fetching URL"
	case "WebSearch":
		if query := safeStr(toolInput, "query"); query != "" {
			return "Search: " + textutil.TruncateToWidth(query, 30)
		}
		return "Searching web"
	case "AskUserQuestion":
		if q := safeStr(toolInput, "question"); q != "" {
			return "Question: " + textutil.TruncateToWidth(q, 50)
		}
		return "Asking question"
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

func fileDesc(verb string, input map[string]json.RawMessage) string {
	if fp := safeStr(input, "file_path"); fp != "" {
		return verb + " " + filepath.Base(fp)
	}
	return verb
}
