package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const finishedID = "0f8fad5b-d9cb-469f-a165-70867728950e"

// newLiaisonItemsServer is the attention test server with its metadata
// clock fixed at clock's value.
func newLiaisonItemsServer(t *testing.T) (*Server, *time.Time) {
	t.Helper()
	s := newAttentionTestServer(t)
	clock := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	s.Metadata.SetClock(func() int64 { return clock.UnixMilli() })
	return s, &clock
}

func postLiaisonItems(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/set-liaison-items", strings.NewReader(body)))
	return rec
}

func workMetadata(t *testing.T, s *Server) *wire.SessionMetadata {
	t.Helper()
	for _, sess := range getState(t, s).Sessions {
		if sess.Name == "work" {
			return sess.Metadata
		}
	}
	t.Fatal("session work not in state")
	return nil
}

const validLiaisonBody = `{"session":"work","items":[` +
	`{"kind":"blocked","title":"deploy needs a key","since":"2026-11-16T08:50:00Z"},` +
	`{"kind":"reminder","title":"line one\nline two\u0007","since":"2026-11-16T08:55:00+00:00"}` +
	`],"finished":["` + finishedID + `"]}`

func TestSetLiaisonItemsAcceptsAndServesInState(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	if rec := postLiaisonItems(t, s, validLiaisonBody); rec.Code != 204 {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	meta := workMetadata(t, s)
	if meta == nil {
		t.Fatal("no metadata")
	}
	want := []wire.LiaisonItem{
		{Kind: "blocked", Title: "deploy needs a key", Since: time.Date(2026, 11, 16, 8, 50, 0, 0, time.UTC).UnixMilli()},
		{Kind: "reminder", Title: "line one line two", Since: time.Date(2026, 11, 16, 8, 55, 0, 0, time.UTC).UnixMilli()},
	}
	if len(meta.LiaisonItems) != 2 || meta.LiaisonItems[0] != want[0] || meta.LiaisonItems[1] != want[1] {
		t.Fatalf("items = %+v", meta.LiaisonItems)
	}
	if len(meta.LiaisonFinished) != 1 || meta.LiaisonFinished[0] != finishedID {
		t.Fatalf("finished = %v", meta.LiaisonFinished)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/state", nil))
	body := rec.Body.String()
	for _, part := range []string{`"metadata":{`, `"liaisonItems":[{"kind":"blocked"`, `"liaisonFinished":["` + finishedID + `"]`} {
		if !strings.Contains(body, part) {
			t.Fatalf("state JSON lacks %s: %s", part, body)
		}
	}
	if start := strings.Index(body, `"metadata":`); start >= 0 {
		t.Logf("GET /state metadata: %s", body[start:])
	}
}

func TestSetLiaisonItemsCutsLongTitles(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	long := strings.Repeat("a", 200)
	body := `{"session":"work","items":[{"kind":"answer","title":"` + long + `","since":"2026-11-16T08:00:00Z"}]}`
	if rec := postLiaisonItems(t, s, body); rec.Code != 204 {
		t.Fatalf("status = %d", rec.Code)
	}
	title := workMetadata(t, s).LiaisonItems[0].Title
	if n := len([]rune(title)); n != 80 || !strings.HasSuffix(title, "…") {
		t.Fatalf("title has %d runes: %q", n, title)
	}
}

func TestSetLiaisonItemsRemovesFormatCharacters(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	body := `{"session":"work","items":[{"kind":"answer","title":"\u202epay\u200b now\ufeff","since":"2026-11-16T08:00:00Z"}]}`
	if rec := postLiaisonItems(t, s, body); rec.Code != 204 {
		t.Fatalf("status = %d", rec.Code)
	}
	if title := workMetadata(t, s).LiaisonItems[0].Title; title != "pay now" {
		t.Fatalf("title = %q", title)
	}
}

func TestSetLiaisonItemsRefusals(t *testing.T) {
	item := func(kind, title, since string) string {
		return `{"kind":"` + kind + `","title":"` + title + `","since":"` + since + `"}`
	}
	ok := item("blocked", "x", "2026-11-16T08:00:00Z")
	many := func(n int, one string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = one
		}
		return strings.Join(parts, ",")
	}
	cases := map[string]string{
		"invalid json":         `{"session":`,
		"missing session":      `{"items":[` + ok + `]}`,
		"too many items":       `{"session":"work","items":[` + many(21, ok) + `]}`,
		"too many finished":    `{"session":"work","finished":[` + many(21, `"`+finishedID+`"`) + `]}`,
		"unknown kind":         `{"session":"work","items":[` + item("urgent", "x", "2026-11-16T08:00:00Z") + `]}`,
		"asks from a caller":   `{"session":"work","items":[` + item("asks", "x", "2026-11-16T08:00:00Z") + `]}`,
		"empty title":          `{"session":"work","items":[` + item("blocked", "", "2026-11-16T08:00:00Z") + `]}`,
		"control-only title":   `{"session":"work","items":[` + item("blocked", `\n\t\u0007 `, "2026-11-16T08:00:00Z") + `]}`,
		"since not RFC 3339":   `{"session":"work","items":[` + item("blocked", "x", "16/11/2026") + `]}`,
		"since in the future":  `{"session":"work","items":[` + item("blocked", "x", "2026-11-16T09:01:01Z") + `]}`,
		"since over 30 days":   `{"session":"work","items":[` + item("blocked", "x", "2026-10-17T08:59:59Z") + `]}`,
		"finished upper case":  `{"session":"work","finished":["0F8FAD5B-D9CB-469F-A165-70867728950E"]}`,
		"finished not a UUID":  `{"session":"work","finished":["thread-1"]}`,
		"one bad item of many": `{"session":"work","items":[` + ok + `,` + item("nope", "x", "2026-11-16T08:00:00Z") + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := newLiaisonItemsServer(t)
			if rec := postLiaisonItems(t, s, body); rec.Code != 400 {
				t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
			}
			if meta := workMetadata(t, s); meta != nil && (len(meta.LiaisonItems) > 0 || len(meta.LiaisonFinished) > 0) {
				t.Fatalf("refused request stored %+v", meta)
			}
		})
	}
}

func TestSetLiaisonItemsAcceptsSinceWithinAMinuteAhead(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	body := `{"session":"work","items":[{"kind":"blocked","title":"x","since":"2026-11-16T09:00:59Z"}]}`
	if rec := postLiaisonItems(t, s, body); rec.Code != 204 {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestSetLiaisonItemsAcceptsSinceThirtyDaysAgo(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	body := `{"session":"work","items":[{"kind":"blocked","title":"x","since":"2026-10-17T09:00:00Z"}]}`
	if rec := postLiaisonItems(t, s, body); rec.Code != 204 {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestSetLiaisonItemsEmptyListsClear(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	postLiaisonItems(t, s, validLiaisonBody)
	if rec := postLiaisonItems(t, s, `{"session":"work","items":[],"finished":[]}`); rec.Code != 204 {
		t.Fatalf("status = %d", rec.Code)
	}
	if meta := workMetadata(t, s); meta != nil {
		t.Fatalf("metadata after clear = %+v", meta)
	}
}

func TestSetLiaisonItemsExpireAfterTenMinutes(t *testing.T) {
	s, clock := newLiaisonItemsServer(t)
	postLiaisonItems(t, s, validLiaisonBody)
	*clock = clock.Add(10*time.Minute - time.Second)
	if meta := workMetadata(t, s); meta == nil || len(meta.LiaisonItems) != 2 {
		t.Fatalf("set gone before ten minutes: %+v", meta)
	}
	*clock = clock.Add(time.Second)
	if meta := workMetadata(t, s); meta != nil {
		t.Fatalf("set still present after ten minutes: %+v", meta)
	}
}

func TestSetLiaisonItemsKeepsStatusBeside(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	postLiaisonItems(t, s, validLiaisonBody)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/set-status", strings.NewReader(`{"session":"work","text":"reading"}`)))
	if rec.Code != 204 {
		t.Fatalf("set-status = %d %q", rec.Code, rec.Body.String())
	}
	meta := workMetadata(t, s)
	if meta == nil || meta.Status == nil || meta.Status.Text != "reading" || len(meta.LiaisonItems) != 2 {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestSetLiaisonItemsWithoutItemsFieldClears(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	postLiaisonItems(t, s, validLiaisonBody)
	if rec := postLiaisonItems(t, s, `{"session":"work"}`); rec.Code != 204 {
		t.Fatalf("status = %d", rec.Code)
	}
	if meta := workMetadata(t, s); meta != nil {
		t.Fatalf("metadata after clear = %+v", meta)
	}
}

func TestSetLiaisonItemsForUnknownSessionIsPruned(t *testing.T) {
	s, _ := newLiaisonItemsServer(t)
	body := `{"session":"ghost","items":[{"kind":"blocked","title":"x","since":"2026-11-16T08:00:00Z"}]}`
	if rec := postLiaisonItems(t, s, body); rec.Code != 204 {
		t.Fatalf("status = %d", rec.Code)
	}
	getState(t, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta := s.Metadata.Get("ghost"); meta != nil {
		t.Fatalf("set for unknown session survived a state build: %+v", meta)
	}
	if s.Metadata.Get("work") != nil {
		t.Fatal("control: session work has metadata it was never sent")
	}
}
