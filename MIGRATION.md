# Migration: `opensessions` → `tail-claude-mux` (tcm)

This file is the **user-side checklist** for the identity rename that landed
between `29216ac` (last `opensessions` commit) and `7a25c13` (rename complete).

The repo has been renamed `opensessions` → `tail-claude-mux` (short name: `tcm`).
Every internal identifier — npm packages, tmux options, env vars, filesystem
paths, plugin filename — has been flipped. This is a **breaking change** for
anyone (you, me, future-me) who has the old names wired into their machine.

If you do nothing, nothing will work. The list below is the minimum to get
back to a green machine.

## TL;DR — fastest path

```bash
# 1. Update tmux.conf
sed -i '' \
  -e "s|Ataraxy-Labs/opensessions|kylesnowschwartz/tail-claude-mux|g" \
  -e "s|@opensessions-|@tcm-|g" \
  ~/.tmux.conf

# 2. Move config dir
mv ~/.config/opensessions ~/.config/tcm 2>/dev/null || true

# 3. Re-install plugin via TPM (prefix + I)
#    Old plugin dir becomes orphaned and can be removed:
rm -rf ~/.tmux/plugins/opensessions

# 4. Kill any stale stash session
tmux kill-session -t _os_stash 2>/dev/null || true

# 5. Reload tmux
tmux source-file ~/.tmux.conf
```

That's enough for the common case. If you have direnv, dev-mode tmux setup,
or extra customizations, read the rest.

---

## 1. Tmux configuration (`~/.tmux.conf`)

### TPM plugin line — REQUIRED

```diff
- set -g @plugin 'Ataraxy-Labs/opensessions'
+ set -g @plugin 'kylesnowschwartz/tail-claude-mux'
```

After changing this, `prefix + I` to make TPM clone the new repo. The new
plugin will live at `~/.tmux/plugins/tail-claude-mux/`. The old
`~/.tmux/plugins/opensessions/` becomes orphaned and can be deleted.

### Option overrides — IF YOU HAVE ANY

Every `@opensessions-*` option is now `@tcm-*`. You probably have none of
these set; defaults are applied if unset. If you do:

| Old                                  | New                          |
| ------------------------------------ | ---------------------------- |
| `@opensessions-header`               | `@tcm-header`                |
| `@opensessions-header-status-aware`  | `@tcm-header-status-aware`   |
| `@opensessions-agent-glyphs`         | `@tcm-agent-glyphs`          |
| `@opensessions-index-keys`           | `@tcm-index-keys`            |
| `@opensessions-focus-global-key`     | `@tcm-focus-global-key`      |
| `@opensessions-width`                | `@tcm-width`                 |
| `@opensessions-prefix-key`           | `@tcm-prefix-key`            |

### Dev-mode line — IF YOU USE `scripts/toggle-dev.sh`

If you've ever run dev mode (sourcing the local workspace instead of TPM),
your tmux.conf may contain:

```diff
- run '/path/to/opensessions/opensessions.tmux'
+ run '/path/to/tail-claude-mux/tcm.tmux'
```

Two flips needed: the directory name and the plugin filename. The
`scripts/toggle-dev.sh` script has already been updated to detect both
patterns; running it will keep this line in sync.

## 2. Config directory

```bash
mv ~/.config/opensessions ~/.config/tcm
```

Contents preserved:
- `~/.config/tcm/config.json` — your active mux choice, plugin allowlist, etc.
- `~/.config/tcm/active-theme.json` — current theme override (if set)
- `~/.config/tcm/session-order.json` — sidebar session ordering
- `~/.config/tcm/plugins/` — any local plugins (TS files)

If you have local plugins under `plugins/`, no edits are required as long
as they `import type { ... } from "@tcm/runtime"`. Old `@opensessions/runtime`
imports will silently no-op (the plugin will fail to register but won't
crash other things).

## 3. Pi extension symlink — IF YOU USE PI INTEGRATION

```bash
# old symlink:
rm ~/.pi/agent/extensions/opensessions
# re-run setup to create the new one:
bun run scripts/setup-pi-extension.ts
```

The new symlink target is `~/.pi/agent/extensions/tcm`.

## 4. Environment variables — IF YOU SET ANY EXPLICITLY

In your shell rc or `.envrc` (direnv), flip:

| Old                       | New             |
| ------------------------- | --------------- |
| `OPENSESSIONS_DIR`        | `TCM_DIR`       |
| `OPENSESSIONS_HOST`       | `TCM_HOST`      |
| `OPENSESSIONS_PORT`       | `TCM_PORT`      |
| `OPENSESSIONS_WIDTH`      | `TCM_WIDTH`     |
| `OPENSESSIONS_RELOAD_TUI` | `TCM_RELOAD_TUI` |
| `OPENSESSIONS_FZF_COLORS` | `TCM_FZF_COLORS` |

None of these are commonly set; you'd know if you'd set one.

## 5. Stash session

The hidden-sidebar tmux session was renamed `_os_stash` → `_tcm_stash`.
Any sidebars currently stashed in `_os_stash` will be orphaned. Kill the
old session; new stashes go to the new name automatically:

```bash
tmux kill-session -t _os_stash 2>/dev/null || true
```

## 6. Stale `/tmp/` artifacts

These get recreated on next run; deleting them is optional but keeps `/tmp`
tidy:

```bash
rm -f /tmp/opensessions.pid /tmp/opensessions.version \
      /tmp/opensessions-debug.log /tmp/opensessions-err.log \
      /tmp/opensessions-install.log /tmp/opensessions-reattach \
      /tmp/opensessions-tui-agent-click.log \
      /tmp/opensessions-test-* /tmp/opensessions-test-save-* \
      /tmp/opensessions-test-theme-* /tmp/opensessions-plugin-test-*
```

New artifacts use `/tmp/tcm-*` and `/tmp/tcm.{pid,version}`.

## 7. Local clone directory — OPTIONAL

The repo on disk is still named `opensessions/`:

```bash
mv ~/Code/meta-claude/opensessions ~/Code/meta-claude/tail-claude-mux
```

If you do this **after** the tmux.conf and TPM changes are in place, nothing
breaks. Update any shell aliases, IDE workspaces, or shell-history shortcuts
that reference the old path.

## 8. GitHub-side cleanup

The old fork (`kylesnowschwartz/opensessions`) is still there. Recommended:

1. Edit its README to point at the new home.
2. Archive it (Settings → General → Archive this repository).

Don't delete — archiving keeps any external links, stars, and clones
discoverable while making the redirect intent clear.

## 9. Verification

After everything:

```bash
# tmux side
tmux show-options -g | grep -E "^@(tcm|opensessions|os)-"
# should show only @tcm-* lines, no opensessions/os entries

# config side
ls ~/.config/tcm/      # should have your config files
ls ~/.config/opensessions 2>/dev/null  # should not exist

# plugin side
ls ~/.tmux/plugins/tail-claude-mux/   # should be the new clone
ls ~/.tmux/plugins/opensessions 2>/dev/null  # should not exist (or you can rm -rf)

# server side
curl -s "http://localhost:${TCM_PORT:-3737}/health"   # should respond
```

## What NOT to update

- `.aos/journal/` entries are **historical** and intentionally still mention
  `opensessions` — they document what the names were when the journal was
  written.
- Past commits (everything before `10292b2`) reference `opensessions`. That's
  history; rebasing it would lose information. Use commits going forward for
  the new identity.
- `.cloned-sources/` is upstream code mirrored for reference; not ours to
  rename.
