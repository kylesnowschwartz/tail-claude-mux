// Package codexwatch adapts Codex lifecycle hooks and recent rollout files
// into tcm agent events.
//
// Live hooks provide immediate status updates. Rollout JSONL also provides a
// bounded cold-start seed, scan-time process identity, and a durable liveness
// probe when hooks are silent. session_index.jsonl resolves display names.
//
// The Adapter is not safe for concurrent use. The server serializes hook and
// seed work under its state lock. Name lookup reads off-lock and re-enters via
// Context.Locked.
package codexwatch

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/procwalk"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const staleMS = 5 * 60 * 1000

var codexCmdRE = regexp.MustCompile(`(?i)(?:^|/)codex($|[\s/])`)
var rolloutIDRE = regexp.MustCompile(`[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

var hookStatusMap = map[string]string{
	"SessionStart":      wire.StatusIdle,
	"UserPromptSubmit":  wire.StatusRunning,
	"PreToolUse":        wire.StatusRunning,
	"PostToolUse":       wire.StatusRunning,
	"PermissionRequest": wire.StatusWaiting,
	// Stop is a turn boundary, not a session end: the codex process stays
	// alive at its prompt, so the row rests at idle like a live agent.
	// Trade-off: idle is not terminal, so a turn finishing in an inactive
	// session sets no unseen badge (tracker's unseen policy keys on
	// terminal/waiting).
	"Stop": wire.StatusIdle,
}

type Context struct {
	ResolveSession      func(projectDir string) string
	ResolveSessionByPid func(pid int) string
	Emit                func(ev wire.AgentEvent)
	Locked              func(fn func())
}

type threadState struct {
	status              string
	threadName          string
	projectDir          string
	nameFromIndex       bool
	nameLookupInFlight  bool
	pid                 int
	dropLogged          bool
	lastToolDescription string
	lastToolVerb        string
}

type Adapter struct {
	SessionsDir      string
	SessionIndexPath string

	ctx             *Context
	threads         map[string]*threadState
	now             func() int64
	openFilesForPID func(pid int) []string
}

func New(sessionsDir, sessionIndexPath string) *Adapter {
	return &Adapter{
		SessionsDir:      sessionsDir,
		SessionIndexPath: sessionIndexPath,
		threads:          map[string]*threadState{},
		now:              func() int64 { return time.Now().UnixMilli() },
		openFilesForPID:  lsofPathsForPID,
	}
}

func (a *Adapter) Name() string { return "codex" }

// RolloutForFollowup resolves a tracked thread directly. Older tracker entries
// without a thread ID fall back to the newest rollout for cwd.
func (a *Adapter) RolloutForFollowup(cwd, trackedThreadID string) (path, threadID string, err error) {
	var newest time.Time
	err = filepath.WalkDir(a.SessionsDir, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		candidateThreadID := threadIDFromPath(candidate)
		if candidateThreadID == "" {
			return nil
		}
		if trackedThreadID != "" {
			if candidateThreadID == trackedThreadID {
				path, threadID = candidate, candidateThreadID
				return filepath.SkipAll
			}
			return nil
		}
		file, err := os.Open(candidate)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		matches := false
		if scanner.Scan() {
			rolloutDir, parseErr := rolloutCwd(scanner.Bytes())
			if parseErr != nil {
				_ = file.Close()
				return parseErr
			}
			matches = rolloutDir == cwd
		}
		if scanErr := scanner.Err(); scanErr != nil {
			_ = file.Close()
			return scanErr
		}
		if err := file.Close(); err != nil {
			return err
		}
		if !matches {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if path == "" || info.ModTime().After(newest) {
			path, threadID, newest = candidate, candidateThreadID, info.ModTime()
		}
		return nil
	})
	return path, threadID, err
}

func rolloutCwd(line []byte) (string, error) {
	var entry struct {
		Cwd     string `json:"cwd"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return "", err
	}
	if entry.Cwd != "" {
		return entry.Cwd, nil
	}
	return entry.Payload.Cwd, nil
}

func (a *Adapter) Start(ctx *Context) {
	a.ctx = ctx
	a.seedFromJSONL()
}

func (a *Adapter) HandleHook(payload wire.HookPayload) {
	if payload.Agent != "codex" || a.ctx == nil {
		return
	}
	newStatus := hookStatusMap[payload.Event]
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

	if state.pid == 0 && payload.PID != 0 && payload.ProcessSnapshot != "" {
		processes := procwalk.ParseProcessSnapshot(payload.ProcessSnapshot)
		resolved := procwalk.ResolveAgentSessionPid(payload.PID, codexCmdRE, processes)
		if resolved != payload.PID {
			state.pid = resolved
		} else if info, ok := processes[payload.PID]; ok && codexCmdRE.MatchString(info.Command) {
			state.pid = payload.PID
		}
		if state.pid == 0 {
			log.Printf("codex-hook %s: pid unresolved (reported=%d, snapshot=%dB)", shortThread(threadID), payload.PID, len(payload.ProcessSnapshot))
		}
	}
	if payload.Event == "Stop" {
		if status := a.rolloutStatusForThread(state.pid, threadID); wire.IsTerminalStatus(status) {
			newStatus = status
		}
	}

	session := a.resolveStateSession(state, payload.Cwd)
	if session == "" {
		if !state.dropLogged {
			if state.pid != 0 {
				log.Printf("codex-hook %s: dropped, pid %d resolved but no pane owns it", shortThread(threadID), state.pid)
			} else {
				log.Printf("codex-hook %s: dropped, no pid and cwd %q matches no session", shortThread(threadID), payload.Cwd)
			}
			state.dropLogged = true
		}
		if payload.Event == "Stop" {
			a.resolveThreadNameAsync(threadID, state)
		}
		return
	}

	previousDescription := state.lastToolDescription
	hasToolContext := payload.Event == "PreToolUse" || payload.Event == "PermissionRequest"
	kind := emitUpdate
	if payload.Event == "PreToolUse" {
		kind = emitInvoked
	}
	if hasToolContext {
		state.lastToolDescription = ccwatch.ToolDescription(payload.ToolName, payload.ToolInput)
		state.lastToolVerb = ccwatch.ToolVerb(payload.ToolName)
	} else if payload.Event != "PostToolUse" {
		state.lastToolDescription = ""
		state.lastToolVerb = ""
	}

	if payload.Event == "UserPromptSubmit" && !state.nameFromIndex && state.threadName == "" {
		state.threadName = promptThreadName(payload.Prompt)
	}
	visibleToolChange := previousDescription != state.lastToolDescription
	if state.status == newStatus && !isNewThread && !visibleToolChange && kind != emitInvoked {
		if payload.Event == "Stop" {
			a.resolveThreadNameAsync(threadID, state)
		}
		return
	}
	state.status = newStatus
	a.emit(threadID, state, session, kind)
	if payload.Event == "Stop" {
		a.resolveThreadNameAsync(threadID, state)
	}
}

type emitKind int

const (
	emitUpdate emitKind = iota
	emitInvoked
)

func (a *Adapter) emit(threadID string, state *threadState, session string, kind emitKind) {
	a.ctx.Emit(wire.AgentEvent{
		Agent:           "codex",
		Session:         session,
		Status:          state.status,
		TS:              a.now(),
		ThreadID:        threadID,
		ThreadName:      state.threadName,
		ToolDescription: state.lastToolDescription,
		ToolVerb:        state.lastToolVerb,
		ToolInvoked:     kind == emitInvoked,
		PID:             state.pid,
	})
}

func (a *Adapter) resolveStateSession(state *threadState, fallbackCwd string) string {
	if state.pid != 0 {
		return a.ctx.ResolveSessionByPid(state.pid)
	}
	if state.projectDir != "" {
		return a.ctx.ResolveSession(state.projectDir)
	}
	return a.ctx.ResolveSession(fallbackCwd)
}

func shortThread(id string) string {
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
}

func promptThreadName(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if line, _, ok := strings.Cut(prompt, "\n"); ok {
		prompt = line
	}
	return textutil.SanitizeSessionName(strings.TrimSpace(prompt))
}

func (a *Adapter) resolveThreadNameAsync(threadID string, state *threadState) {
	if state.nameLookupInFlight {
		return
	}
	state.nameLookupInFlight = true
	ctx := a.ctx
	indexPath := a.SessionIndexPath
	go func() {
		name := lookupThreadName(indexPath, threadID)
		if ctx == nil {
			return
		}
		ctx.Locked(func() {
			if cur, ok := a.threads[threadID]; !ok || cur != state {
				return
			}
			state.nameLookupInFlight = false
			if name == "" {
				return
			}
			state.nameFromIndex = true
			if state.threadName == name {
				return
			}
			state.threadName = name
			if session := a.resolveStateSession(state, state.projectDir); session != "" {
				a.emit(threadID, state, session, emitUpdate)
			}
		})
	}()
}

func lookupThreadName(path, threadID string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	name := ""
	for line := range strings.SplitSeq(string(raw), "\n") {
		var entry struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.ID == threadID && entry.ThreadName != "" {
			name = textutil.SanitizeSessionName(entry.ThreadName)
		}
	}
	return name
}

// SessionInfoForPid resolves a live Codex process to its primary rollout.
// Codex has no sessions/<pid>.json registry: the process itself is the
// authoritative association because it keeps each active rollout open. A
// process can also own subagent rollouts, so source:"cli" outranks those.
func (a *Adapter) SessionInfoForPid(pid int) (threadID, name string) {
	path := a.rolloutPathForPID(pid, "")
	return a.sessionInfoForRollout(path)
}

// ScanStateForPid resolves identity and status from one rollout lookup so the
// three-second pane scan launches lsof only once per Codex process.
func (a *Adapter) ScanStateForPid(pid int, _ string) (threadID, name string, verdict tracker.ProbeVerdict) {
	path := a.rolloutPathForPID(pid, "")
	threadID, name = a.sessionInfoForRollout(path)
	if threadID == "" {
		return "", "", tracker.ProbeNoSignal
	}
	return threadID, name, scanRolloutPath(path)
}

func (a *Adapter) sessionInfoForRollout(path string) (threadID, name string) {
	if path == "" {
		return "", ""
	}
	threadID = threadIDFromPath(path)
	return threadID, lookupThreadName(a.SessionIndexPath, threadID)
}

// ProbeLiveStatus classifies the latest durable state in a thread's rollout.
// A definitive non-running state ends reconciliation; missing or unreadable
// rollout data leaves the tracker unchanged.
func (a *Adapter) ProbeLiveStatus(pid int, threadID, _ string) tracker.ProbeVerdict {
	path := a.rolloutPathForPID(pid, threadID)
	if path == "" && threadID != "" {
		path = a.rolloutPathForThread(threadID)
	}
	return probeRolloutPath(path)
}

func probeRolloutPath(path string) tracker.ProbeVerdict {
	status := rolloutStatusForPath(path)
	if status == wire.StatusRunning {
		return tracker.ProbeWorking
	}
	if wire.IsTerminalStatus(status) {
		return tracker.ProbeEnded
	}
	return tracker.ProbeNoSignal
}

func scanRolloutPath(path string) tracker.ProbeVerdict {
	switch rolloutStatusForPath(path) {
	case wire.StatusRunning:
		return tracker.ProbeWorking
	case wire.StatusDone:
		return tracker.ProbeDone
	case wire.StatusInterrupted:
		return tracker.ProbeInterrupted
	case wire.StatusError:
		return tracker.ProbeError
	default:
		return tracker.ProbeNoSignal
	}
}

func (a *Adapter) rolloutStatusForThread(pid int, threadID string) string {
	path := a.rolloutPathForPID(pid, threadID)
	if path == "" && threadID != "" {
		path = a.rolloutPathForThread(threadID)
	}
	return rolloutStatusForPath(path)
}

func rolloutStatusForPath(path string) string {
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	status, _ := parseRollout(string(raw))
	return status
}

func (a *Adapter) rolloutPathForPID(pid int, threadID string) string {
	if pid <= 1 || a.openFilesForPID == nil {
		return ""
	}
	var fallback string
	for _, path := range a.openFilesForPID(pid) {
		if !a.isRolloutPath(path) {
			continue
		}
		id := threadIDFromPath(path)
		if id == "" || threadID != "" && id != threadID {
			continue
		}
		if isPrimaryRollout(path) {
			return path
		}
		if fallback == "" {
			fallback = path
		}
	}
	return fallback
}

func (a *Adapter) rolloutPathForThread(threadID string) string {
	var found string
	_ = filepath.WalkDir(a.SessionsDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && threadIDFromPath(path) == threadID {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func (a *Adapter) isRolloutPath(path string) bool {
	rel, err := filepath.Rel(a.SessionsDir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && filepath.Ext(path) == ".jsonl"
}

func isPrimaryRollout(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	for scanner := bufio.NewScanner(file); scanner.Scan(); {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Source json.RawMessage `json:"source"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "session_meta" {
			continue
		}
		var source string
		return json.Unmarshal(entry.Payload.Source, &source) == nil && source == "cli"
	}
	return false
}

func lsofPathsForPID(pid int) []string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, "n"); ok && filepath.IsAbs(path) {
			paths = append(paths, path)
		}
	}
	return paths
}

func (a *Adapter) seedFromJSONL() {
	if a.ctx == nil {
		return
	}
	nowMS := a.now()
	_ = filepath.WalkDir(a.SessionsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		threadID := threadIDFromPath(path)
		if threadID == "" {
			return nil
		}
		info, err := entry.Info()
		if err != nil || nowMS-info.ModTime().UnixMilli() > staleMS {
			return nil
		}
		if _, known := a.threads[threadID]; known {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		status, cwd := parseRollout(string(raw))
		if status == wire.StatusIdle || wire.IsTerminalStatus(status) || cwd == "" {
			return nil
		}
		session := a.ctx.ResolveSession(cwd)
		if session == "" {
			return nil
		}
		name := lookupThreadName(a.SessionIndexPath, threadID)
		state := &threadState{status: status, threadName: name, projectDir: cwd, nameFromIndex: name != ""}
		a.threads[threadID] = state
		a.ctx.Emit(wire.AgentEvent{Agent: "codex", Session: session, Status: status, TS: info.ModTime().UnixMilli(), ThreadID: threadID, ThreadName: name})
		return nil
	})
}

func threadIDFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return rolloutIDRE.FindString(base)
}

type rolloutEntry struct {
	Type    string `json:"type"`
	Payload struct {
		Type  string `json:"type"`
		Role  string `json:"role"`
		Phase string `json:"phase"`
		Cwd   string `json:"cwd"`
	} `json:"payload"`
}

func parseRollout(text string) (status, cwd string) {
	status = wire.StatusIdle
	for line := range strings.SplitSeq(text, "\n") {
		var entry rolloutEntry
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if cwd == "" && entry.Type == "turn_context" {
			cwd = entry.Payload.Cwd
		}
		if next := rolloutStatus(entry); next != "" {
			status = next
		}
	}
	return status, cwd
}

func rolloutStatus(entry rolloutEntry) string {
	if entry.Type == "event_msg" {
		switch entry.Payload.Type {
		case "task_complete":
			return wire.StatusDone
		case "turn_aborted":
			return wire.StatusInterrupted
		case "user_message":
			return wire.StatusRunning
		case "agent_message":
			return assistantStatus(entry.Payload.Phase)
		case "error":
			return wire.StatusError
		}
	}
	if entry.Type == "response_item" {
		switch entry.Payload.Type {
		case "message":
			if entry.Payload.Role == "user" {
				return wire.StatusRunning
			}
			if entry.Payload.Role == "assistant" {
				return assistantStatus(entry.Payload.Phase)
			}
		case "function_call", "function_call_output", "reasoning":
			return wire.StatusRunning
		}
	}
	return ""
}

func assistantStatus(phase string) string {
	if phase == "commentary" {
		return wire.StatusRunning
	}
	return wire.StatusDone
}
