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
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/pi/discover"
	"github.com/kylesnowschwartz/agent-ouija/pi/session"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// AgentName is the agent key Pi rows carry.
const AgentName = "pi"

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

// pidRecord is the latest scan reading for one Pi process.
type pidRecord struct {
	path    string
	verdict tracker.ProbeVerdict
}

// fileState is the scan goroutine's read position in one session file.
type fileState struct {
	toolOffset    int64
	summaryOffset int64
	summary       session.Summary
	status        string
	statusSent    bool
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
func (a *Adapter) Name() string { return AgentName }

// ScanStateForPid resolves the pid's current session file and reads its
// identity and trailing status. The pid is re-resolved on every call, so a
// /new, /resume or /fork in the same process is followed.
func (a *Adapter) ScanStateForPid(pid int, _ string) tracker.ScanState {
	path := a.sessionPathForPID(pid)
	if path == "" {
		a.forget(pid)
		return tracker.ScanState{}
	}
	verdict := verdictForPath(path)
	a.mu.Lock()
	a.byPID[pid] = pidRecord{path: path, verdict: verdict}
	a.mu.Unlock()
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
	if !ok || threadID != "" && discover.SessionIDFromPath(rec.path) != threadID {
		return tracker.ProbeNoSignal
	}
	return verdictForPath(rec.path)
}

// Activity returns the events the pid's session file produced since the
// previous call: one when the status differs from the last one reported,
// and one per tool call written since. At first sight of a file the tool
// offset starts at the file's end, so earlier calls are not replayed. Call
// it after ScanStateForPid for the same pid, on the same goroutine.
func (a *Adapter) Activity(pid int, tmuxSession string) []wire.AgentEvent {
	a.mu.Lock()
	rec, ok := a.byPID[pid]
	a.mu.Unlock()
	if !ok || tmuxSession == "" {
		return nil
	}
	threadID := discover.SessionIDFromPath(rec.path)
	name := a.threadName(rec.path)
	fs := a.fileState(rec.path)

	base := wire.AgentEvent{
		Agent:      AgentName,
		Session:    tmuxSession,
		ThreadID:   threadID,
		ThreadName: name,
		PID:        pid,
		TS:         a.now(),
	}
	var out []wire.AgentEvent

	if status := wireStatus(rec.verdict); status != "" && (!fs.statusSent || status != fs.status) {
		fs.status, fs.statusSent = status, true
		ev := base
		ev.Status = status
		out = append(out, ev)
	}

	calls, next, err := session.ReadToolCalls(rec.path, fs.toolOffset)
	if err == nil {
		fs.toolOffset = next
		for _, call := range calls {
			ev := base
			ev.Status = wire.StatusRunning
			ev.ToolInvoked = true
			toolName, input := ccToolName(call.Name), toolInput(call.Arguments)
			ev.ToolDescription = ccwatch.ToolDescription(toolName, input)
			ev.ToolVerb = ccwatch.ToolVerb(toolName, input)
			out = append(out, ev)
		}
		if len(calls) > 0 && fs.status != wire.StatusRunning {
			// The trailing status is what the row shows once the calls land.
			last := out[len(out)-1]
			last.ToolInvoked = false
			last.ToolDescription, last.ToolVerb = "", ""
			last.Status = fs.status
			out = append(out, last)
		}
	}
	return out
}

// Forget drops what the adapter holds for pids absent from live; files no
// live pid reads are dropped too.
func (a *Adapter) Forget(live map[int]bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
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

func (a *Adapter) forget(pid int) {
	a.mu.Lock()
	delete(a.byPID, pid)
	a.mu.Unlock()
}

// fileState returns the read position for path, starting the tool offset at
// the file's current end on first sight.
func (a *Adapter) fileState(path string) *fileState {
	if fs := a.files[path]; fs != nil {
		return fs
	}
	fs := &fileState{}
	if _, end, err := session.ReadToolCalls(path, 0); err == nil {
		fs.toolOffset = end
	}
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

// sessionPathForPID picks the session file a Pi process writes: among the
// files whose header cwd equals the process cwd, the one started within
// startMatchWindow of the process start; else the newest modified at or
// after the process start; else none.
func (a *Adapter) sessionPathForPID(pid int) string {
	if pid <= 1 || a.cwdForPID == nil || a.startTimeForPID == nil {
		return ""
	}
	cwd := a.cwdForPID(pid)
	started := a.startTimeForPID(pid)
	if cwd == "" || started.IsZero() {
		return ""
	}
	dir := a.OverrideDir
	if dir == "" {
		dir = discover.SessionDirForCwd(a.SessionsDir, cwd)
	}
	sessions, err := discover.DiscoverSessions(dir)
	if err != nil {
		return ""
	}
	newest := ""
	for _, s := range sessions {
		if s.Cwd != cwd {
			continue
		}
		if !s.Started.IsZero() && absDuration(s.Started.Sub(started)) <= startMatchWindow {
			return s.Path
		}
		// Sessions arrive newest ModTime first.
		if newest == "" && !s.ModTime.Before(started) {
			newest = s.Path
		}
	}
	return newest
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

func ccToolName(name string) string {
	if mapped, ok := piToClaudeTools[name]; ok {
		return mapped
	}
	return name
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
