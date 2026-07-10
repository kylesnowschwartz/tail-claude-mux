// Package codexhooks registers tcm's command hooks without replacing the
// user's existing Codex configuration.
package codexhooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var Events = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PermissionRequest",
	"Stop",
}

type hookGroup struct {
	Hooks []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// Register appends missing tcm hook groups and atomically replaces path.
// Unknown top-level keys, event groups, and handler fields are retained.
func Register(path, hookScript string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	root := map[string]json.RawMessage{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
	}

	events := map[string][]json.RawMessage{}
	if hooksRaw := root["hooks"]; len(hooksRaw) != 0 {
		if err := json.Unmarshal(hooksRaw, &events); err != nil {
			return nil, fmt.Errorf("decode %s hooks: %w", path, err)
		}
	}

	added := make([]string, 0, len(Events))
	for _, event := range Events {
		if containsTCMHook(events[event], hookScript) {
			continue
		}
		command := fmt.Sprintf("%s %s codex", shellDoubleQuote(hookScript), event)
		group, err := json.Marshal(hookGroup{Hooks: []hookHandler{{Type: "command", Command: command, Timeout: 10}}})
		if err != nil {
			return nil, err
		}
		events[event] = append(events[event], group)
		added = append(added, event)
	}
	if len(added) == 0 {
		return nil, nil
	}

	hooksRaw, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}
	root["hooks"] = hooksRaw
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	if err := atomicWrite(path, out); err != nil {
		return nil, err
	}
	return added, nil
}

func containsTCMHook(groups []json.RawMessage, hookScript string) bool {
	quotedScript := shellDoubleQuote(hookScript)
	for _, raw := range groups {
		var group struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if json.Unmarshal(raw, &group) != nil {
			continue
		}
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, hookScript) || strings.Contains(hook.Command, quotedScript) {
				return true
			}
		}
	}
	return false
}

// shellDoubleQuote protects a path inside a shell command while matching
// the double-quoted path convention used by Codex hook configuration.
func shellDoubleQuote(path string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
	).Replace(path)
	return `"` + escaped + `"`
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hooks.json-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
