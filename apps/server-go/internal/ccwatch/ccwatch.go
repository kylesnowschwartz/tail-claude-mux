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
	"log"
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
	// lastToolVerb is the structured verb for lastToolDescription — same
	// lifecycle.
	lastToolVerb string
	// subagent from sessions/<pid>.json `agent`, "" when the parent thread
	// is in control.
	subagent string
	// nonTmuxLogged fires once per thread: after the first "dropped" hook
	// from a classified non-tmux agent (registry Kind != "interactive", e.g.
	// claude bg-spare), further drops for the same thread stay silent
	// instead of re-logging on every hook.
	nonTmuxLogged bool
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

// shortThread is the last-4 thread id used across tcm's log surfaces.
func shortThread(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

// Start binds the context and runs the cold-start seed. Call with the
// server lock held.
func (a *Adapter) Start(ctx *Context) {
	a.ctx = ctx
	a.seedFromJSONL()
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
		if state.pid == 0 {
			log.Printf("cc-hook %s: pid unresolved (reported=%d, snapshot=%dB)", shortThread(threadID), payload.PID, len(payload.ProcessSnapshot))
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
		// Silent drops cost real debugging time; say which channel failed —
		// unless the registry says this pid is a known non-tmux agent (bg
		// spare pool, headless run, IDE instance): those hooks fire on every
		// event forever since no pane will ever claim them, so log once per
		// thread rather than flooding on every hook. The file must identify
		// THIS thread (sessionId match) — after pid reuse a stale bg-kind
		// file would otherwise mute a real routing failure for the new
		// process. A missing/unreadable file, a sessionId mismatch, or an
		// "interactive" kind stays loud — still a real routing signal.
		if state.pid != 0 {
			if file := a.readSessionFile(state.pid); file != nil && file.SessionID == threadID && file.Kind != "interactive" {
				if !state.nonTmuxLogged {
					state.nonTmuxLogged = true
					log.Printf("cc-hook %s: non-tmux claude (kind=%s), ignoring", shortThread(threadID), file.Kind)
				}
			} else {
				log.Printf("cc-hook %s: dropped — pid %d resolved but no pane owns it", shortThread(threadID), state.pid)
			}
		} else {
			log.Printf("cc-hook %s: dropped — no pid, cwd %q matches no session", shortThread(threadID), payload.Cwd)
		}
		return
	}

	// Refresh subagent + registry session name from sessions/<pid>.json;
	// failures leave prior values in place (preserved through transient
	// errors).
	a.refreshFromRegistry(threadID, state)

	// SessionEnd bypasses the dedup below: a prior Stop already set done,
	// so dedup would swallow the end signal and leave a ghost until prune.
	if payload.Event == "SessionEnd" {
		state.status = newStatus
		state.lastToolDescription = ""
		state.lastToolVerb = ""
		a.emit(threadID, state, session, emitEnded)
		delete(a.threads, threadID)
		return
	}

	hasToolContext := payload.Event == "PreToolUse" || payload.Event == "PermissionRequest"
	// PreToolUse fires exactly once per tool call, BEFORE the permission
	// system evaluates (its hook output can allow/deny), and never re-fires
	// after approval — the prompted sequence is PreToolUse →
	// PermissionRequest → [approve] → PostToolUse. So PreToolUse alone marks
	// the invocation; PermissionRequest only refreshes the visible
	// description for the same already-counted call.
	kind := emitUpdate
	if payload.Event == "PreToolUse" {
		kind = emitInvoked
	}
	if hasToolContext {
		state.lastToolDescription = ToolDescription(payload.ToolName, payload.ToolInput)
		state.lastToolVerb = ToolVerb(payload.ToolName)
	} else if payload.Event != "PostToolUse" {
		// Clear on non-tool events; PostToolUse keeps the prior description
		// (a tool just finished).
		state.lastToolDescription = ""
		state.lastToolVerb = ""
	}

	// Dedup: skip when status is unchanged — except new threads and tool
	// events (fresh tool description).
	if state.status == newStatus && !isNewThread && !hasToolContext {
		return
	}

	state.status = newStatus
	a.emit(threadID, state, session, kind)
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
		Agent:           "claude-code",
		Session:         session,
		Status:          state.status,
		TS:              a.now(),
		ThreadID:        threadID,
		ThreadName:      state.threadName,
		ToolDescription: state.lastToolDescription,
		ToolVerb:        state.lastToolVerb,
		ToolInvoked:     kind == emitInvoked,
		PID:             state.pid,
		Subagent:        state.subagent,
		Ended:           kind == emitEnded,
	})
}

// --- sessions/<pid>.json (agent-ouija registry format) ---

// SessionIDForPid returns the sessionId recorded in sessions/<pid>.json,
// "" when the file is missing or undecodable. The server's agent-pane
// resolver uses it to match a candidate claude pid to a tracker threadId
// (index.ts resolveClaudeCodePane).
func (a *Adapter) SessionIDForPid(pid int) string {
	id, _ := a.SessionInfoForPid(pid)
	return id
}

// SessionInfoForPid returns the sessionId and display name recorded in
// sessions/<pid>.json (both "" when the file is missing or undecodable).
// One read serves both — the pane scan wants the pair. The name is
// sanitized and width-capped like transcript-derived thread names
// (extractThreadName): registry names come from raw user prompts and can
// otherwise run hundreds of columns into the sidebar.
func (a *Adapter) SessionInfoForPid(pid int) (sessionID, name string) {
	if l := a.readSessionFile(pid); l != nil {
		return l.SessionID, textutil.SanitizeSessionName(l.Name)
	}
	return "", ""
}

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

// refreshFromRegistry refreshes state.subagent and state.threadName from
// sessions/<pid>.json. The registry name is the user-facing session name
// (renamable live) and outranks the transcript-derived fallback; an empty
// registry name leaves the transcript name in place.
//
// PID precedence: (1) state.pid from the process-ancestry walker; (2) the
// sessions/-walk fallback. PID-reuse detection: the file's sessionId must
// match the thread — a mismatch clears the cached pid for re-resolution. A
// missing file is NOT reuse (may be transient) and never clobbers the pid.
func (a *Adapter) refreshFromRegistry(threadID string, state *threadState) {
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
	if cached.Name != "" {
		state.threadName = cached.Name
	}
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
			if latestStatus == wire.StatusIdle || wire.IsTerminalStatus(latestStatus) {
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

// resolveThreadNameAsync finds the thread's transcript, reads it off-lock
// (through the same scanTranscript the seed uses), and re-emits with the
// resolved name under Context.Locked. Fire-once per thread (nameResolved),
// same as the TS adapter's async path.
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
			if _, err := os.Stat(path); err != nil {
				continue
			}
			_, threadName := scanTranscript(path)
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
// markup/JSON/interrupt lines, sanitized and width-capped.
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
	return textutil.SanitizeSessionName(text)
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

// ToolVerb maps a tool name to its shared.ts MetadataVerb, from the same
// switch ToolDescription uses. The watcher knows the tool name; tagging here
// spares renderers from regex-guessing the verb back out of the message
// (the TUI's classify.ts remains the fallback for untagged producers).
// Unknown/custom tools return "".
func ToolVerb(toolName string) string {
	switch toolName {
	case "Read":
		return "read"
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Bash":
		return "run"
	case "Glob", "Grep":
		return "search"
	case "Agent", "Task":
		return "task"
	case "WebFetch", "WebSearch":
		return "web"
	case "Skill":
		return "skill"
	default:
		return ""
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
