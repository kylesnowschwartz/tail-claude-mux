// Programmatic metadata API: the /set-status, /set-progress, /log,
// /clear-log, and /notify routes from packages/runtime/src/server/index.ts.
// External scripts drive the sidebar's status line, progress bar, and
// activity log through these; every mutation broadcasts immediately.
package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// apiBodyLimit bounds programmatic API request bodies. Metadata values are
// truncated to ≤500 display units by the store, so 64 KiB is generous.
const apiBodyLimit = 64 << 10

// decodeAPIBody decodes a JSON body into dst and validates the required
// session field, writing the bun server's exact 400 responses on failure.
func decodeAPIBody(w http.ResponseWriter, r *http.Request, dst any, session *string) bool {
	body, _ := io.ReadAll(io.LimitReader(r.Body, apiBodyLimit))
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return false
	}
	if *session == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return false
	}
	return true
}

// mutateMetadata runs fn on the store under the state lock, broadcasts,
// and answers 204 — the shared tail of every metadata route.
func (s *Server) mutateMetadata(w http.ResponseWriter, fn func()) {
	s.mu.Lock()
	fn()
	s.broadcastLocked()
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string          `json:"session"`
		Pane    string          `json:"pane,omitempty"`
		Text    json.RawMessage `json:"text"` // string | null | absent
		Tone    string          `json:"tone"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	if _, err := s.resolveTrackedEvent(body.Session, "", body.Pane); err != nil {
		writeAgentResolutionError(w, err)
		return
	}
	// null and absent both clear (bun: `body.text === null || undefined`);
	// any other non-string JSON value is the explicit 400.
	if len(body.Text) == 0 || string(body.Text) == "null" {
		s.mutateMetadata(w, func() { s.Metadata.SetStatus(body.Session, nil) })
		return
	}
	var text string
	if err := json.Unmarshal(body.Text, &text); err != nil {
		http.Error(w, "text must be a string or null", http.StatusBadRequest)
		return
	}
	s.mutateMetadata(w, func() {
		s.Metadata.SetStatus(body.Session, &wire.MetadataStatus{Text: text, Tone: body.Tone})
	})
}

func (s *Server) handleSetProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string   `json:"session"`
		Pane    string   `json:"pane,omitempty"`
		Current *float64 `json:"current"`
		Total   *float64 `json:"total"`
		Percent *float64 `json:"percent"`
		Label   string   `json:"label"`
		Clear   bool     `json:"clear"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	if _, err := s.resolveTrackedEvent(body.Session, "", body.Pane); err != nil {
		writeAgentResolutionError(w, err)
		return
	}
	s.mutateMetadata(w, func() {
		if body.Clear {
			s.Metadata.SetProgress(body.Session, nil)
			return
		}
		s.Metadata.SetProgress(body.Session, &wire.MetadataProgress{
			Current: body.Current, Total: body.Total, Percent: body.Percent, Label: body.Label,
		})
	})
}

// handleLog serves both /log and /notify — the bun server's handlers are
// byte-identical today (notify is reserved for a future desktop hook).
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
		Message string `json:"message"`
		Tone    string `json:"tone"`
		Source  string `json:"source"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	if body.Message == "" {
		http.Error(w, "missing message", http.StatusBadRequest)
		return
	}
	s.mutateMetadata(w, func() {
		s.Metadata.AppendLog(body.Session, wire.MetadataLogEntry{
			Message: body.Message, Tone: body.Tone, Source: body.Source,
		})
	})
}

func (s *Server) handleClearLog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	s.mutateMetadata(w, func() { s.Metadata.ClearLogs(body.Session) })
}
