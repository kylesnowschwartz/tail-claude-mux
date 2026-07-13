package codexwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRolloutForFollowup(t *testing.T) {
	root := resolvedTempDir(t)
	dir := filepath.Join(root, "2026", "07", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldID := "11111111-1111-1111-1111-111111111111"
	newID := "22222222-2222-2222-2222-222222222222"
	otherID := "33333333-3333-3333-3333-333333333333"
	writeRollout := func(id, cwd string, mod time.Time) string {
		path := filepath.Join(dir, "rollout-2026-07-11T00-00-00-"+id+".jsonl")
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":"session_meta","payload":{"cwd":%q}}`+"\n", cwd)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
		return path
	}
	now := time.Now()
	writeRollout(oldID, "/project", now.Add(-time.Hour))
	wantPath := writeRollout(newID, "/project", now)
	writeRollout(otherID, "/other", now.Add(time.Hour))

	adapter := New(root, "")
	path, id, err := adapter.RolloutForFollowup("/project", "")
	if err != nil {
		t.Fatal(err)
	}
	if path != wantPath || id != newID {
		t.Fatalf("got (%q, %q), want (%q, %q)", path, id, wantPath, newID)
	}

	oldPath, id, err := adapter.RolloutForFollowup("/project", oldID)
	if err != nil {
		t.Fatal(err)
	}
	if oldPath == wantPath || id != oldID {
		t.Fatalf("tracked thread got (%q, %q), want thread %q", oldPath, id, oldID)
	}
}

func TestRolloutForFollowupSkipsMalformedRollout(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "2026", "07", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-07-11T00-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gotPath, gotID, err := New(root, "").RolloutForFollowup("/project", "")
	if err != nil || gotPath != "" || gotID != "" {
		t.Fatalf("got (%q, %q, %v), want empty result without error", gotPath, gotID, err)
	}
}
