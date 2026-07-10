package codexwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewestRolloutForCwd(t *testing.T) {
	root := t.TempDir()
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

	path, id := New(root, "").NewestRolloutForCwd("/project")
	if path != wantPath || id != newID {
		t.Fatalf("got (%q, %q), want (%q, %q)", path, id, wantPath, newID)
	}
}
