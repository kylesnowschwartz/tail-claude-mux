// Package piwatch derives tcm agent state for Pi coding agent processes from
// Pi's session files alone. Pi sends no hooks, so every reading starts from
// the process the pane scan found: its cwd and start time pick the session
// file, and agent-ouija's pi packages read that file.
//
// ScanStateForPid and Activity run on the pane-scan goroutine outside the
// server lock; ProbeLiveStatus runs under it on the liveness goroutine. The
// per-pid file record they share is guarded by the Adapter's mutex; the
// per-file read offsets belong to the scan goroutine alone.
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

	mu    sync.Mutex
	byPID map[int]pidRecord

	files map[string]*fileState
}

// pidRecord is one live Pi process: its cwd and start time, read once, and
// the session file and verdict of its latest scan.
type pidRecord struct {
	cwd     string
	started time.Time
	path    string
	verdict tracker.ProbeVerdict
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
		files:           map[string]*fileState{},
	}
}

// Name is the agent key this adapter serves.
func (a *Adapter) Name() string { return agentmatch.AgentPi }

// ScanStateForPid resolves the pid's current session file and reads its
// identity and trailing status. The pid is re-resolved on every call, so a
// /new, /resume or /fork in the same process is followed.
func (a *Adapter) ScanStateForPid(pid int, _ string) tracker.ScanState {
	path := a.sessionPathForPID(pid)
	verdict := tracker.ProbeNoSignal
	if path != "" {
		verdict = verdictForPath(path)
	}
	a.mu.Lock()
	rec := a.byPID[pid]
	rec.path, rec.verdict = path, verdict
	a.byPID[pid] = rec
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
	return verdictForPath(rec.path)
}

// Activity returns the events the pid's session file produced since the
// previous call: one per tool call written since, then, last, one carrying
// the trailing status when it differs from the last status sent for that
// file. At first sight of a file the tool offset starts at the end of its
// last complete line, so earlier calls are not replayed. Call it after
// ScanStateForPid for the same pid, on the same goroutine.
func (a *Adapter) Activity(pid int, tmuxSession string) []wire.AgentEvent {
	a.mu.Lock()
	rec, ok := a.byPID[pid]
	a.mu.Unlock()
	if !ok || rec.path == "" || tmuxSession == "" {
		return nil
	}
	fs := a.fileState(rec.path)
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
			ev.Status = wire.StatusRunning
			ev.ToolInvoked = true
			ev.ToolDescription, ev.ToolVerb = describeTool(call)
			out = append(out, ev)
		}
	}
	if status := wireStatus(rec.verdict); status != "" && status != fs.status {
		fs.status = status
		ev := base
		ev.Status = status
		out = append(out, ev)
	}
	return out
}

// SetLive makes live the set of Pi pids the adapter knows: it records the
// cwd and start time of each new pid, and drops dead pids and the files no
// live pid reads. Call it with every pid of a scan before that scan's
// ScanStateForPid calls, so each pid's resolution sees the others.
func (a *Adapter) SetLive(live map[int]bool) {
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
	for pid, rec := range added {
		a.byPID[pid] = rec
	}
	used := map[string]bool{}
	for pid, rec := range a.byPID {
		if !live[pid] {
			delete(a.byPID, pid)
			continue
		}
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
