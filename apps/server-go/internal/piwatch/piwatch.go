// Package piwatch derives tcm agent state for Pi coding agent processes.
// Every reading starts from a process the pane scan found. The session file,
// read with agent-ouija's pi packages, supplies the thread name and tool
// calls, and the status of a process that sends no hooks. A process running
// tcm's Pi extension (integrations/pi-extension) reports its session file
// and status through HandleHook; from its first accepted hook on, its status
// comes only from hooks.
//
// ScanStateForPid and Activity run on the pane-scan goroutine outside the
// server lock; Resolve, ProbeLiveStatus and HandleHook run under it. The per-pid
// records they share are guarded by the Adapter's mutex; the per-file read
// offsets belong to the scan goroutine alone.
package piwatch

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/pi/discover"
	"github.com/kylesnowschwartz/agent-ouija/pi/session"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/agentmatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// startMatchWindow is how far a session header's start time may sit from
// the process start time and still name that process's first session.
const startMatchWindow = 2 * time.Second

// pendingHookTTL is how long a hook from a pid the scan has not found yet is
// kept to apply once the scan finds it.
const pendingHookTTL = 10 * time.Second

// Adapter maps Pi processes to their session files and reads status, names
// and tool calls from them.
type Adapter struct {
	// SessionsDir holds one folder per cwd (pidir.Root.SessionsDir).
	SessionsDir string
	// OverrideDir, when set, is a flat folder holding every cwd's files.
	OverrideDir string

	cwdForPID       func(pid int) string
	startTimeForPID func(pid int) time.Time
	now             func() int64

	mu      sync.Mutex
	byPID   map[int]pidRecord
	pending map[int]pendingHook

	files map[string]*fileState
}

// pidRecord is one live Pi process: its cwd and start time, read once, and
// the session file and verdict of its latest scan.
type pidRecord struct {
	cwd     string
	started time.Time
	// session is the tmux session the scan found the pid in.
	session string
	path    string
	verdict tracker.ProbeVerdict

	// hooked is set by the pid's first accepted hook; hookFile is the
	// session file its latest hook named and lastHookAt when that hook
	// arrived. hookStatus is the status its hooks set, beforePrompt the
	// status a dialog interrupted, and sentStatus the last status emitted.
	// checkSettleError asks the next scan whether a settled turn ended in
	// an error; terminalScans counts scans in a row whose file shows a
	// finished turn newer than lastHookAt while hookStatus is running.
	// ended marks a quit.
	hooked           bool
	hookFile         string
	lastHookAt       time.Time
	hookStatus       string
	beforePrompt     string
	sentStatus       string
	checkSettleError bool
	terminalScans    int
	ended            bool
}

// pendingHook is the latest hook from a pid the scan has not found yet.
type pendingHook struct {
	payload wire.HookPayload
	at      time.Time
}

// fileState is the scan goroutine's read position in one session file.
type fileState struct {
	toolOffset    int64
	summaryOffset int64
	summary       session.Summary
	status        string
}

// New returns an Adapter reading sessionsDir, or overrideDir when it is
// non-empty.
func New(sessionsDir, overrideDir string) *Adapter {
	return &Adapter{
		SessionsDir:     sessionsDir,
		OverrideDir:     overrideDir,
		cwdForPID:       lsofCwdForPID,
		startTimeForPID: psStartTimeForPID,
		now:             func() int64 { return time.Now().UnixMilli() },
		byPID:           map[int]pidRecord{},
		pending:         map[int]pendingHook{},
		files:           map[string]*fileState{},
	}
}

// Name is the agent key this adapter serves.
func (a *Adapter) Name() string { return agentmatch.AgentPi }

// ScanStateForPid resolves the pid's current session file and reads its
// identity and trailing status. A hooked pid's file is the one its latest
// hook named, and its reading carries no verdict: hooks own its status. Any
// other pid is re-resolved on every call, so a /new, /resume or /fork in
// the same process is followed.
func (a *Adapter) ScanStateForPid(pid int, _ string) tracker.ScanState {
	a.mu.Lock()
	rec := a.byPID[pid]
	a.mu.Unlock()
	path := rec.hookFile
	if path == "" {
		path = a.sessionPathForPID(pid)
	}
	verdict := tracker.ProbeNoSignal
	if path != "" && !rec.hooked {
		verdict = verdictForPath(path)
	}
	a.mu.Lock()
	if cur, ok := a.byPID[pid]; ok && cur.hookFile == rec.hookFile && cur.hooked == rec.hooked {
		cur.path, cur.verdict = path, verdict
		a.byPID[pid] = cur
	}
	a.mu.Unlock()
	if path == "" {
		return tracker.ScanState{}
	}
	return tracker.ScanState{
		ThreadID:   discover.SessionIDFromPath(path),
		ThreadName: a.threadName(path),
		Verdict:    verdict,
	}
}

// ProbeLiveStatus answers for the file the last scan resolved for pid. A
// hooked pid is working while its hook status is running; any other pid
// reads the file's trailing status. It starts no process.
func (a *Adapter) ProbeLiveStatus(pid int, threadID, _ string) tracker.ProbeVerdict {
	a.mu.Lock()
	rec, ok := a.byPID[pid]
	a.mu.Unlock()
	if !ok || rec.path == "" || threadID != "" && discover.SessionIDFromPath(rec.path) != threadID {
		return tracker.ProbeNoSignal
	}
	if rec.hooked {
		if rec.hookStatus == wire.StatusRunning {
			return tracker.ProbeWorking
		}
		return tracker.ProbeNoSignal
	}
	return verdictForPath(rec.path)
}

// Reading is one scan's look at a pid's session file, read outside the
// server lock. Resolve turns it into events under the lock.
type Reading struct {
	pid  int
	path string
	base wire.AgentEvent
	// tools are the tool calls written since the previous scan, as events
	// without a status.
	tools []wire.AgentEvent
	// fileStatus is the file's trailing status for a file-only pid when it
	// changed since the previous scan; for a hooked pid it is the trailing
	// status every scan, with fileLast the time of the file's last message.
	fileStatus string
	fileLast   time.Time
}

// Activity reads the pid's session file outside the server lock: the tool
// calls written since the previous call and the file's trailing status. At
// first sight of a file the tool offset starts at the end of its last
// complete line, so earlier calls are not replayed. Call it after
// ScanStateForPid for the same pid, on the same goroutine, and pass the
// result to Resolve under the server lock.
func (a *Adapter) Activity(pid int, tmuxSession string) *Reading {
	a.mu.Lock()
	rec, ok := a.byPID[pid]
	a.mu.Unlock()
	if !ok || rec.path == "" || rec.ended || tmuxSession == "" {
		return nil
	}
	fs := a.fileState(rec.path)
	r := &Reading{
		pid:  pid,
		path: rec.path,
		base: wire.AgentEvent{
			Agent:      agentmatch.AgentPi,
			Session:    tmuxSession,
			ThreadID:   discover.SessionIDFromPath(rec.path),
			ThreadName: a.threadName(rec.path),
			PID:        pid,
			TS:         a.now(),
		},
	}
	calls, next, err := session.ReadToolCalls(rec.path, fs.toolOffset)
	if err == nil {
		fs.toolOffset = next
		for _, call := range calls {
			ev := r.base
			ev.ToolInvoked = true
			ev.ToolDescription, ev.ToolVerb = describeTool(call)
			r.tools = append(r.tools, ev)
		}
	}
	if rec.hooked {
		if state, err := session.TrailingState(rec.path); err == nil {
			r.fileStatus, r.fileLast = sessionStatus(state.Status), state.LastMessageAt
		}
		return r
	}
	if status := wireStatus(rec.verdict); status != "" && status != fs.status {
		fs.status = status
		r.fileStatus = status
	}
	return r
}

// Resolve turns a Reading into the events to emit, deciding status with the
// pid's record as it stands now: the tool-call events, then, last, one
// status event when the status differs from the last one sent. A hooked
// pid's events carry its current hook status. Two file checks can change
// that status: after a settled turn, an error at the end of the file makes
// it error; and while it is running, a finished turn in the file newer than
// the pid's last hook, seen on two scans in a row, replaces it. A reading
// of a file the pid no longer reads yields nothing. Call it under the
// server lock.
func (a *Adapter) Resolve(r *Reading) []wire.AgentEvent {
	if r == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.byPID[r.pid]
	if !ok || rec.ended || rec.path != r.path {
		return nil
	}
	status := r.fileStatus
	if rec.hooked {
		if rec.checkSettleError {
			rec.checkSettleError = false
			if rec.hookStatus == wire.StatusDone && r.fileStatus == wire.StatusError {
				rec.hookStatus = wire.StatusError
			}
		}
		if rec.hookStatus == wire.StatusRunning && wire.IsTerminalStatus(r.fileStatus) && r.fileLast.After(rec.lastHookAt) {
			rec.terminalScans++
		} else {
			rec.terminalScans = 0
		}
		if rec.terminalScans >= 2 {
			rec.hookStatus, rec.terminalScans = r.fileStatus, 0
		}
		status = ""
		if rec.hookStatus != rec.sentStatus {
			status = rec.hookStatus
			rec.sentStatus = status
		}
	}
	toolStatus := wire.StatusRunning
	if rec.hooked {
		toolStatus = rec.hookStatus
	}
	a.byPID[r.pid] = rec
	out := make([]wire.AgentEvent, 0, len(r.tools)+1)
	for _, ev := range r.tools {
		ev.Status = toolStatus
		out = append(out, ev)
	}
	if status != "" {
		ev := r.base
		ev.Status = status
		out = append(out, ev)
	}
	return out
}

// HandleHook applies one hook from tcm's Pi extension and returns the event
// to emit now, if any. It reads no file. A hook from a pid the scan has not
// found as a live Pi is kept for pendingHookTTL and applied when the scan
// finds it; a newer hook from the same pid replaces it. See applyHook for
// the status each hook sets; session_shutdown with reason quit ends the
// row.
func (a *Adapter) HandleHook(p wire.HookPayload) []wire.AgentEvent {
	if p.Agent != agentmatch.AgentPi || p.PID == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.UnixMilli(a.now())
	rec, ok := a.byPID[p.PID]
	if !ok {
		a.pending[p.PID] = pendingHook{payload: p, at: now}
		return nil
	}
	applyHook(&rec, p, now)
	a.byPID[p.PID] = rec
	if rec.session == "" {
		return nil
	}
	ev := wire.AgentEvent{
		Agent:    agentmatch.AgentPi,
		Session:  rec.session,
		ThreadID: discover.SessionIDFromPath(rec.hookFile),
		PID:      p.PID,
		TS:       now.UnixMilli(),
	}
	if ev.ThreadID == "" {
		ev.ThreadID = p.SessionID
	}
	if rec.ended {
		ev.Ended = true
		return []wire.AgentEvent{ev}
	}
	if rec.hookStatus == rec.sentStatus {
		return nil
	}
	rec.sentStatus = rec.hookStatus
	a.byPID[p.PID] = rec
	ev.Status = rec.hookStatus
	return []wire.AgentEvent{ev}
}

// applyHook folds one hook, received at, into a pid's record. A hook naming
// a different session file starts the new thread from no status. Status:
// agent_start → running; ui_prompt_start → waiting, remembering the status
// it interrupts; ui_prompt_end → that remembered status; agent_settled →
// interrupted when aborted, else done, with the next scan checking the file
// for an error; session_shutdown with reason quit → ended. A pid whose
// hooks have set no status yet is idle.
func applyHook(rec *pidRecord, p wire.HookPayload, at time.Time) {
	rec.hooked = true
	rec.lastHookAt = at
	if p.TranscriptPath != "" && p.TranscriptPath != rec.hookFile {
		if rec.hookFile != "" {
			rec.hookStatus, rec.beforePrompt, rec.sentStatus = "", "", ""
			rec.checkSettleError, rec.terminalScans = false, 0
		}
		rec.hookFile = p.TranscriptPath
		rec.path = p.TranscriptPath
	}
	switch p.Event {
	case "agent_start":
		rec.hookStatus = wire.StatusRunning
	case "ui_prompt_start":
		if rec.hookStatus != wire.StatusWaiting {
			rec.beforePrompt = rec.hookStatus
		}
		rec.hookStatus = wire.StatusWaiting
	case "ui_prompt_end":
		if rec.hookStatus == wire.StatusWaiting {
			rec.hookStatus = rec.beforePrompt
		}
	case "agent_settled":
		if p.Aborted != nil && *p.Aborted {
			rec.hookStatus = wire.StatusInterrupted
		} else {
			rec.hookStatus = wire.StatusDone
			rec.checkSettleError = true
		}
	case "session_shutdown":
		if p.ShutdownReason == "quit" {
			rec.ended = true
		}
	}
	if rec.hookStatus == "" {
		rec.hookStatus = wire.StatusIdle
	}
}

func sessionStatus(status session.Status) string {
	switch status {
	case session.Working:
		return wire.StatusRunning
	case session.Done:
		return wire.StatusDone
	case session.Interrupted:
		return wire.StatusInterrupted
	case session.Error:
		return wire.StatusError
	default:
		return ""
	}
}

// SetLive makes live the set of Pi pids the adapter knows: it records the
// cwd and start time of each new pid, and drops dead pids and the files no
// live pid reads. Call it with every pid of a scan before that scan's
// ScanStateForPid calls, so each pid's resolution sees the others. Each pid
// maps to the tmux session it was found in. A pending hook for a new pid is
// applied to its record, and the scan's Resolve emits the status it set.
func (a *Adapter) SetLive(live map[int]string) {
	var fresh []int
	a.mu.Lock()
	for pid := range live {
		if _, ok := a.byPID[pid]; !ok {
			fresh = append(fresh, pid)
		}
	}
	a.mu.Unlock()
	added := map[int]pidRecord{}
	for _, pid := range fresh {
		added[pid] = pidRecord{cwd: a.cwdForPID(pid), started: a.startTimeForPID(pid)}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.UnixMilli(a.now())
	for pid, rec := range added {
		if hook, ok := a.pending[pid]; ok && now.Sub(hook.at) <= pendingHookTTL {
			applyHook(&rec, hook.payload, hook.at)
		}
		a.byPID[pid] = rec
	}
	for pid, hook := range a.pending {
		if _, ok := a.byPID[pid]; ok || now.Sub(hook.at) > pendingHookTTL {
			delete(a.pending, pid)
		}
	}
	used := map[string]bool{}
	for pid, rec := range a.byPID {
		session, ok := live[pid]
		if !ok {
			delete(a.byPID, pid)
			continue
		}
		rec.session = session
		a.byPID[pid] = rec
		used[rec.path] = true
	}
	for path := range a.files {
		if !used[path] {
			delete(a.files, path)
		}
	}
}

// fileState returns the read position for path, starting the tool offset at
// the file's current end on first sight.
func (a *Adapter) fileState(path string) *fileState {
	if fs := a.files[path]; fs != nil {
		return fs
	}
	fs := &fileState{toolOffset: completeLinesEnd(path)}
	a.files[path] = fs
	return fs
}

// threadName is the session's last session_info name, else its first
// prompt.
func (a *Adapter) threadName(path string) string {
	fs := a.fileState(path)
	summary, next, err := session.ReadSummary(path, fs.summaryOffset, fs.summary)
	if err == nil {
		fs.summary, fs.summaryOffset = summary, next
	}
	if fs.summary.Name != "" {
		return fs.summary.Name
	}
	return fs.summary.FirstPrompt
}

// sessionPathForPID picks the session file a Pi process writes. Candidates
// are the files whose header cwd equals the process cwd and that were
// modified at or after the process start (less startMatchWindow). A file
// another live Pi pid owns is dropped: one whose header start is within
// startMatchWindow of that pid's start, or the file that pid resolved to at
// its last scan. The newest remaining file by ModTime wins, so a /new,
// /resume or /fork is followed; none left means no file.
func (a *Adapter) sessionPathForPID(pid int) string {
	a.mu.Lock()
	self, ok := a.byPID[pid]
	var others []pidRecord
	for other, rec := range a.byPID {
		if other != pid {
			others = append(others, rec)
		}
	}
	a.mu.Unlock()
	if !ok || self.cwd == "" || self.started.IsZero() {
		return ""
	}
	dir := a.OverrideDir
	if dir == "" {
		dir = discover.SessionDirForCwd(a.SessionsDir, self.cwd)
	}
	sessions, err := discover.DiscoverSessions(dir)
	if err != nil {
		return ""
	}
	since := self.started.Add(-startMatchWindow)
	// Sessions arrive newest ModTime first.
	for _, s := range sessions {
		if s.Cwd != self.cwd || s.ModTime.Before(since) || ownedByOther(s, others) {
			continue
		}
		return s.Path
	}
	return ""
}

func ownedByOther(s discover.Session, others []pidRecord) bool {
	for _, other := range others {
		if other.path == s.Path {
			return true
		}
		if !s.Started.IsZero() && !other.started.IsZero() && absDuration(s.Started.Sub(other.started)) <= startMatchWindow {
			return true
		}
	}
	return false
}

// completeLinesEnd is the offset just past the last newline in path: the
// file size when the last line is complete, else the start of the
// half-written last line, so ReadToolCalls reads that line once it lands.
func completeLinesEnd(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	const chunk = 64 * 1024
	buf := make([]byte, chunk)
	for end := info.Size(); end > 0; {
		start := max(end-chunk, 0)
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && n != int(end-start) {
			return 0
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1
		}
		end = start
	}
	return 0
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func verdictForPath(path string) tracker.ProbeVerdict {
	state, err := session.TrailingState(path)
	if err != nil {
		return tracker.ProbeNoSignal
	}
	switch state.Status {
	case session.Working:
		return tracker.ProbeWorking
	case session.Done:
		return tracker.ProbeDone
	case session.Interrupted:
		return tracker.ProbeInterrupted
	case session.Error:
		return tracker.ProbeError
	default:
		return tracker.ProbeNoSignal
	}
}

func wireStatus(verdict tracker.ProbeVerdict) string {
	switch verdict {
	case tracker.ProbeWorking:
		return wire.StatusRunning
	case tracker.ProbeDone:
		return wire.StatusDone
	case tracker.ProbeInterrupted:
		return wire.StatusInterrupted
	case tracker.ProbeError:
		return wire.StatusError
	default:
		return ""
	}
}

// piToClaudeTools maps Pi's built-in tool names to the Claude Code names the
// description and verb helpers know.
var piToClaudeTools = map[string]string{
	"bash":  "Bash",
	"read":  "Read",
	"edit":  "Edit",
	"write": "Write",
	"grep":  "Grep",
	"find":  "Glob",
	"ls":    "Glob",
}

// describeTool returns the waterfall description and verb for one Pi tool
// call. ls carries only a path, so it is described as a listing of it.
func describeTool(call session.ToolCall) (description, verb string) {
	name, input := call.Name, toolInput(call.Arguments)
	if mapped, ok := piToClaudeTools[name]; ok {
		name = mapped
	}
	description, verb = ccwatch.ToolDescription(name, input), ccwatch.ToolVerb(name, input)
	if call.Name == "ls" {
		description = "Listing"
		var path string
		if json.Unmarshal(input["path"], &path) == nil && path != "" {
			description = "Listing " + textutil.TruncateToWidth(path, 30)
		}
	}
	return description, verb
}

// toolInput decodes a tool call's arguments object; Pi's "path" argument is
// also offered as "file_path", the key the file-tool descriptions read.
func toolInput(arguments string) map[string]json.RawMessage {
	input := map[string]json.RawMessage{}
	if arguments == "" || json.Unmarshal([]byte(arguments), &input) != nil {
		return map[string]json.RawMessage{}
	}
	if _, ok := input["file_path"]; !ok {
		if path, ok := input["path"]; ok {
			input["file_path"] = path
		}
	}
	return input
}

func lsofCwdForPID(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if path, ok := strings.CutPrefix(line, "n"); ok && filepath.IsAbs(path) {
			return path
		}
	}
	return ""
}

// lstartLayouts are the orders `ps -o lstart=` prints its date in.
var lstartLayouts = []string{"Mon _2 Jan 15:04:05 2006", "Mon Jan _2 15:04:05 2006"}

func psStartTimeForPID(pid int) time.Time {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return time.Time{}
	}
	return parseLstart(string(out))
}

func parseLstart(raw string) time.Time {
	text := strings.Join(strings.Fields(raw), " ")
	for _, layout := range lstartLayouts {
		if t, err := time.ParseInLocation(strings.Join(strings.Fields(layout), " "), text, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}
