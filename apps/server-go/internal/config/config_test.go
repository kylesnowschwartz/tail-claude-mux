// Ported from packages/runtime/test/save-config.test.ts (merge semantics;
// the load-side cases in config.test.ts cover the TS loadConfig defaults,
// which live in state.LoadSidebarWidth/LoadSidebarPosition on the Go side).
package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return m
}

func TestSave_CreatesFileAndDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tcm")
	if err := Save(dir, map[string]any{"sidebarWidth": 40}); err != nil {
		t.Fatal(err)
	}
	if got := string(read(t, dir)["sidebarWidth"]); got != "40" {
		t.Errorf("sidebarWidth = %s, want 40", got)
	}
}

func TestSave_MergesOverExisting(t *testing.T) {
	dir := t.TempDir()
	seed := `{"theme":"catppuccin-latte","sidebarWidth":33,"customUnknownKey":{"nested":true}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, map[string]any{"sidebarWidth": 45}); err != nil {
		t.Fatal(err)
	}
	m := read(t, dir)
	if string(m["sidebarWidth"]) != "45" {
		t.Errorf("sidebarWidth = %s, want 45", m["sidebarWidth"])
	}
	if string(m["theme"]) != `"catppuccin-latte"` {
		t.Errorf("theme = %s, want preserved", m["theme"])
	}
	// The TS spread preserved keys it didn't model; so must Save.
	// (MarshalIndent may reformat — compare compacted.)
	var compact bytes.Buffer
	if err := json.Compact(&compact, m["customUnknownKey"]); err != nil {
		t.Fatal(err)
	}
	if compact.String() != `{"nested":true}` {
		t.Errorf("unknown key = %s, want preserved", compact.String())
	}
}

func TestSave_ReplacesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json {"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, map[string]any{"theme": "nord"}); err != nil {
		t.Fatal(err)
	}
	if got := string(read(t, dir)["theme"]); got != `"nord"` {
		t.Errorf("theme = %s, want \"nord\"", got)
	}
}

func TestSave_TrailingNewline(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, map[string]any{"sidebarWidth": 33}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Error("config.json must end with a newline (TS writeFileSync parity)")
	}
}
