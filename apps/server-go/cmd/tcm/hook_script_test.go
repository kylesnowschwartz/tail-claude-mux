package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHookScriptOptionalAgent(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "scripts", "hook.sh"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	capture := filepath.Join(bin, "body.json")
	fakeCurl := `#!/usr/bin/env bash
while [[ $# -gt 0 ]]; do
  if [[ "$1" == "-d" ]]; then
    printf '%s' "$2" > "$CAPTURE"
    exit 0
  fi
  shift
done
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fakeCurl), 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) map[string]json.RawMessage {
		t.Helper()
		cmd := exec.Command(script, args...)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CAPTURE="+capture)
		cmd.Stdin = bytes.NewBufferString(`{"session_id":"s1","cwd":"/project"}`)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook.sh: %v: %s", err, out)
		}
		raw, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("body %q: %v", raw, err)
		}
		return body
	}

	withAgent := run("Stop", "codex")
	if string(withAgent["event"]) != `"Stop"` || string(withAgent["agent"]) != `"codex"` {
		t.Fatalf("agent invocation body = %s", mustJSON(t, withAgent))
	}
	withoutAgent := run("Stop")
	if _, ok := withoutAgent["agent"]; ok {
		t.Fatalf("single-argument invocation added agent: %s", mustJSON(t, withoutAgent))
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
