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
	routingPID          int
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
	// the terminal Stop hook is dropped, and delegate status is stuck at idle.
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

// RolloutForFollowup resolves a tracked thread directly. Older tracker entries
// without a thread ID fall back to the newest rollout for cwd.
func (a *Adapter) RolloutForFollowup(cwd, trackedThreadID string) (path, threadID string, err error) {
	rollouts, err := discover.DiscoverRollouts(a.SessionsDir)
	if err != nil {
		return "", "", err
	}
	for _, candidate := range rollouts {
		if trackedThreadID != "" {
			if candidate.SessionID == trackedThreadID {
				return candidate.Path, candidate.SessionID, nil
			}
			continue
		}
		rolloutDir, err := followupRolloutCwd(candidate.Path)
		if err != nil {
			return "", "", err
		}
		if rolloutDir == cwd {
			return candidate.Path, candidate.SessionID, nil
		}
	}
	return "", "", nil
}

// RolloutForThread resolves the rollout pinned to a tracked Codex thread.
// It never falls back to cwd matching.
func (a *Adapter) RolloutForThread(threadID string) (string, error) {
	path, _, err := a.RolloutForFollowup("", threadID)
	return path, err
}

func followupRolloutCwd(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	meta, ok, err := rollout.SessionMeta(file)
	if err != nil || !ok {
		return "", err
	}
	return meta.Payload.Cwd, nil
}

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

	if state.routingPID == 0 && payload.PID != 0 && payload.ProcessSnapshot != "" {
		processes := procwalk.ParseProcessSnapshot(payload.ProcessSnapshot)
		resolved := procwalk.ResolveAgentSessionPid(payload.PID, codexCmdRE, processes)
		if resolved != payload.PID {
			state.routingPID = resolved
		} else if info, ok := processes[payload.PID]; ok && codexCmdRE.MatchString(info.Command) {
			state.routingPID = payload.PID
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
	if state.routingPID != 0 && state.pid == 0 {
		if !state.dropLogged {
			log.Printf("codex-hook %s: dropped, pid %d does not own thread", shortThread(threadID), state.routingPID)
			state.dropLogged = true
		}
		if payload.Event == "Stop" {
			state.recheckOwnership = true
			a.resolveThreadNameAsync(threadID, state)
		}
		return
	}
	var evidence rollout.State
	if claim.NeedsEvidence() {
		evidence = a.trailingStateForThread(state.pid, threadID)
	}
	newStatus := verdictWire(rollout.Reconcile(claim, evidence))

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
			if session := a.resolveStateSession(state, state.projectDir); session != "" {
				a.emit(threadID, state, session, emitUpdate)
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
