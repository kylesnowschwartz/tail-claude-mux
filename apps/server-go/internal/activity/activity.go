// Package activity is tcm's activity record: an append-only log of the hook
// events tcm receives and the status changes it derives, kept as one JSON
// Lines file per local day. Every line carries a sequence number that rises
// by one per line across day files and restarts, so a reader keeps the
// number of the last line it read and asks for the lines after it.
//
// tcm is the only writer. Old records are removed by deleting whole day
// files; no file is ever rewritten, so a program following a file never
// loses its place.
package activity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RetentionDays is how many days of day files are kept before today's.
const RetentionDays = 30

const (
	dayLayout = "2006-01-02"
	fileExt   = ".jsonl"
	fileMode  = 0o600
	dirMode   = 0o700

	// KindHook marks a line for a hook event tcm received.
	KindHook = "hook"
	// KindStatus marks a line for a status change tcm derived.
	KindStatus = "status"
)

// Hook is the recorded part of one hook event. It holds no conversation
// content: no prompt text and no tool input.
type Hook struct {
	Event            string
	SessionID        string
	Cwd              string
	ToolName         string
	NotificationType string
	StopReason       string
	PID              int
	TranscriptPath   string
}

// Status is the recorded part of one derived status change.
type Status struct {
	TmuxSession string
	PaneID      string
	SessionID   string
	Status      string
	ToolVerb    string
}

// hookLine is the JSON shape of a hook line.
type hookLine struct {
	Seq              int64  `json:"seq"`
	Kind             string `json:"kind"`
	ReceivedMS       int64  `json:"received_ms"`
	Event            string `json:"event"`
	SessionID        string `json:"session_id,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
	StopReason       string `json:"stop_reason,omitempty"`
	PID              int    `json:"pid,omitempty"`
	TranscriptPath   string `json:"transcript_path,omitempty"`
}

// statusLine is the JSON shape of a status line.
type statusLine struct {
	Seq         int64  `json:"seq"`
	Kind        string `json:"kind"`
	ReceivedMS  int64  `json:"received_ms"`
	TmuxSession string `json:"tmux_session,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Status      string `json:"status"`
	ToolVerb    string `json:"tool_verb,omitempty"`
}

// Writer appends lines to the day files in one directory. Safe for
// concurrent use.
type Writer struct {
	dir string
	now func() time.Time

	mu        sync.Mutex
	seq       int64
	day       string // day of the open file
	file      *os.File
	prunedDay string // day retention last ran
}

// Open prepares the directory, deletes day files past retention, and
// recovers the last sequence number from the newest file. now is the clock;
// nil means time.Now.
func Open(dir string, now func() time.Time) (*Writer, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, now: now}
	w.pruneLocked(now())
	seq, err := lastSeq(dir)
	if err != nil {
		return nil, err
	}
	w.seq = seq
	return w, nil
}

// Dir is the directory holding the day files.
func (w *Writer) Dir() string { return w.dir }

// AppendHook writes one hook line.
func (w *Writer) AppendHook(h Hook) error {
	return w.append(func(seq, ms int64) any {
		return hookLine{
			Seq: seq, Kind: KindHook, ReceivedMS: ms,
			Event: h.Event, SessionID: h.SessionID, Cwd: h.Cwd, ToolName: h.ToolName,
			NotificationType: h.NotificationType, StopReason: h.StopReason,
			PID: h.PID, TranscriptPath: h.TranscriptPath,
		}
	})
}

// AppendStatus writes one status line.
func (w *Writer) AppendStatus(s Status) error {
	return w.append(func(seq, ms int64) any {
		return statusLine{
			Seq: seq, Kind: KindStatus, ReceivedMS: ms,
			TmuxSession: s.TmuxSession, PaneID: s.PaneID, SessionID: s.SessionID,
			Status: s.Status, ToolVerb: s.ToolVerb,
		}
	})
}

// Close closes the open day file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *Writer) append(build func(seq, ms int64) any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if err := w.rollLocked(now); err != nil {
		return err
	}
	data, err := json.Marshal(build(w.seq+1, now.UnixMilli()))
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := w.file.Write(data); err != nil {
		return err
	}
	w.seq++
	return nil
}

// rollLocked opens the file for now's day, and runs retention once per day.
func (w *Writer) rollLocked(now time.Time) error {
	day := now.Format(dayLayout)
	if w.prunedDay != day {
		w.pruneLocked(now)
	}
	if w.file != nil && w.day == day {
		return nil
	}
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	f, err := os.OpenFile(filepath.Join(w.dir, day+fileExt), os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		return err
	}
	// A file that ends mid-line (a write cut short) gets a newline so the
	// next line starts on its own; readers skip the unparseable fragment.
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		if !endsWithNewline(f.Name(), info.Size()) {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				_ = f.Close()
				return err
			}
		}
	}
	w.file, w.day = f, day
	return nil
}

// pruneLocked deletes day files older than RetentionDays before now's day.
func (w *Writer) pruneLocked(now time.Time) {
	w.prunedDay = now.Format(dayLayout)
	cutoff := now.AddDate(0, 0, -RetentionDays).Format(dayLayout)
	days, err := dayFiles(w.dir)
	if err != nil {
		log.Printf("activity: list %s: %v", w.dir, err)
		return
	}
	for _, d := range days {
		if d < cutoff {
			if err := os.Remove(filepath.Join(w.dir, d+fileExt)); err != nil {
				log.Printf("activity: delete %s: %v", d, err)
			}
		}
	}
}

func endsWithNewline(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, size-1); err != nil {
		return true
	}
	return b[0] == '\n'
}

// dayFiles lists the day names ("2006-01-02") of the files in dir, oldest
// first. Other files are ignored.
func dayFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var days []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, fileExt) {
			continue
		}
		day := strings.TrimSuffix(name, fileExt)
		if _, err := time.Parse(dayLayout, day); err != nil {
			continue
		}
		days = append(days, day)
	}
	sort.Strings(days)
	return days, nil
}

// completeLines returns the sequence number and raw bytes of each complete,
// parseable line in one day file. A last line with no newline is still
// being written and is left out.
func completeLines(path string) ([]int64, [][]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		data = data[:i]
	} else {
		return nil, nil, nil
	}
	var seqs []int64
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var head struct {
			Seq int64 `json:"seq"`
		}
		if len(line) == 0 || json.Unmarshal(line, &head) != nil || head.Seq <= 0 {
			continue
		}
		seqs = append(seqs, head.Seq)
		lines = append(lines, line)
	}
	return seqs, lines, nil
}

// lastSeq is the newest committed sequence number in dir, searching back
// past day files that hold no complete line. Zero when there is none.
func lastSeq(dir string) (int64, error) {
	days, err := dayFiles(dir)
	if err != nil {
		return 0, err
	}
	for i := len(days) - 1; i >= 0; i-- {
		seqs, _, err := completeLines(filepath.Join(dir, days[i]+fileExt))
		if err != nil {
			return 0, err
		}
		if len(seqs) > 0 {
			return seqs[len(seqs)-1], nil
		}
	}
	return 0, nil
}

// Page is one answer from Read.
type Page struct {
	// OldestSeq is the sequence number of the oldest kept line; zero when
	// the record is empty. A reader whose last-read number is below
	// OldestSeq-1 has missed lines.
	OldestSeq int64 `json:"oldest_seq"`
	// Events are the lines after the requested number, oldest first.
	Events []json.RawMessage `json:"events"`
}

// Read returns up to limit complete lines with a sequence number above
// since, oldest first, from the day files in dir.
func Read(dir string, since int64, limit int) (Page, error) {
	page := Page{Events: []json.RawMessage{}}
	if limit <= 0 {
		return page, fmt.Errorf("limit must be positive")
	}
	days, err := dayFiles(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return page, nil
		}
		return page, err
	}
	for _, day := range days {
		seqs, lines, err := completeLines(filepath.Join(dir, day+fileExt))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted by retention between listing and reading
			}
			return page, err
		}
		if page.OldestSeq == 0 && len(seqs) > 0 {
			page.OldestSeq = seqs[0]
		}
		for i, seq := range seqs {
			if seq <= since {
				continue
			}
			page.Events = append(page.Events, json.RawMessage(lines[i]))
			if len(page.Events) >= limit {
				return page, nil
			}
		}
	}
	return page, nil
}
