// Package piwatch derives tcm agent state for Pi coding agent processes.
// Every reading starts from a process the pane scan found. The session file,
// read with agent-ouija's pi packages, supplies the thread name and tool
// calls, and the status of a process that sends no hooks. A process running
// tcm's Pi extension (integrations/pi-extension) reports its session file
// and status through HandleHook; from its first accepted hook on, its status
// comes only from hooks.
//
// ScanStateForPid and Activity run on the pane-scan goroutine outside the
// server lock; ProbeLiveStatus and HandleHook run under it. The per-pid
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

	// hooked is set by the pid's first accepted hook. hookFile is the
	// session file its latest hook named, hookStatus the status its hooks
	// set, sentStatus the last status emitted for it, and ended marks a
	// quit.
	hooked     bool
	hookFile   string
	hookStatus string
	sentStatus string
	ended      bool
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
	if cur, ok := a.byPID[pid]; ok {
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

// ProbeLiveStatus reads the trailing status of the file the last scan
// resolved for pid. It starts no process.
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

// Activity returns the events the pid's session file produced since the
// previous call: one per tool call written since, then, last, one carrying
// the status when it differs from the last status sent. A hooked pid's
// status is its hook status, which its tool-call events carry too; any
// other pid's is the file's trailing status, compared per file. At first sight of a file the tool offset starts at the end of its
// last complete line, so earlier calls are not replayed. Call it after
// ScanStateForPid for the same pid, on the same goroutine.
func (a *Adapter) Activity(pid int, tmuxSession string) []wire.AgentEvent {
	a.mu.Lock()
	rec, ok := a.byPID[pid]
	a.mu.Unlock()
	if !ok || rec.path == "" || rec.ended || tmuxSession == "" {
		return nil
	}
	fs := a.fileState(rec.path)
	toolStatus := wire.StatusRunning
	if rec.hooked && rec.hookStatus != "" {
		toolStatus = rec.hookStatus
	}
	base := wire.AgentEvent{
		Agent:      agentmatch.AgentPi,
		Session:    tmuxSession,
		ThreadID:   discover.SessionIDFromPath(rec.path),
		ThreadName: a.threadName(rec.path),
		PID:        pid,
		TS:         a.now(),
	}
	var out []wire.AgentEvent

	calls, next, err := session.ReadToolCalls(rec.path, fs.toolOffset)
	if err == nil {
		fs.toolOffset = next
		for _, call := range calls {
			ev := base
			ev.Status = toolStatus
			ev.ToolInvoked = true
			ev.ToolDescription, ev.ToolVerb = describeTool(call)
			out = append(out, ev)
		}
	}
	if rec.hooked {
		if rec.hookStatus != "" && rec.hookStatus != rec.sentStatus {
			a.mu.Lock()
			if cur, ok := a.byPID[pid]; ok {
				cur.sentStatus = rec.hookStatus
				a.byPID[pid] = cur
			}
			a.mu.Unlock()
			ev := base
			ev.Status = rec.hookStatus
			out = append(out, ev)
		}
		return out
	}
	if status := wireStatus(rec.verdict); status != "" && status != fs.status {
		fs.status = status
		ev := base
		ev.Status = status
		out = append(out, ev)
	}
	return out
}

// HandleHook applies one hook from tcm's Pi extension and returns the event
// to emit now, if any. A hook from a pid the scan has not found as a live Pi
// is kept for pendingHookTTL and applied when the scan finds it; a newer
// hook from the same pid replaces it. Status: agent_start and ui_prompt_end
// → running, ui_prompt_start → waiting, agent_settled → interrupted when
// aborted, else error when the file ends in an error, else done;
// session_shutdown with reason quit ends the row.
func (a *Adapter) HandleHook(p wire.HookPayload) []wire.AgentEvent {
	if p.Agent != agentmatch.AgentPi || p.PID == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.byPID[p.PID]
	if !ok {
		a.pending[p.PID] = pendingHook{payload: p, at: time.UnixMilli(a.now())}
		return nil
	}
	applyHook(&rec, p)
	a.byPID[p.PID] = rec
	if rec.session == "" {
		return nil
	}
	ev := wire.AgentEvent{
		Agent:    agentmatch.AgentPi,
		Session:  rec.session,
		ThreadID: discover.SessionIDFromPath(rec.hookFile),
		PID:      p.PID,
		TS:       a.now(),
	}
	if ev.ThreadID == "" {
		ev.ThreadID = p.SessionID
	}
	if rec.ended {
		ev.Ended = true
		return []wire.AgentEvent{ev}
	}
	if rec.hookStatus == "" || rec.hookStatus == rec.sentStatus {
		return nil
	}
	rec.sentStatus = rec.hookStatus
	a.byPID[p.PID] = rec
	ev.Status = rec.hookStatus
	return []wire.AgentEvent{ev}
}

// applyHook folds one hook into a pid's record.
func applyHook(rec *pidRecord, p wire.HookPayload) {
	rec.hooked = true
	if p.TranscriptPath != "" {
		rec.hookFile = p.TranscriptPath
		rec.path = p.TranscriptPath
	}
	switch p.Event {
	case "agent_start", "ui_prompt_end":
		rec.hookStatus = wire.StatusRunning
	case "ui_prompt_start":
		rec.hookStatus = wire.StatusWaiting
	case "agent_settled":
		switch {
		case p.Aborted != nil && *p.Aborted:
			rec.hookStatus = wire.StatusInterrupted
		case settledWithError(rec.hookFile):
			rec.hookStatus = wire.StatusError
		default:
			rec.hookStatus = wire.StatusDone
		}
	case "session_shutdown":
		if p.ShutdownReason == "quit" {
			rec.ended = true
		}
	}
}

// settledWithError reports whether the session file's last turn ended in an
// error.
func settledWithError(path string) bool {
	if path == "" {
		return false
	}
	state, err := session.TrailingState(path)
	return err == nil && state.Status == session.Error
}

// SetLive makes live the set of Pi pids the adapter knows: it records the
// cwd and start time of each new pid, and drops dead pids and the files no
// live pid reads. Call it with every pid of a scan before that scan's
// ScanStateForPid calls, so each pid's resolution sees the others.
// Each pid maps to the tmux session it was found in. A pending hook for a
// new pid is applied to its record; Activity emits the status it set.
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
			applyHook(&rec, hook.payload)
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
