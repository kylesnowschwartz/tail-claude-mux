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
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kylesnowschwartz/agent-ouija/codex/discover"
	"github.com/kylesnowschwartz/agent-ouija/codex/rollout"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/procwalk"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const staleMS = 5 * 60 * 1000

var codexCmdRE = regexp.MustCompile(`(?i)(?:^|/)codex($|[\s/])`)

type Context struct {
	ResolveSession      func(projectDir string) string
	ResolveSessionByPid func(pid int) string
	// PaneForThread reports the Codex pane the pane scan linked to a thread
	// id. It routes hooks whose sender process is in no tmux pane, such as
	// the shared Codex app-server that runs hooks for every attached TUI.
	PaneForThread func(threadID string) (ThreadPane, bool)
	Emit          func(ev wire.AgentEvent)
	Locked        func(fn func())
}

// ThreadPane is the tmux session and pane process of a scanned Codex pane.
type ThreadPane struct {
	Session string
	PID     int
}

const acpOriginator = "@agentclientprotocol/codex-acp"

type threadState struct {
	rolloutChecked      bool
	acpRollout          bool
	status              string
	threadName          string
	projectDir          string
	nameFromIndex       bool
	nameLookupInFlight  bool
	pid                 int
	routingPID          int
	parentPID           int
	ownershipChecked    bool
	recheckOwnership    bool
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
	// lsof reports each rollout's real path with symlinks resolved (e.g. when
	// ~/.codex is a dotfiles symlink to ~/Code/dotfiles/codex). SessionsDir
	// arrives as the pre-resolution path, so isRolloutPath's filepath.Rel
	// against it rejects every lsof rollout path — ownership never resolves,
	// the terminal Stop hook is dropped, and agent status is stuck at idle.
	// Resolve once here so the comparison base matches lsof's output.
	if resolved, err := filepath.EvalSymlinks(sessionsDir); err == nil {
		sessionsDir = resolved
	}
	return &Adapter{
		SessionsDir:      sessionsDir,
		SessionIndexPath: sessionIndexPath,
		threads:          map[string]*threadState{},
		now:              func() int64 { return time.Now().UnixMilli() },
		openFilesForPID:  lsofPathsForPID,
	}
}

func (a *Adapter) Name() string { return "codex" }

func (a *Adapter) Start(ctx *Context) {
	a.ctx = ctx
	a.seedFromJSONL()
}

func (a *Adapter) HandleHook(payload wire.HookPayload) {
	if payload.Agent != "codex" || a.ctx == nil {
		return
	}
	claim, ok := rollout.ClaimForHookEvent(payload.Event)
	if !ok {
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

	// Every hook with a process snapshot names the codex process that sent
	// it. A resumed thread (`codex resume`) keeps its id in a new process, so
	// a different process re-routes the thread and re-checks ownership; a
	// snapshot with no codex process keeps the prior routing pid.
	if payload.PID != 0 && payload.ProcessSnapshot != "" {
		processes := procwalk.ParseProcessSnapshot(payload.ProcessSnapshot)
		resolved := procwalk.ResolveAgentSessionPid(payload.PID, codexCmdRE, processes)
		senderPID := 0
		if resolved != payload.PID {
			senderPID = resolved
		} else if info, ok := processes[payload.PID]; ok && codexCmdRE.MatchString(info.Command) {
			senderPID = payload.PID
		}
		if senderPID != 0 && senderPID != state.routingPID {
			state.routingPID = senderPID
			state.parentPID = procwalk.ResolveAncestorPid(senderPID, ccwatch.IsClaudeCommand, processes)
			state.pid = 0
			state.ownershipChecked = false
			state.dropLogged = false
		}
		if state.routingPID == 0 {
			log.Printf("codex-hook %s: pid unresolved (reported=%d, snapshot=%dB)", shortThread(threadID), payload.PID, len(payload.ProcessSnapshot))
		}
	}
	shouldRecheck := state.recheckOwnership && (payload.Event == "SessionStart" || payload.Event == "UserPromptSubmit")
	if state.routingPID != 0 && (!state.ownershipChecked || shouldRecheck) {
		state.ownershipChecked = true
		state.recheckOwnership = false
		if a.rolloutPathForPID(state.routingPID, threadID) != "" {
			state.pid = state.routingPID
		}
	}
	// A sender that holds no rollout for the thread routes only through the
	// pane the scan linked to the thread. The pane lookup runs on every hook,
	// so a thread dropped before its pane is linked routes once it is.
	senderOwnsThread := state.routingPID == 0 || state.pid != 0
	if !senderOwnsThread {
		if payload.Event == "Stop" {
			state.recheckOwnership = true
		}
		if _, ok := a.paneForThread(threadID); !ok {
			if !state.dropLogged {
				log.Printf("codex-hook %s: dropped, pid %d does not own thread", shortThread(threadID), state.routingPID)
				state.dropLogged = true
			}
			if payload.Event == "Stop" {
				a.resolveThreadNameAsync(threadID, state)
			}
			return
		}
	}
	if state.parentPID == 0 {
		if !state.rolloutChecked {
			path := a.rolloutPathForPID(state.pid, threadID)
			if path == "" {
				path = a.rolloutPathForThread(threadID)
			}
			if path != "" {
				state.acpRollout = isACPRollout(path)
				state.rolloutChecked = true
			}
		}
		if state.acpRollout {
			return
		}
	}
	var evidence rollout.State
	if claim.NeedsEvidence() {
		evidence = a.trailingStateForThread(state.pid, threadID)
	}
	newStatus := verdictWire(rollout.Reconcile(claim, evidence))

	session, emitPID := "", state.pid
	if senderOwnsThread {
		session = a.resolveStateSession(state, payload.Cwd)
	}
	if session == "" {
		if pane, ok := a.paneForThread(threadID); ok {
			session, emitPID = pane.Session, pane.PID
		}
	}
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
		state.lastToolVerb = ccwatch.ToolVerb(payload.ToolName, payload.ToolInput)
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
	a.emit(threadID, state, session, emitPID, kind)
	if payload.Event == "Stop" {
		a.resolveThreadNameAsync(threadID, state)
	}
}

func (a *Adapter) trailingStateForThread(pid int, threadID string) rollout.State {
	path := a.rolloutPathForPID(pid, threadID)
	if path == "" && threadID != "" {
		path = a.rolloutPathForThread(threadID)
	}
	state, err := trailingRolloutState(path)
	if err != nil {
		return rollout.State{}
	}
	return state
}

type emitKind int

const (
	emitUpdate emitKind = iota
	emitInvoked
)

// emit publishes the thread's state to the tmux session. pid is the codex
// process the row belongs to: the rollout-owning sender, or the pane process
// of the pane the thread is linked to.
func (a *Adapter) emit(threadID string, state *threadState, session string, pid int, kind emitKind) {
	if state.parentPID != 0 {
		return
	}

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
		PID:             pid,
	})
}

// paneForThread asks the server for the pane the scan linked to the thread.
func (a *Adapter) paneForThread(threadID string) (ThreadPane, bool) {
	if a.ctx.PaneForThread == nil {
		return ThreadPane{}, false
	}
	pane, ok := a.ctx.PaneForThread(threadID)
	if !ok || pane.Session == "" {
		return ThreadPane{}, false
	}
	return pane, true
}

// routeThread returns the session and row pid for an event about the thread,
// preferring the sender's session and falling back to the linked pane.
func (a *Adapter) routeThread(threadID string, state *threadState) (string, int) {
	if session := a.resolveStateSession(state, state.projectDir); session != "" {
		return session, state.pid
	}
	if pane, ok := a.paneForThread(threadID); ok {
		return pane.Session, pane.PID
	}
	return "", 0
}

func (a *Adapter) resolveStateSession(state *threadState, fallbackCwd string) string {
	if state.routingPID != 0 {
		return a.ctx.ResolveSessionByPid(state.routingPID)
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
			if session, pid := a.routeThread(threadID, state); session != "" {
				a.emit(threadID, state, session, pid, emitUpdate)
			}
		})
	}()
}

func lookupThreadName(path, threadID string) string {
	names, err := discover.ThreadNames(path)
	if err != nil {
		return ""
	}
	return textutil.SanitizeSessionName(names[threadID])
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
// three-second pane scan launches lsof only once per Codex process. The
// rollout records no permission-prompt answer, so Prompt stays empty.
//
// A Codex TUI attached to the shared app-server holds no rollout open; its
// pane title names its thread, so the title resolves through the session index
// and the thread's newest rollout supplies the verdict.
func (a *Adapter) ScanStateForPid(pid int, paneTitle string) tracker.ScanState {
	path := a.rolloutPathForPID(pid, "")
	threadID, name := a.sessionInfoForRollout(path)
	if threadID == "" {
		threadID, name = a.threadForPaneTitle(paneTitle)
		if threadID == "" {
			return tracker.ScanState{}
		}
		path = a.rolloutPathForThread(threadID)
	}
	state := tracker.ScanState{ThreadID: threadID, ThreadName: name}
	if path != "" {
		state.Verdict = scanRolloutPath(path)
	}
	return state
}

// threadForPaneTitle matches a pane title against session-index thread names.
// Codex titles its terminal "<thread name> | <project name>", so a title
// matches a name it equals or that it starts with followed by " | ". The
// longest matching name wins; among ids sharing it, the newest updated_at wins.
func (a *Adapter) threadForPaneTitle(title string) (threadID, name string) {
	if title == "" {
		return "", ""
	}
	records, err := discover.ThreadRecords(a.SessionIndexPath)
	if err != nil {
		return "", ""
	}
	var best discover.ThreadRecord
	bestLen := 0
	for id, record := range records {
		matched := 0
		for _, candidate := range []string{record.Name, textutil.SanitizeSessionName(record.Name)} {
			if len(candidate) > matched && titleNamesThread(title, candidate) {
				matched = len(candidate)
			}
		}
		if matched == 0 {
			continue
		}
		if matched > bestLen || matched == bestLen && record.UpdatedAt.After(best.UpdatedAt) {
			threadID, best, bestLen = id, record, matched
		}
	}
	if bestLen == 0 {
		return "", ""
	}
	return threadID, textutil.SanitizeSessionName(best.Name)
}

func titleNamesThread(title, name string) bool {
	if name == "" {
		return false
	}
	for _, t := range []string{title, withoutRunStateMark(title)} {
		if t == name || strings.HasPrefix(t, name+" | ") {
			return true
		}
	}
	return false
}

// withoutRunStateMark drops the one-symbol run-state mark Codex puts before
// the thread name while a turn runs, as in "⠼ Fix bug | repo".
func withoutRunStateMark(title string) string {
	mark, size := utf8.DecodeRuneInString(title)
	if size == 0 || unicode.IsLetter(mark) || unicode.IsDigit(mark) || unicode.IsSpace(mark) {
		return title
	}
	rest, ok := strings.CutPrefix(title[size:], " ")
	if !ok {
		return title
	}
	return rest
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

func rolloutStatusForPath(path string) string {
	state, err := trailingRolloutState(path)
	if err != nil {
		return ""
	}
	return wireStatus(state.Status)
}

func verdictWire(verdict rollout.Verdict) string {
	switch verdict {
	case rollout.VerdictIdle:
		return wire.StatusIdle
	case rollout.VerdictRunning:
		return wire.StatusRunning
	case rollout.VerdictWaiting:
		return wire.StatusWaiting
	case rollout.VerdictDone:
		return wire.StatusDone
	case rollout.VerdictInterrupted:
		return wire.StatusInterrupted
	case rollout.VerdictError:
		return wire.StatusError
	default:
		return ""
	}
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

// rolloutPathForThread returns the thread's most recently modified rollout;
// DiscoverRollouts sorts newest first.
func (a *Adapter) rolloutPathForThread(threadID string) string {
	rollouts, _ := discover.DiscoverRollouts(a.SessionsDir)
	for _, candidate := range rollouts {
		if candidate.SessionID == threadID {
			return candidate.Path
		}
	}
	return ""
}

func (a *Adapter) isRolloutPath(path string) bool {
	rel, err := filepath.Rel(a.SessionsDir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && filepath.Ext(path) == ".jsonl"
}

func isACPRollout(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	meta, ok, err := rollout.SessionMeta(file)
	return err == nil && ok && meta.Payload.Originator == acpOriginator
}

func isPrimaryRollout(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	meta, ok, err := rollout.SessionMeta(file)
	return err == nil && ok && meta.Payload.Source.Kind == "cli"
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
	rollouts, _ := discover.DiscoverRollouts(a.SessionsDir)
	for _, candidate := range rollouts {
		if nowMS-candidate.ModTime.UnixMilli() > staleMS {
			continue
		}
		if isACPRollout(candidate.Path) {
			continue
		}
		threadID := candidate.SessionID
		if _, known := a.threads[threadID]; known {
			continue
		}
		rolloutState, err := trailingRolloutState(candidate.Path)
		if err != nil {
			continue
		}
		status, cwd := wireStatus(rolloutState.Status), rolloutState.Cwd
		if status == wire.StatusIdle || wire.IsTerminalStatus(status) || cwd == "" {
			continue
		}
		session := a.ctx.ResolveSession(cwd)
		if session == "" {
			continue
		}
		name := lookupThreadName(a.SessionIndexPath, threadID)
		state := &threadState{status: status, threadName: name, projectDir: cwd, nameFromIndex: name != ""}
		a.threads[threadID] = state
		a.ctx.Emit(wire.AgentEvent{Agent: "codex", Session: session, Status: status, TS: candidate.ModTime.UnixMilli(), ThreadID: threadID, ThreadName: name})
	}
}

func threadIDFromPath(path string) string {
	return discover.SessionIDFromPath(path)
}

func trailingRolloutState(path string) (rollout.State, error) {
	if path == "" {
		return rollout.State{}, errors.New("rollout path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return rollout.State{}, err
	}
	defer file.Close()
	return rollout.TrailingState(file)
}

func wireStatus(status rollout.Status) string {
	switch status {
	case rollout.Idle:
		return wire.StatusIdle
	case rollout.Running:
		return wire.StatusRunning
	case rollout.Done:
		return wire.StatusDone
	case rollout.Interrupted:
		return wire.StatusInterrupted
	case rollout.Error:
		return wire.StatusError
	default:
		return ""
	}
}
