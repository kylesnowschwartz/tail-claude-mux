# opensessions tmux header — passive reader of @os-thm-* and @os-agent* options
# written by the opensessions server. See docs/specs/tmux-header.md.
#
# This file applies status-line variables only. The server is the single
# writer of theme tokens (@os-thm-*) and per-window agent state (@os-agent*).
# Sourced by opensessions.tmux when @opensessions-header == "on".

# Status-line position and justification.
set -g status-position top
set -g status-justify left

# Theme-aware base styles. Inactive tabs use the terminal's default fg so
# they remain readable across light and dark terminal palettes — relying on
# bold + accent fg for the active window to differentiate. The `?...,default`
# chain keeps things sane on first paint before the server writes options.
set -g status-style "fg=default,bg=#{?@os-thm-base,#{@os-thm-base},default}"
set -g window-status-style "fg=default"
set -g window-status-current-style "fg=#{?@os-thm-blue,#{@os-thm-blue},default},bold"

# Window-status-format: leading + trailing space for breathing room, optional
# agent glyph after the leading pad, then the directory-basename window name
# with zoom flag. Mirrors the user's existing oh-my-tmux window naming so we
# don't fight the Claude Code version-string suppression in .tmux.conf.local.
set -g window-status-format " #{?@os-agent,#[fg=#{@os-agent-fg}]#{@os-agent}#[default] ,}#I:#{=12:#{b:pane_current_path}}#{?window_zoomed_flag,Z,} "
set -g window-status-current-format " #{?@os-agent,#[fg=#{@os-agent-fg}]#{@os-agent}#[default] ,}#I:#{=12:#{b:pane_current_path}}#{?window_zoomed_flag,Z,} "
# Single-space separator between windows (paired with the trailing pad above
# this gives 2 cells of breathing room — visually close to a tab gutter).
set -g window-status-separator " "

# Status-left: session pill on accent. No trailing space outside the pill —
# the windows carry their own leading pad, so a space here would render with
# `bg=default` and create a stray cell when default differs from the bar bg.
set -g status-left "#[fg=#{?@os-thm-base,#{@os-thm-base},default},bg=#{?@os-thm-blue,#{@os-thm-blue},default},bold] #S #[default]"
set -g status-left-length 40

# Status-right: keep oh-my-tmux semantic content (prefix indicator, pairing,
# sync) but route through palette options. Pairing and sync only render when
# active.
set -g status-right " #{?client_prefix,#[fg=#{?@os-thm-base,#{@os-thm-base},default}]#[bg=#{?@os-thm-yellow,#{@os-thm-yellow},default}]#[bold] ^A #[default],} #{?session_many_attached,#[fg=#{?@os-thm-base,#{@os-thm-base},default}]#[bg=#{?@os-thm-yellow,#{@os-thm-yellow},default}]#[bold] 2+ #[default],} #{?pane_synchronized,#[fg=#{?@os-thm-base,#{@os-thm-base},default}]#[bg=#{?@os-thm-red,#{@os-thm-red},default}]#[bold] SY #[default],}"
set -g status-right-length 40
