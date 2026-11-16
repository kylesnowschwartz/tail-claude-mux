// Package metadata ports packages/runtime/src/server/metadata-store.ts —
// the per-session presentation metadata behind the TUI's ln zone (activity
// log) and the programmatic API (set-status / set-progress / log).
//
// Not safe for concurrent use — the server serializes access under its
// state lock, like every other stateful piece of the pipeline.
package metadata

import (
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const (
	// maxLogs must cover each session's share of the `tcm waterfall` visible
	// window, which scales with pane height: one 8 s row per line below the
	// header, so a 30-line pane shows 29 rows ≈ 232 s. A smaller cap lets
	// in-window rows decay to a false-calm baseline as eviction removes
	// their entries. Tool entries are per-INVOCATION (identical
	// repeated calls each count — watch.go's ToolInvoked keying), and one
	// session can host several threads plus parallel subagents, so budget
	// ~3 tool entries/s sustained. Thinking entries (one per thinking block)
	// run at about two for every three tool calls, adding ~2/s: 5 entries/s ×
	// 232 s ≈ 1160, so 1200 covers the window; memory cost is trivial
	// (≤ ~750 KB/session).
	maxLogs          = 1200
	maxMessageLength = 500
	maxLabelLength   = 100
	maxSourceLength  = 50
)

// truncate caps s at max display units with a trailing ellipsis. The TS
// original slices UTF-16 code units; runes are the closest Go analogue and
// differ only on surrogate-pair content at exactly the boundary.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// LiaisonTTL is how long a set from POST /set-liaison-items stays visible
// after it was received. A liaison that stops sending shows nothing stale.
const LiaisonTTL = 10 * time.Minute

// liaisonSet is one session's liaison items and finished conversation IDs,
// with the time the server received them.
type liaisonSet struct {
	items      []wire.LiaisonItem
	finished   []string
	receivedAt int64 // epoch ms
}

// Store holds SessionMetadata per session name.
type Store struct {
	store   map[string]*wire.SessionMetadata
	liaison map[string]liaisonSet
	now     func() int64 // epoch ms, injectable for tests
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		store:   map[string]*wire.SessionMetadata{},
		liaison: map[string]liaisonSet{},
		now:     func() int64 { return time.Now().UnixMilli() },
	}
}

// SetClock replaces the store's clock (epoch ms); tests use it to move time.
func (s *Store) SetClock(now func() int64) { s.now = now }

// Now returns the store's clock reading in epoch ms.
func (s *Store) Now() int64 { return s.now() }

// SetLiaison replaces the session's liaison items and finished IDs. Empty
// lists clear the set.
func (s *Store) SetLiaison(session string, items []wire.LiaisonItem, finished []string) {
	if len(items) == 0 && len(finished) == 0 {
		delete(s.liaison, session)
		return
	}
	s.liaison[session] = liaisonSet{
		items:      append([]wire.LiaisonItem(nil), items...),
		finished:   append([]string(nil), finished...),
		receivedAt: s.now(),
	}
}

// currentLiaison returns the session's set while it is younger than
// LiaisonTTL, dropping it once it has expired.
func (s *Store) currentLiaison(session string) (liaisonSet, bool) {
	set, ok := s.liaison[session]
	if !ok {
		return liaisonSet{}, false
	}
	if s.now()-set.receivedAt >= LiaisonTTL.Milliseconds() {
		delete(s.liaison, session)
		return liaisonSet{}, false
	}
	return set, true
}

func (s *Store) getOrCreate(session string) *wire.SessionMetadata {
	meta := s.store[session]
	if meta == nil {
		meta = &wire.SessionMetadata{Logs: []wire.MetadataLogEntry{}}
		s.store[session] = meta
	}
	return meta
}

// Get returns the session's metadata, or nil when everything is empty
// (the wire carries metadata: null for quiet sessions).
//
// The liaison set is read through its expiry on every call, so the
// server's periodic change check pushes the disappearance of an expired set.
func (s *Store) Get(session string) *wire.SessionMetadata {
	meta := s.store[session]
	set, hasLiaison := s.currentLiaison(session)
	if !hasLiaison {
		if meta == nil || (meta.Status == nil && meta.Progress == nil && len(meta.Logs) == 0) {
			return nil
		}
		return meta
	}
	out := wire.SessionMetadata{Logs: []wire.MetadataLogEntry{}}
	if meta != nil {
		out = *meta
	}
	out.LiaisonItems = set.items
	out.LiaisonFinished = set.finished
	return &out
}

// SetStatus sets or clears (nil) the session's status line.
func (s *Store) SetStatus(session string, status *wire.MetadataStatus) {
	if status == nil {
		if meta := s.store[session]; meta != nil {
			meta.Status = nil
		}
		return
	}
	meta := s.getOrCreate(session)
	meta.Status = &wire.MetadataStatus{
		Text: truncate(status.Text, maxLabelLength),
		Tone: status.Tone,
		TS:   s.now(),
	}
}

// SetProgress sets or clears (nil) the session's progress bar.
func (s *Store) SetProgress(session string, progress *wire.MetadataProgress) {
	if progress == nil {
		if meta := s.store[session]; meta != nil {
			meta.Progress = nil
		}
		return
	}
	meta := s.getOrCreate(session)
	p := *progress
	p.Label = truncate(p.Label, maxLabelLength)
	p.TS = s.now()
	meta.Progress = &p
}

// AppendLog appends one activity-log entry, keeping the newest maxLogs.
func (s *Store) AppendLog(session string, entry wire.MetadataLogEntry) {
	meta := s.getOrCreate(session)
	entry.Message = truncate(entry.Message, maxMessageLength)
	entry.Source = truncate(entry.Source, maxSourceLength)
	entry.TS = s.now()
	meta.Logs = append(meta.Logs, entry)
	if len(meta.Logs) > maxLogs {
		meta.Logs = meta.Logs[len(meta.Logs)-maxLogs:]
	}
}

// ClearLogs empties the session's log buffer.
func (s *Store) ClearLogs(session string) {
	if meta := s.store[session]; meta != nil {
		meta.Logs = []wire.MetadataLogEntry{}
	}
}

// PruneSessions drops metadata for sessions no longer present.
func (s *Store) PruneSessions(valid map[string]bool) {
	for name := range s.store {
		if !valid[name] {
			delete(s.store, name)
		}
	}
	for name := range s.liaison {
		if !valid[name] {
			delete(s.liaison, name)
		}
	}
}
