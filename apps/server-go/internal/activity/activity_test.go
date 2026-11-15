package activity

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClock() *clock {
	return &clock{t: time.Date(2026, 11, 15, 23, 59, 0, 0, time.Local)}
}

func open(t *testing.T, dir string, c *clock) *Writer {
	t.Helper()
	w, err := Open(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func seqsIn(t *testing.T, dir string) []int64 {
	t.Helper()
	page, err := Read(dir, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for _, raw := range page.Events {
		var head struct{ Seq int64 }
		if err := json.Unmarshal(raw, &head); err != nil {
			t.Fatal(err)
		}
		out = append(out, head.Seq)
	}
	return out
}

func TestSequenceContinuesAfterRestart(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	w := open(t, dir, c)
	_ = w.AppendHook(Hook{Event: "Stop"})
	_ = w.AppendStatus(Status{Status: "idle"})
	_ = w.Close()

	w2 := open(t, dir, c)
	if err := w2.AppendHook(Hook{Event: "UserPromptSubmit"}); err != nil {
		t.Fatal(err)
	}
	if got := seqsIn(t, dir); len(got) != 3 || got[2] != 3 {
		t.Fatalf("seqs = %v, want [1 2 3]", got)
	}
}

func TestNewDayStartsNewFile(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	w := open(t, dir, c)
	_ = w.AppendHook(Hook{Event: "A"})
	c.t = c.t.Add(2 * time.Minute)
	_ = w.AppendHook(Hook{Event: "B"})

	days, _ := dayFiles(dir)
	if len(days) != 2 || days[0] != "2026-11-15" || days[1] != "2026-11-16" {
		t.Fatalf("days = %v", days)
	}
	if got := seqsIn(t, dir); len(got) != 2 || got[1] != 2 {
		t.Fatalf("seqs = %v", got)
	}
}

func TestRetentionDeletesOldFilesAndRewritesNone(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	old := filepath.Join(dir, c.t.AddDate(0, 0, -RetentionDays-1).Format(dayLayout)+fileExt)
	kept := filepath.Join(dir, c.t.AddDate(0, 0, -RetentionDays).Format(dayLayout)+fileExt)
	keptBody := []byte(`{"seq":7,"kind":"hook","received_ms":1,"event":"Stop"}` + "\n")
	_ = os.WriteFile(old, []byte(`{"seq":1,"kind":"hook","received_ms":1,"event":"Stop"}`+"\n"), 0o600)
	_ = os.WriteFile(kept, keptBody, 0o600)

	w := open(t, dir, c)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file still present: %v", err)
	}
	// Once a day: crossing midnight deletes the file that has aged out.
	c.t = c.t.Add(2 * time.Minute)
	_ = w.AppendHook(Hook{Event: "X"})
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Fatalf("aged-out file still present after day change: %v", err)
	}
	if got := seqsIn(t, dir); len(got) != 1 || got[0] != 8 {
		t.Fatalf("seqs = %v, want [8]", got)
	}

	// No file is rewritten: an existing day file only grows by appends.
	day := filepath.Join(dir, c.t.Format(dayLayout)+fileExt)
	before, _ := os.ReadFile(day)
	_ = w.AppendHook(Hook{Event: "Y"})
	after, _ := os.ReadFile(day)
	if string(after[:len(before)]) != string(before) {
		t.Fatalf("day file rewritten:\n%s\n%s", before, after)
	}
}

func TestFileAndDirectoryModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	c := newClock()
	w := open(t, dir, c)
	_ = w.AppendHook(Hook{Event: "Stop"})
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", info.Mode().Perm())
	}
	info, _ = os.Stat(filepath.Join(dir, c.t.Format(dayLayout)+fileExt))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v", info.Mode().Perm())
	}
}

func TestRecoversSequencePastTornAndEmptyFiles(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	yesterday := c.t.AddDate(0, 0, -1).Format(dayLayout)
	today := c.t.Format(dayLayout)
	_ = os.WriteFile(filepath.Join(dir, yesterday+fileExt), []byte(`{"seq":4,"kind":"hook","received_ms":1,"event":"A"}`+"\n"), 0o600)
	// Today's file holds only a torn line: no committed number in it.
	_ = os.WriteFile(filepath.Join(dir, today+fileExt), []byte(`{"seq":5,"kind":"ho`), 0o600)

	w := open(t, dir, c)
	if err := w.AppendHook(Hook{Event: "B"}); err != nil {
		t.Fatal(err)
	}
	if got := seqsIn(t, dir); len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("seqs = %v, want [4 5]", got)
	}
}

func TestReadSkipsUnfinishedLastLineAndHonorsLimit(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	w := open(t, dir, c)
	for range 3 {
		_ = w.AppendHook(Hook{Event: "E"})
	}
	f, _ := os.OpenFile(filepath.Join(dir, c.t.Format(dayLayout)+fileExt), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"seq":4,"kind":"hook"`)
	_ = f.Close()

	page, err := Read(dir, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.OldestSeq != 1 || len(page.Events) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if got := seqsIn(t, dir); len(got) != 3 {
		t.Fatalf("seqs = %v, want 3 complete lines", got)
	}
}

type countingReader struct {
	io.ReadCloser
	n *int
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	*c.n += n
	return n, err
}

func TestReadOpensOnlyFilesAfterSinceAndStopsAtLimit(t *testing.T) {
	dir, c := t.TempDir(), newClock()
	w := open(t, dir, c)
	for range 3 {
		for range 200 {
			_ = w.AppendHook(Hook{Event: "E", Cwd: "/some/long/working/directory/path"})
		}
		c.t = c.t.AddDate(0, 0, 1)
	}
	days, _ := dayFiles(dir)
	read := map[string]*int{}
	orig := openDayFile
	t.Cleanup(func() { openDayFile = orig })
	openDayFile = func(path string) (io.ReadCloser, error) {
		f, err := orig(path)
		if err != nil {
			return nil, err
		}
		n := new(int)
		read[filepath.Base(path)] = n
		return countingReader{f, n}, nil
	}

	page, err := Read(dir, 450, 5)
	if err != nil {
		t.Fatal(err)
	}
	if page.OldestSeq != 1 || len(page.Events) != 5 {
		t.Fatalf("page oldest=%d n=%d", page.OldestSeq, len(page.Events))
	}
	size := func(day string) int {
		info, _ := os.Stat(filepath.Join(dir, day+fileExt))
		return int(info.Size())
	}
	for _, day := range days[:2] {
		if n := read[day+fileExt]; n != nil && *n >= size(day)/2 {
			t.Fatalf("%s: read %d of %d bytes before the starting file", day, *n, size(day))
		}
	}
	if n := read[days[2]+fileExt]; n == nil || *n >= size(days[2])/2 {
		t.Fatalf("starting file read past the limit: %v of %d", n, size(days[2]))
	}
}
