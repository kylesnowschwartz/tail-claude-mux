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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	newestDay string // newest day file written or found on disk
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
	if days, err := dayFiles(dir); err == nil && len(days) > 0 {
		w.newestDay = days[len(days)-1]
	}
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
		// The write may have left a fragment. Closing the file makes the
		// next append reopen it, and reopening a file that ends mid-line
		// starts a new line.
		_ = w.file.Close()
		w.file = nil
		return err
	}
	w.seq++
	return nil
}

// rollLocked opens the file for now's day, and runs retention once per day.
// It never moves to a day earlier than the newest day file: a clock that
// steps back keeps appending to the newest file until its date is later.
func (w *Writer) rollLocked(now time.Time) error {
	day := now.Format(dayLayout)
	if w.prunedDay != day {
		w.pruneLocked(now)
	}
	if day < w.newestDay {
		day = w.newestDay
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
	w.file, w.day, w.newestDay = f, day, day
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

// openDayFile opens a day file for reading.
var openDayFile = func(path string) (io.ReadCloser, error) { return os.Open(path) }

// lastSeq is the newest committed sequence number in dir, searching back
// past day files that hold no complete line. Zero when there is none.
func lastSeq(dir string) (int64, error) {
	days, err := dayFiles(dir)
	if err != nil {
		return 0, err
	}
	for i := len(days) - 1; i >= 0; i-- {
		var last int64
		err := scanLines(filepath.Join(dir, days[i]+fileExt), func(seq int64, _ []byte) bool {
			last = seq
			return true
		})
		if err != nil {
			return 0, err
		}
		if last > 0 {
			return last, nil
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

// lineSeq is the sequence number of one line; false when the line is not
// a record line.
func lineSeq(line []byte) (int64, bool) {
	var head struct {
		Seq int64 `json:"seq"`
	}
	if len(line) == 0 || json.Unmarshal(line, &head) != nil || head.Seq <= 0 {
		return 0, false
	}
	return head.Seq, true
}

// scanLines calls fn with each complete, parseable line of a day file and
// its sequence number, in file order, until fn returns false. A last line
// with no newline is still being written and is left out.
func scanLines(path string, fn func(seq int64, line []byte) bool) error {
	f, err := openDayFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		line = line[:len(line)-1]
		if seq, ok := lineSeq(line); ok && !fn(seq, line) {
			return nil
		}
	}
}

// firstSeq is the sequence number of a day file's first record line;
// false when it holds none.
func firstSeq(path string) (int64, bool, error) {
	var first int64
	err := scanLines(path, func(seq int64, _ []byte) bool {
		first = seq
		return false
	})
	return first, first > 0, err
}

// Read returns up to limit complete lines with a sequence number above
// since, oldest first, from the day files in dir. It reads only the first
// line of each file to find the file holding since+1, then reads forward
// from there until limit lines are collected.
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
	start := 0
	for i, day := range days {
		seq, ok, err := firstSeq(filepath.Join(dir, day+fileExt))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted by retention between listing and reading
			}
			return page, err
		}
		if !ok {
			continue
		}
		if page.OldestSeq == 0 {
			page.OldestSeq = seq
		}
		if seq > since+1 {
			break
		}
		start = i
	}
	for _, day := range days[start:] {
		err := scanLines(filepath.Join(dir, day+fileExt), func(seq int64, line []byte) bool {
			if seq > since {
				page.Events = append(page.Events, json.RawMessage(bytes.Clone(line)))
			}
			return len(page.Events) < limit
		})
		if err != nil && !os.IsNotExist(err) {
			return page, err
		}
		if len(page.Events) >= limit {
			break
		}
	}
	return page, nil
}
