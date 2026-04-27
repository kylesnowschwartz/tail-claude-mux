# tcm

A tmux sidebar I built for myself. **Personal tool — fork at your own risk.**

Shows the session list, agent state for Claude Code and pi, and the git branch for each session — in one small pane that lives inside your existing tmux workflow.


## Install

Requires `tmux`, `bun`, and [TPM](https://github.com/tmux-plugins/tpm).

```tmux
set -g @plugin 'kylesnowschwartz/tail-claude-mux'
```

Reload tmux, run `~/.tmux/plugins/tpm/bin/install_plugins`, then open the sidebar with `prefix o → s`.

TPM clones the repo into `~/.tmux/plugins/tail-claude-mux/`. There is no standalone binary — `tcm` runs from that checkout against your local `bun`.

## Update

`prefix + U` (TPM update). The plugin auto-restarts the server so it picks up new code.

## Uninstall

Run the cleanup script **before** removing the plugin or you'll leak tmux hooks, keybindings, and panes:

```bash
sh ~/.tmux/plugins/tail-claude-mux/integrations/tmux-plugin/scripts/uninstall.sh
```

Then remove the `set -g @plugin` line from `~/.tmux.conf` and run `prefix + alt + u`.

## Docs

- [Get started in tmux](./docs/tutorials/get-started-in-tmux.md)
- [Configuration](./docs/reference/configuration.md)
- [Keybindings](./docs/reference/features-and-keybindings.md)
- [Programmatic API](./docs/reference/programmatic-api.md) — push status / progress / logs to the sidebar over HTTP
- [Internal contracts](./CONTRACTS.md)

## License

MIT — see [LICENSE](./LICENSE).
