package theming

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
)

// fakeTmux records every Run invocation and answers via fn (nil fn = ok/"").
func fakeTmux(fn func(args []string) (string, error)) (*tmux.Tmux, *[][]string) {
	calls := &[][]string{}
	return &tmux.Tmux{Run: func(args ...string) (string, error) {
		*calls = append(*calls, args)
		if fn != nil {
			return fn(args)
		}
		return "", nil
	}}, calls
}

func TestBuildPaletteFileBody(t *testing.T) {
	tests := []struct {
		name      string
		theme     Theme
		themeName string
		want      string
	}{
		{
			name:      "default theme with name (matches vendored fallback values)",
			theme:     BuiltinTheme("catppuccin-mocha"),
			themeName: "catppuccin-mocha",
			want: "# tcm tmux palette — active (catppuccin-mocha). Auto-generated; do not edit.\n" +
				"# Single writer: packages/runtime/src/server/tmux-palette-file.ts.\n" +
				"# Read by: tcm.tmux at TPM init; bun server re-runs `tmux source-file` on theme change.\n" +
				"\n" +
				"set -gq @tcm-thm-base     \"#1e1e2e\"\n" +
				"set -gq @tcm-thm-text     \"#cdd6f4\"\n" +
				"set -gq @tcm-thm-blue     \"#89b4fa\"\n" +
				"set -gq @tcm-thm-surface0 \"#313244\"\n" +
				"set -gq @tcm-thm-surface2 \"#585b70\"\n" +
				"set -gq @tcm-thm-overlay0 \"#6c7086\"\n" +
				"set -gq @tcm-thm-yellow   \"#f9e2af\"\n" +
				"set -gq @tcm-thm-red      \"#f38ba8\"\n" +
				"set -gq @tcm-thm-green    \"#a6e3a1\"\n" +
				"\n" +
				"set -gq @tcm-shell-glyph        \"\uea85\"\n" +
				"set -gq @tcm-last-window-glyph  \"\U000F17B3\"\n",
		},
		{
			name:      "no theme name drops the label",
			theme:     BuiltinTheme(""),
			themeName: "",
			want: "# tcm tmux palette — active. Auto-generated; do not edit.\n" +
				"# Single writer: packages/runtime/src/server/tmux-palette-file.ts.\n" +
				"# Read by: tcm.tmux at TPM init; bun server re-runs `tmux source-file` on theme change.\n" +
				"\n" +
				"set -gq @tcm-thm-base     \"#1e1e2e\"\n" +
				"set -gq @tcm-thm-text     \"#cdd6f4\"\n" +
				"set -gq @tcm-thm-blue     \"#89b4fa\"\n" +
				"set -gq @tcm-thm-surface0 \"#313244\"\n" +
				"set -gq @tcm-thm-surface2 \"#585b70\"\n" +
				"set -gq @tcm-thm-overlay0 \"#6c7086\"\n" +
				"set -gq @tcm-thm-yellow   \"#f9e2af\"\n" +
				"set -gq @tcm-thm-red      \"#f38ba8\"\n" +
				"set -gq @tcm-thm-green    \"#a6e3a1\"\n" +
				"\n" +
				"set -gq @tcm-shell-glyph        \"\uea85\"\n" +
				"set -gq @tcm-last-window-glyph  \"\U000F17B3\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildPaletteFileBody(tt.theme, tt.themeName); got != tt.want {
				t.Errorf("body mismatch:\ngot:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestBuildPaletteFileBodyTransparent(t *testing.T) {
	body := BuildPaletteFileBody(BuiltinTheme("transparent"), "transparent")
	if !strings.Contains(body, "set -gq @tcm-thm-base     \"default\"") {
		t.Errorf("transparent base not translated to tmux 'default':\n%s", body)
	}
	if strings.Contains(body, "transparent\"") {
		t.Errorf("literal 'transparent' leaked into a palette value:\n%s", body)
	}
}

func TestPaletteWriterApply(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "config.json"), `{"theme":"nord"}`)
	tm, calls := fakeTmux(nil)
	w := NewPaletteWriter(dir, tm, nil)

	w.Apply("server-boot")

	dest := PaletteFilePath(dir)
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("palette file not written: %v", err)
	}
	if !strings.Contains(string(raw), "set -gq @tcm-thm-blue     \"#81a1c1\"") {
		t.Errorf("palette file missing nord blue:\n%s", raw)
	}
	if !strings.Contains(string(raw), "(nord)") {
		t.Errorf("palette file header missing theme label:\n%s", raw)
	}
	if len(*calls) != 1 {
		t.Fatalf("tmux calls = %d, want 1 source-file", len(*calls))
	}
	wantCall := []string{"source-file", "-q", dest}
	if !equalArgs((*calls)[0], wantCall) {
		t.Errorf("tmux call = %v, want %v", (*calls)[0], wantCall)
	}

	// Second apply with an unchanged theme: no rewrite, no re-source.
	w.Apply("set-theme")
	if len(*calls) != 1 {
		t.Errorf("unchanged theme re-sourced: %d calls, want 1", len(*calls))
	}

	// External theme lands: re-write and re-source.
	mustWrite(t, filepath.Join(dir, "active-theme.json"),
		`{"name":"dayfox","palette":{"blue":"#2848a9"}}`)
	w.Apply("external-theme-change")
	raw, _ = os.ReadFile(dest)
	if !strings.Contains(string(raw), "set -gq @tcm-thm-blue     \"#2848a9\"") {
		t.Errorf("palette file not rewritten for external theme:\n%s", raw)
	}
	if !strings.Contains(string(raw), "(dayfox)") {
		t.Errorf("palette header missing external theme label:\n%s", raw)
	}
	if len(*calls) != 2 {
		t.Errorf("tmux calls = %d, want 2 after theme change", len(*calls))
	}
}

func TestPaletteWriterCreatesConfigDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "tcm")
	tm, _ := fakeTmux(nil)
	w := NewPaletteWriter(dir, tm, nil)
	w.Apply("server-boot")
	if _, err := os.Stat(PaletteFilePath(dir)); err != nil {
		t.Fatalf("palette file missing after Apply into non-existent dir: %v", err)
	}
}

func TestPaletteWriterSourceFileFailureIsLoggedNotFatal(t *testing.T) {
	dir := t.TempDir()
	tm, _ := fakeTmux(func(args []string) (string, error) {
		return "", errors.New("no server running")
	})
	var msgs []string
	w := NewPaletteWriter(dir, tm, func(msg string, _ map[string]any) {
		msgs = append(msgs, msg)
	})
	w.Apply("server-boot")
	// The write still lands and lastBody advances (idempotence intact).
	if _, err := os.Stat(PaletteFilePath(dir)); err != nil {
		t.Fatalf("palette file not written: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m == "palette-file source-file failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("source-file failure not logged; msgs = %v", msgs)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
