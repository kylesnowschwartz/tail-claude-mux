package theming

// Palette file writer (catppuccin pattern) — the Go port of
// packages/runtime/src/server/tmux-palette-file.ts plus the
// applyPaletteToTmux glue from packages/runtime/src/server/index.ts.
//
// The server is the single writer of <configDir>/palette-active.tmux.conf,
// a tmux-source-file-able config containing `set -gq @tcm-thm-* "#xxx"`
// lines and the two statusline glyph constants. tcm.tmux sources this file
// at TPM init (before sourcing header.tmux), so every cold boot /
// kill-server restart / prefix+r reload paints the status line with the
// correct palette on first repaint. On runtime theme changes the server
// calls Apply again, which re-writes the file AND issues `tmux source-file`
// so the change lands live.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
)

// Logger receives structured log lines; same shape as the TS server's
// log(msg, data). A nil Logger silences the package.
type Logger func(msg string, data map[string]any)

// PaletteFileName is the palette file's name under the config dir
// (tmux-palette-file.ts PALETTE_FILE_PATH, with the dir injected).
const PaletteFileName = "palette-active.tmux.conf"

// PaletteFilePath returns the palette file destination for a config dir.
func PaletteFilePath(configDir string) string {
	return filepath.Join(configDir, PaletteFileName)
}

// paletteFileTokens mirrors PALETTE_TOKENS in tmux-palette-file.ts: the
// active palette file must contain exactly these tokens, in this order —
// the vendored fallback at
// integrations/tmux-plugin/themes/default-palette.tmux.conf agrees.
var paletteFileTokens = []string{
	"base", "text", "blue", "surface0", "surface2",
	"overlay0", "yellow", "red", "green",
}

// toTmuxColour translates a tcm palette value to a tmux-renderable colour.
// The "transparent" theme stores the literal string "transparent" for base
// surfaces; tmux understands "default" but not "transparent". Shared by the
// palette body and the header's fg emission (the two TS copies are kept in
// lockstep by comment; one function here).
func toTmuxColour(value string) string {
	if value == "transparent" {
		return "default"
	}
	return value
}

// BuildPaletteFileBody builds the file body — byte-identical to
// tmux-palette-file.ts buildPaletteFileBody (including its provenance
// comments, kept verbatim so TS- and Go-written files diff clean). Each
// option uses `-gq` (global, quiet) so a re-source silently overwrites.
func BuildPaletteFileBody(theme Theme, themeName string) string {
	header := "# tcm tmux palette — active. Auto-generated; do not edit."
	if themeName != "" {
		header = fmt.Sprintf("# tcm tmux palette — active (%s). Auto-generated; do not edit.", themeName)
	}
	lines := []string{
		header,
		"# Single writer: packages/runtime/src/server/tmux-palette-file.ts.",
		"# Read by: tcm.tmux at TPM init; bun server re-runs `tmux source-file` on theme change.",
		"",
	}
	for _, token := range paletteFileTokens {
		value := toTmuxColour(theme.Palette.token(token))
		// Pad token name to keep columns aligned with the vendored default
		// for diffability (TS padEnd(8)).
		lines = append(lines, fmt.Sprintf(`set -gq @tcm-thm-%-8s "%s"`, token, value))
	}
	lines = append(lines,
		"",
		fmt.Sprintf(`set -gq @tcm-shell-glyph        "%s"`, StatuslineShell),
		fmt.Sprintf(`set -gq @tcm-last-window-glyph  "%s"`, StatuslineLastWindow),
		"",
	)
	return strings.Join(lines, "\n")
}

// PaletteWriter writes the palette file and applies it live. It carries the
// last-written body across calls so an unchanged theme doesn't trigger a
// redundant file write or `tmux source-file` (tmux-palette-file.ts's
// module-level lastWrittenBody, made instance state).
type PaletteWriter struct {
	configDir string
	tmux      *tmux.Tmux
	log       Logger
	lastBody  string
}

// NewPaletteWriter returns a writer rooted at configDir (~/.config/tcm in
// production; injected so tests use a temp dir) issuing tmux commands
// through t.
func NewPaletteWriter(configDir string, t *tmux.Tmux, log Logger) *PaletteWriter {
	return &PaletteWriter{configDir: configDir, tmux: t, log: log}
}

// Apply ports index.ts applyPaletteToTmux: resolve the active theme from
// disk (external > config > default) and write+source the palette file.
// Idempotent on body content; safe to call repeatedly. Call at server boot,
// on the set-theme command (after persisting config.json), and whenever the
// caller detects an active-theme.json change.
func (w *PaletteWriter) Apply(reason string) {
	theme, themeName := ResolveActiveTheme(w.configDir)
	w.logf("apply", map[string]any{"reason": reason, "themeName": themeName})
	w.Write(theme, themeName)
}

// Write ports tmux-palette-file.ts writePaletteFile: write the palette file
// for an already-resolved theme and apply it live via `tmux source-file -q`.
// The `-q` flag swallows the no-such-tmux-server error so this is safe from
// a process not running inside a tmux session (e.g. external triggers).
func (w *PaletteWriter) Write(theme Theme, themeName string) {
	body := BuildPaletteFileBody(theme, themeName)
	if body == w.lastBody {
		w.logf("palette-file unchanged", map[string]any{"themeName": themeName})
		return
	}
	dest := PaletteFilePath(w.configDir)
	if err := os.MkdirAll(w.configDir, 0o755); err != nil {
		w.logf("palette-file write failed", map[string]any{"error": err.Error(), "path": dest})
		return
	}
	if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
		w.logf("palette-file write failed", map[string]any{"error": err.Error(), "path": dest})
		return
	}
	w.lastBody = body
	w.logf("palette-file written", map[string]any{"path": dest, "themeName": themeName, "bytes": len(body)})

	// Apply live. If we're not inside a tmux server, -q swallows the error.
	if _, err := w.tmux.Run("source-file", "-q", dest); err != nil {
		w.logf("palette-file source-file failed", map[string]any{"error": err.Error()})
	}
}

func (w *PaletteWriter) logf(msg string, data map[string]any) {
	if w.log != nil {
		w.log(msg, data)
	}
}
