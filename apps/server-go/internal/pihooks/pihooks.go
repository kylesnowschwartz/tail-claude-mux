// Package pihooks installs tcm's Pi extension: a symlink named
// tcm-status.ts in the Pi agent directory's extensions folder, pointing at
// integrations/pi-extension/tcm-status.ts in the tcm checkout, the way the
// Claude and Codex hooks point at the checkout's scripts/hook.sh.
package pihooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the extension's file name in Pi's extensions folder.
const FileName = "tcm-status.ts"

// Result says what Install did.
type Result int

const (
	// Installed means the link was created or replaced.
	Installed Result = iota
	// AlreadyInstalled means the link already pointed at the source.
	AlreadyInstalled
	// NoAgentDir means the Pi agent directory does not exist; nothing was
	// written.
	NoAgentDir
)

// Install links <agentDir>/extensions/tcm-status.ts to source. It creates
// the extensions folder when missing and replaces only its own file.
func Install(agentDir, source string) (Result, error) {
	if info, err := os.Stat(agentDir); err != nil || !info.IsDir() {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return NoAgentDir, nil
		}
		return NoAgentDir, err
	}
	if _, err := os.Stat(source); err != nil {
		return 0, fmt.Errorf("pi extension source: %w", err)
	}
	dir := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	target := filepath.Join(dir, FileName)
	if current, err := os.Readlink(target); err == nil && current == source {
		return AlreadyInstalled, nil
	}
	tmp := target + ".tcm-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(source, tmp); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return Installed, nil
}
