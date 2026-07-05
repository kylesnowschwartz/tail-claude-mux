// Package theming is the Go port of the bun server's tmux palette-file
// writer and tmux header synchroniser:
//
//   - packages/runtime/src/server/tmux-palette-file.ts  → PaletteWriter
//   - packages/runtime/src/server/tmux-header-sync.ts   → HeaderSync
//   - packages/runtime/src/themes.ts                    → theme resolution
//     (ported only to the extent the two writers need: palette tokens)
//
// Unlike the TS runtime, this package ships NO file watcher for
// ~/.config/tcm/active-theme.json. PaletteWriter.Apply is a pure "resolve
// from disk and apply" call — the caller decides when to re-apply (server
// boot, the set-theme command, and whenever it detects an external theme
// change). Same for HeaderSync.Sync: the server calls it from its broadcast
// path; nothing here schedules itself.
//
// Everything is injected: the config dir path and the *tmux.Tmux runner are
// constructor parameters, and the Clawd-font probe takes the home dir as an
// argument. No os.UserHomeDir / os.Getwd inside logic.
package theming
