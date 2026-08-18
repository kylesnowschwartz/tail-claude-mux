package delegation

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	createdAt := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	params := CreateParams{
		Agent:        "codex",
		Cwd:          "/work/project",
		OwnerSession: "owner-session",
		SessionName:  "tcm-work",
		PaneID:       "%7",
		WindowID:     "@3",
	}

	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "create and get round trip",
			run: func(t *testing.T) {
				store := newTestStore(t, createdAt)
				created := createTestRecord(t, store, params)
				got, err := store.Get(created.DelegationID)
				if err != nil {
					t.Fatalf("Get() error = %v", err)
				}
				assertRecordEqual(t, got, created)
				if !validUUID(created.DelegationID) {
					t.Errorf("DelegationID = %q, want UUID", created.DelegationID)
				}
				if created.ThreadID != "" || created.BoundAt != nil {
					t.Errorf("new record binding = (%q, %v), want unbound", created.ThreadID, created.BoundAt)
				}
				matches, err := filepath.Glob(filepath.Join(store.dir, "*.json"))
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) != 1 {
					t.Fatalf("record files = %v, want one JSON file", matches)
				}
				raw, err := os.ReadFile(matches[0])
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) == "" || raw[len(raw)-1] != '\n' {
					t.Errorf("record file is not newline-terminated: %q", raw)
				}
			},
		},
		{
			name: "bind",
			run: func(t *testing.T) {
				store := newTestStore(t, createdAt)
				created := createTestRecord(t, store, params)
				boundAt := createdAt.Add(time.Minute)
				store.now = func() time.Time { return boundAt }
				bound, err := store.Bind(created.DelegationID, "codex", "thread-123")
				if err != nil {
					t.Fatalf("Bind() error = %v", err)
				}
				if bound.ThreadID != "thread-123" || bound.BoundAt == nil || !bound.BoundAt.Equal(boundAt) {
					t.Errorf("binding = (%q, %v), want (thread-123, %v)", bound.ThreadID, bound.BoundAt, boundAt)
				}
				got, err := store.Get(created.DelegationID)
				if err != nil {
					t.Fatalf("Get() after Bind() error = %v", err)
				}
				assertRecordEqual(t, got, bound)
			},
		},
		{
			name: "find unbound by pane",
			run: func(t *testing.T) {
				store := newTestStore(t, createdAt)
				older := createTestRecord(t, store, params)
				if _, err := store.Bind(older.DelegationID, "codex", "thread-old"); err != nil {
					t.Fatalf("Bind() error = %v", err)
				}
				store.now = func() time.Time { return createdAt.Add(time.Minute) }
				want := createTestRecord(t, store, params)
				otherPane := params
				otherPane.PaneID = "%8"
				createTestRecord(t, store, otherPane)

				got, err := store.FindUnboundByPane("tcm-work", "%7")
				if err != nil {
					t.Fatalf("FindUnboundByPane() error = %v", err)
				}
				assertRecordEqual(t, got, want)
			},
		},
		{
			name: "has unbound",
			run: func(t *testing.T) {
				store := newTestStore(t, createdAt)
				if has, err := store.HasUnbound(); err != nil || has {
					t.Fatalf("empty HasUnbound() = (%v, %v), want (false, nil)", has, err)
				}
				record := createTestRecord(t, store, params)
				if has, err := store.HasUnbound(); err != nil || !has {
					t.Fatalf("created HasUnbound() = (%v, %v), want (true, nil)", has, err)
				}
				if _, err := store.Bind(record.DelegationID, record.Agent, "thread-123"); err != nil {
					t.Fatal(err)
				}
				if has, err := store.HasUnbound(); err != nil || has {
					t.Fatalf("bound HasUnbound() = (%v, %v), want (false, nil)", has, err)
				}
			},
		},
		{
			name: "sweep age boundary",
			run: func(t *testing.T) {
				now := createdAt.Add(8 * 24 * time.Hour)
				store := newTestStore(t, now.Add(-7*24*time.Hour-time.Nanosecond))
				old := createTestRecord(t, store, params)
				store.now = func() time.Time { return now.Add(-7 * 24 * time.Hour) }
				boundary := createTestRecord(t, store, params)
				store.now = func() time.Time { return now }

				removed, err := store.Sweep(7 * 24 * time.Hour)
				if err != nil {
					t.Fatalf("Sweep() error = %v", err)
				}
				if removed != 1 {
					t.Errorf("Sweep() removed = %d, want 1", removed)
				}
				if _, err := store.Get(old.DelegationID); !errors.Is(err, ErrNotFound) {
					t.Errorf("Get(old) error = %v, want ErrNotFound", err)
				}
				if _, err := store.Get(boundary.DelegationID); err != nil {
					t.Errorf("Get(boundary) error = %v", err)
				}
			},
		},
		{
			name: "persistence across reopened store",
			run: func(t *testing.T) {
				dir := t.TempDir()
				store, err := New(dir)
				if err != nil {
					t.Fatalf("New() error = %v", err)
				}
				store.now = func() time.Time { return createdAt }
				want := createTestRecord(t, store, params)

				reopened, err := New(dir)
				if err != nil {
					t.Fatalf("reopen New() error = %v", err)
				}
				got, err := reopened.Get(want.DelegationID)
				if err != nil {
					t.Fatalf("reopened Get() error = %v", err)
				}
				assertRecordEqual(t, got, want)
			},
		},
		{
			name: "concurrent creates",
			run: func(t *testing.T) {
				store := newTestStore(t, createdAt)
				const count = 24
				createErrors := make(chan error, count)
				var workers sync.WaitGroup
				for range count {
					workers.Go(func() {
						_, err := store.Create(params)
						createErrors <- err
					})
				}
				workers.Wait()
				close(createErrors)
				for err := range createErrors {
					if err != nil {
						t.Errorf("Create() error = %v", err)
					}
				}
				records, err := store.List()
				if err != nil {
					t.Fatalf("List() error = %v", err)
				}
				if len(records) != count {
					t.Errorf("List() count = %d, want %d", len(records), count)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func newTestStore(t *testing.T, now time.Time) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	store.now = func() time.Time { return now }
	return store
}

func createTestRecord(t *testing.T, store *Store, params CreateParams) Record {
	t.Helper()
	record, err := store.Create(params)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return record
}

func assertRecordEqual(t *testing.T, got, want Record) {
	t.Helper()
	if got.DelegationID != want.DelegationID ||
		got.Agent != want.Agent ||
		got.Cwd != want.Cwd ||
		got.OwnerSession != want.OwnerSession ||
		got.SessionName != want.SessionName ||
		got.PaneID != want.PaneID ||
		got.WindowID != want.WindowID ||
		!got.CreatedAt.Equal(want.CreatedAt) ||
		got.ThreadID != want.ThreadID ||
		!equalTimePointers(got.BoundAt, want.BoundAt) {
		t.Errorf("record = %#v, want %#v", got, want)
	}
}

func equalTimePointers(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
