# Spec: tcm companion pane

**Status:** v1 — implemented
**Origin:** `.agent-history/COMPANION-PANE-SPEC.md` draft (2026-07-05), promoted on acceptance
**Last updated:** 2026-07

This spec is the lasting reference for tcm's companion pane: a generic
second managed pane — an arbitrary user-configured command — stacked
below the sidebar in the same column, sharing the sidebar's
ensure/enforce/stash lifecycle. Implementation lives in
`apps/server-go/internal/tmux/companion.go` (tmux operations) and
`apps/server-go/internal/server/sidebar.go` (orchestration).

---

## 1. Goals and non-goals

### Goals

- One generic feature: `command` + `rows`, nothing more. tcm never knows
  what the command is; the guest process never knows about tcm. The same
  discipline CONTRACTS.md applies to wire types applies here to the
  process boundary — tcm provides a pane, the guest provides behavior.
- **Off by default.** No `companionPane` config block → byte-identical
  behavior to a build without the feature: zero extra tmux calls. That
  is the acceptance baseline, pinned by a behavior-freeze test on the
  sidebar spawn path.
- Reuse, don't copy: the sidebar's spawn, stash-restore, orphan-prune,
  and drift-enforcement machinery serve both pane kinds through shared
  helpers (`SpawnManagedPane`, `markPane`, `StashPane`,
  `PruneStashOrphans`).

Motivating consumer: `gearshifter strip --compact`, a persistent Bubble
Tea widget designed for a sidebar-width footprint.

### Non-goals (v1)

- No wire-contract changes: no new wire types, no TUI involvement, no
  dedicated `/toggle-companion` route.
- No per-companion theme/glyph integration; the guest owns its own
  rendering and must tolerate whatever column width results (width
  floors are sidebar-content-derived and stay companion-ignorant).
- No respawn backoff or health-checking beyond the hook-debounced
  ensure.
- One companion per window, one config block — not a list. Generalize
  only when a second real guest exists.

---

## 2. Architecture

```
        ~/.config/tcm/config.json { companionPane: {command, rows} }
                          |
                          v  state.LoadCompanionPane (boot)
        +--------------------------------------------+
        |  tcm server (Server.CompanionPane)         |
        |                                            |
        |  ensureSidebarInWindow ──┐                 |
        |  spawnInActiveWindows ───┼─> ensureCompanionInWindow
        |  handleToggle (show) ────┘        |        |
        |                                   v        |
        |          tmux.SpawnCompanion(sidebarPaneID, rows, command)
        |                                            |
        |  ensure / toggle-on / client-resized       |
        |        └─> enforceCompanionHeight ─> resize-pane -y
        +--------------------------------------------+
                          |
                          v
        window column:  [ sidebar (tcm TUI)  ]
                        [ companion (guest)  ]  <- rows lines tall
```

Every window that gets a sidebar also gets a companion pane: same
column, below the sidebar, `rows` lines tall, full column width (it
shares the sidebar's vertical edge, so width enforcement comes free —
`resize-pane -x` on the sidebar moves both).

---

## 3. Configuration

New top-level block in `~/.config/tcm/config.json`:

```json
{
  "companionPane": {
    "command": "gearshifter strip --compact",
    "rows": 8
  }
}
```

- `command` (string, required): run via the user's shell exactly as
  `split-window` runs any shell command. Empty/absent → feature off.
  The new pane's cwd defaults to the sidebar pane's; it is unspecified —
  the guest must not depend on it.
- `rows` (int, default 8): pane height in lines. Clamped by
  `tmux.ClampCompanionHeight` to a floor of 3 and a ceiling of half the
  window height; the floor wins when they conflict. Deliberately
  separate from `widthsync`'s pinned width constants.

Loaded once at boot by `state.LoadCompanionPane(configDir)` (anonymous
struct decode of just this block, zero value = disabled) and held on
`server.Server.CompanionPane`. `config.Save` is a merge-write that
preserves unknown keys, so nothing else changes. Runtime toggling
requires a server restart, same as `sidebarPosition` — and both restart
directions work: `BootstrapSidebars` ensures companions under adopted
sidebars when the feature was just enabled, and kills leftover
companion panes (live and stashed) when it was just disabled.

---

## 4. Pane contract

### 4.1 Marker and discovery

| Surface | Sidebar | Companion |
|---|---|---|
| Pane option (stable marker) | `@tcm-sidebar = "1"` | `@tcm-companion = "1"` |
| Pane title (legacy/fallback id) | `tcm-sidebar` | `tcm-companion` |

`ListAllPanes` detects both via option OR title (`Pane.Sidebar`,
`Pane.Companion`); the option is the stable marker because escape
sequences from the pane's process can rewrite the title. The listing
also carries `Height` (`pane_height`) and `WindowHeight`
(`window_height`) for the clamp and drift detection.

`Pane.Managed()` (`Sidebar || Companion`) is the single "this pane is
tcm's, not the user's" predicate; every skip-tcm filter must use it:
`ActiveDirs`, the agent pane scanner, agent-pane kill resolution, orphan
cleanup, stash pruning, and `uninstall.sh`'s pane sweep all do.
`ManagedPanes` is the matching
non-stash filter used by toggle and quit.

### 4.2 Lifecycle

- **Spawn** (`tmux.SpawnCompanion`): targets the window's **sidebar
  pane** (which must exist first) with `split-window -d -v -l <rows>` —
  no `-f`, so the pane stays inside the column. A stashed companion is
  restored via `join-pane -d -v` instead, preserving the running guest.
  Both paths pass `-d`, so restoring or spawning a companion preserves
  the user's current focus and matches the detached sidebar invariant.
- **Ensure** (`server.ensureCompanionInWindow`): hook-driven and
  debounced (150 ms) exactly like the sidebar ensure; no-op when the
  feature is off, the window has no sidebar, or a companion already
  exists. If the guest process exits, the pane closes and the next
  window/session hook respawns it — self-healing, no tight loop
  possible.
- **Height enforcement** (`server.enforceCompanionHeight`): session
  switches redistribute pane sizes proportionally; every site that
  re-imposes sidebar width (ensure tail, toggle-on, client-resized)
  also re-imposes clamped companion height via `resize-pane -y`.
- **Toggle** (`Ctrl-t` / `prefix o → t`): hide stashes sidebar and
  companion panes together via the shared `StashPane`; show restores
  both from the stash. The column is one unit — no separate companion
  toggle in v1. `PruneStashOrphans` spares both managed titles.
- **Restart** (`POST /restart` with reload): the reload kill
  (`server.killForReload`) sweeps companion panes alongside sidebar
  panes before the respawn, so a tcm restart restarts the guest process
  too and no window is left holding a stranded full-height companion
  until its next visit. The respawn covers each session's active
  window; other windows heal on their first visit, exactly like
  sidebars.
- **Stranded-companion rule**: a fresh sidebar spawn in a window that
  already holds a companion kills that companion first
  (`server.killStrandedCompanions`), and the ensure tail respawns it
  below the new sidebar. Without this, a dead sidebar (crash, stale
  panes from a dead server) leaves the companion holding a full-height
  column that the idempotent ensure would never repair. Guest state is
  lost only on sidebar-death paths — acceptable for v1.
- **Orphan cleanup** (`server.handlePaneExited`): when every remaining
  non-stash pane in a window is tcm-managed (sidebar or companion), the
  user's last real pane is gone and all managed panes are killed.
  Reduces to the old "sidebar alone in window" rule when no companion
  exists.
- **Quit** (`server.quitAll`): kills companion panes alongside sidebar
  panes and the stash session.

---

## 5. Function signatures

```go
// internal/tmux
func (t *Tmux) SpawnManagedPane(targetPane string, splitFlags []string,
    size int, command, markerOption, title string) string
func (t *Tmux) SpawnCompanion(sidebarPaneID string, rows int, command string) string
func (t *Tmux) StashPane(paneID string, panes []Pane)
func (t *Tmux) ResizePaneHeight(paneID string, height int)
func CompanionPanes(panes []Pane) []Pane
func ClampCompanionHeight(rows, windowHeight int) int

// internal/state
func LoadCompanionPane(configDir string) CompanionPaneConfig

// internal/server
func (s *Server) ensureCompanionInWindow(windowID string, panes []tmux.Pane, freshSidebarID string)
func (s *Server) enforceGeometry(skipSession string) // one listing feeds width + height enforcement
func (s *Server) killStrandedCompanions(windowID string, panes []tmux.Pane)
func (s *Server) killForReload(panes []tmux.Pane) // reload kill: sidebars + companions + stash
```

Behaviour contract:

- **Idempotent.** Ensure with an existing companion issues no spawn.
- **Disabled = silent.** Empty `Command` short-circuits before any tmux
  call in every companion entry point.
- **Sidebar-anchored.** The companion only ever spawns against an
  existing sidebar pane; no sidebar, no companion.
- **One listing per pass.** Ensure and enforce reuse the caller's pane
  listing; the only deliberate re-list is `SpawnCompanion`'s stash scan,
  because a listing shared across windows would offer the same stashed
  pane twice and the second `join-pane` would steal the first window's
  companion.

---

## 6. Failure modes and recovery

- Guest crashes or user kills the pane → next hook-driven ensure
  respawns it (debounced, no tight loop).
- Sidebar dies while the companion lives → stranded-companion rule
  kills and respawns the companion under the fresh sidebar; guest state
  is lost on this path.
- Stash pane title drift (dead guest, tmux automatic-rename) →
  `PruneStashOrphans` kills it; the next ensure spawns fresh.
- "Pane too small" on stash join → inherited fix: the stash window is
  pre-resized to 200x200 by the shared `StashPane`.
- Window height too small for `rows` → clamp holds the companion to
  half the window, never below 3 rows; when even 3 rows can't fit
  (window shorter than ~7 rows), `ClampCompanionHeight` returns 0 and
  ensure/enforce skip the window entirely instead of retrying a split
  tmux rejects on every hook.
- Feature disabled while panes exist → boot-time teardown kills every
  leftover companion (windows and stash) using the listing bootstrap
  already takes; no extra tmux execs when there are none.

---

## 7. Test surface

`internal/tmux/tmux_test.go`:

- `ListAllPanes` parses the `@tcm-companion` marker, the
  `tcm-companion` title fallback, and `Height`/`WindowHeight`.
- `SpawnManagedPane` exact command sequence;
  `TestSpawnSidebar_CommandSequenceFrozen` pins the sidebar path as
  byte-identical across the extraction (CS-007 behavior freeze).
- `SpawnCompanion` fresh-spawn and restore-from-stash paths.
- `ClampCompanionHeight` floor/ceiling/degenerate cases.
- `PruneStashOrphans` spares both managed titles, kills strangers.

`internal/server/sidebar_test.go`:

- Ensure idempotence; feature off → zero tmux traffic.
- Orphan predicate: `[sidebar+companion]` alone → both killed;
  `[main+sidebar+companion]` → untouched; `[sidebar]` alone → killed.
- Toggle hide/show round-trips the companion through the stash.
- Stranded-companion: `[main+companion]` → fresh sidebar spawn kills
  the companion, then both respawn.
- Reload kill: feature on → companions killed alongside sidebars;
  feature off → sidebars only (bootstrap's teardown owns leftovers).

---

## 8. Future work (not in v1)

- `@tcm-companion-*` tmux-option surface in `tcm.tmux` mirroring
  `@tcm-width`.
- Multiple companions per window (config list) — only when a second
  real guest exists.
- Dedicated `/toggle-companion` route if a guest ever needs independent
  visibility.
