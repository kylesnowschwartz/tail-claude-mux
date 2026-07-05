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
	// maxLogs must cover the TUI seismograph's visible window, which scales
	// with sidebar width: buckets = contentWidth / BUCKET_COLS(3), 8 s each,
	// so a 90-col pane shows ~29 buckets ≈ 4 min (~232 s). A smaller cap
	// lets in-window buckets decay to a false-calm baseline as eviction
	// removes their entries. Tool entries are per-INVOCATION (identical
	// repeated calls each count — watch.go's ToolInvoked keying), and one
	// session can host several threads plus parallel subagents, so budget
	// ~3 entries/s sustained: 800 covers the window with headroom; memory
	// cost is trivial (≤ ~500 KB/session).
	maxLogs          = 800
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

// Store holds SessionMetadata per session name.
type Store struct {
	store map[string]*wire.SessionMetadata
	now   func() int64 // epoch ms, injectable for tests
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		store: map[string]*wire.SessionMetadata{},
		now:   func() int64 { return time.Now().UnixMilli() },
	}
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
func (s *Store) Get(session string) *wire.SessionMetadata {
	meta := s.store[session]
	if meta == nil || (meta.Status == nil && meta.Progress == nil && len(meta.Logs) == 0) {
		return nil
	}
	return meta
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
}
