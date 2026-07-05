// Package config ports packages/runtime/src/config.ts — read/merge-write
// access to ~/.config/tcm/config.json. Save merges updates over whatever
// is on disk, preserving keys this package doesn't model (the TS spread
// did the same), so the Go server and any other writer can share the file
// without clobbering each other's fields.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Save merges updates into configDir/config.json, creating the directory
// and file as needed. Unknown existing keys are preserved verbatim.
func Save(configDir string, updates map[string]any) error {
	path := filepath.Join(configDir, "config.json")

	existing := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		// A corrupt file decodes to nothing and gets replaced — same as
		// the TS loadConfig catch → {} fallback.
		_ = json.Unmarshal(raw, &existing)
	}
	for k, v := range updates {
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		existing[k] = enc
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
