package codexhooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterFreshAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "hooks.json")
	added, err := Register(path, "/repo/scripts/hook.sh")
	if err != nil || len(added) != len(Events) {
		t.Fatalf("first register: added=%v err=%v", added, err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	added, err = Register(path, "/repo/scripts/hook.sh")
	if err != nil || len(added) != 0 {
		t.Fatalf("second register: added=%v err=%v", added, err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent registration rewrote the file")
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".hooks.json-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("atomic temp files left behind: %v, err=%v", temps, err)
	}
}

func TestRegisterPreservesForeignHooksAndUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	original := `{"owner":"user","hooks":{"Stop":[{"matcher":"x","hooks":[{"type":"command","command":"foreign","custom":true}]}],"FutureEvent":[{"future":1}]}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(path, "/repo/scripts/hook.sh"); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["owner"]) != `"user"` {
		t.Fatalf("owner lost: %s", got["owner"])
	}
	var hooks map[string][]json.RawMessage
	if err := json.Unmarshal(got["hooks"], &hooks); err != nil {
		t.Fatal(err)
	}
	if len(hooks["Stop"]) != 2 || len(hooks["FutureEvent"]) != 1 {
		t.Fatalf("foreign groups lost: %#v", hooks)
	}
}

func TestRegisterRecognizesExistingTCMGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	raw := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/repo/scripts/hook.sh old args","timeout":99}]}]}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := Register(path, "/repo/scripts/hook.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range added {
		if event == "Stop" {
			t.Fatal("existing tcm Stop group was duplicated")
		}
	}
}

func TestRegisterQuotesHookScriptPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	hookScript := `/repo with space/$scripts/"hook".sh`
	if _, err := Register(path, hookScript); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	got := doc.Hooks["Stop"][0].Hooks[0].Command
	want := `"/repo with space/\$scripts/\"hook\".sh" Stop codex`
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}

	added, err := Register(path, hookScript)
	if err != nil || len(added) != 0 {
		t.Fatalf("second register: added=%v err=%v", added, err)
	}
}
