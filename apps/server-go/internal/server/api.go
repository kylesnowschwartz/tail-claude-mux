// Programmatic metadata API: the /set-status, /set-progress, /log,
// /clear-log, /notify, and /set-liaison-items routes from packages/runtime/src/server/index.ts.
// External scripts drive the sidebar's status line, progress bar, and
// activity log through these; every mutation broadcasts immediately.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

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
		Thread  string          `json:"thread,omitempty"`
		Text    json.RawMessage `json:"text"` // string | null | absent
		Tone    string          `json:"tone"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	if _, err := s.resolveTrackedEvent(body.Session, body.Thread, body.Pane); err != nil {
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
		Thread  string   `json:"thread,omitempty"`
		Current *float64 `json:"current"`
		Total   *float64 `json:"total"`
		Percent *float64 `json:"percent"`
		Label   string   `json:"label"`
		Clear   bool     `json:"clear"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	if _, err := s.resolveTrackedEvent(body.Session, body.Thread, body.Pane); err != nil {
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

// Limits for POST /set-liaison-items.
const (
	maxLiaisonItems      = 20
	maxLiaisonFinished   = 20
	maxLiaisonTitleRunes = 80
	// liaisonFutureSkew is how far ahead of the server clock an item's
	// since time may be.
	liaisonFutureSkew = time.Minute
)

// liaisonCallerKinds are the item kinds a caller may send. The kind "asks"
// is derived by the sidebar from the liaison's own conversation.
var liaisonCallerKinds = map[string]bool{"blocked": true, "no_report": true, "answer": true, "reminder": true}

var lowerUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cleanLiaisonTitle folds a title to one line: line breaks and tabs become
// spaces, other control characters are removed, runs of spaces collapse,
// and the result is cut to maxLiaisonTitleRunes.
func cleanLiaisonTitle(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	clean := strings.Join(strings.Fields(b.String()), " ")
	if runes := []rune(clean); len(runes) > maxLiaisonTitleRunes {
		clean = strings.TrimRight(string(runes[:maxLiaisonTitleRunes-1]), " ") + "…"
	}
	return clean
}

// handleSetLiaisonItems replaces the session's liaison items and finished
// conversation IDs. The values are display only: any local program can set
// them, so nothing may route work or grant anything by them.
func (s *Server) handleSetLiaisonItems(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
		Items   []struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
			Since string `json:"since"`
		} `json:"items"`
		Finished []string `json:"finished"`
	}
	if !decodeAPIBody(w, r, &body, &body.Session) {
		return
	}
	if len(body.Items) > maxLiaisonItems {
		http.Error(w, fmt.Sprintf("at most %d items", maxLiaisonItems), http.StatusBadRequest)
		return
	}
	if len(body.Finished) > maxLiaisonFinished {
		http.Error(w, fmt.Sprintf("at most %d finished IDs", maxLiaisonFinished), http.StatusBadRequest)
		return
	}
	now := time.UnixMilli(s.Metadata.Now())
	items := make([]wire.LiaisonItem, 0, len(body.Items))
	for i, in := range body.Items {
		if !liaisonCallerKinds[in.Kind] {
			http.Error(w, fmt.Sprintf("items[%d]: kind must be blocked, no_report, answer, or reminder", i), http.StatusBadRequest)
			return
		}
		title := cleanLiaisonTitle(in.Title)
		if title == "" {
			http.Error(w, fmt.Sprintf("items[%d]: empty title", i), http.StatusBadRequest)
			return
		}
		since, err := time.Parse(time.RFC3339, in.Since)
		if err != nil {
			http.Error(w, fmt.Sprintf("items[%d]: since must be an RFC 3339 time", i), http.StatusBadRequest)
			return
		}
		if since.After(now.Add(liaisonFutureSkew)) {
			http.Error(w, fmt.Sprintf("items[%d]: since is in the future", i), http.StatusBadRequest)
			return
		}
		items = append(items, wire.LiaisonItem{Kind: in.Kind, Title: title, Since: since.UnixMilli()})
	}
	for i, id := range body.Finished {
		if !lowerUUID.MatchString(id) {
			http.Error(w, fmt.Sprintf("finished[%d]: must be a lower-case UUID", i), http.StatusBadRequest)
			return
		}
	}
	s.mutateMetadata(w, func() { s.Metadata.SetLiaison(body.Session, items, body.Finished) })
}
