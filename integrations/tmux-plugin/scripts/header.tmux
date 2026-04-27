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
# they remain readable across light and dark terminal palettes. The active
# tab carries a subtle bg pill (surface0) plus bold + accent fg, so it
# differentiates by both colour and weight — even when the active agent's
# severity colour also resolves to theme.blue (working state). The
# `?...,default` chain keeps things sane on first paint before the server
# writes options.
set -g status-style "fg=default,bg=#{?@os-thm-base,#{@os-thm-base},default}"
set -g window-status-style "fg=default"
set -g window-status-current-style "fg=#{?@os-thm-blue,#{@os-thm-blue},default},bg=#{?@os-thm-surface0,#{@os-thm-surface0},default},bold"

# Clear legacy oh-my-tmux / tmux-default tab indicators that duplicate our
# vocabulary. opensessions surfaces the same information through:
#   • activity zone (panel §7)         vs window-status-activity-style underscore
#   • severity glyph colour (Stage 5)   vs window-status-bell-style blink+bold
#   • yellow last-window arrow (§6.2)   vs window-status-last-style cyan fg
# Without these resets the legacy underscore (etc.) renders alongside our own
# indicators — visually noisy, and the underscore in particular reads as a
# "janky interrupted underline" because it's clipped by the active-tab pill.
set -g window-status-activity-style "default"
set -g window-status-bell-style "default"
set -g window-status-last-style "default"

# Window-status-format: leading + trailing space for breathing room, then a
# single "what's here" glyph slot followed by index + window-name + zoom +
# last-window-flag indicator.
#
# Glyph slot rules (single source of truth, mirrors panel left gutter):
#   • An agent is alive in this window  →  the agent's identity glyph in
#     its severity colour (Stage 5 vocabulary).
#   • No agent in this window           →  the boxed-terminal glyph
#     (nf-cod-terminal, U+EA85) in `theme.overlay0` — "this is just a
#     shell, nothing demanding attention."
#
# `#W` is tmux's window name; oh-my-tmux sets it to the basename of the pane's
# pwd by default but respects manual renames. The previous `#I:#{=12:#{b:...}}`
# scheme bypassed renames; tokyo-night-tmux popularised `#I #W` and the rename
# behaviour is what users expect.
#
# Last-window indicator: yellow `nf-md` U+F054C (undo curl-back) marks the most-recently-visited
# window so prefix-l navigation has a glance target. Width grows by 2 cells
# only on the marked tab; harmless given exactly one window carries the flag.
set -g window-status-format " #{?@os-agent,#[fg=#{@os-agent-fg}]#{@os-agent},#[fg=#{?@os-thm-overlay0,#{@os-thm-overlay0},default}]}#[default] #I #W#{?window_zoomed_flag, Z,}#{?window_last_flag,#[fg=#{?@os-thm-yellow,#{@os-thm-yellow},default}] 󰕌,} "
set -g window-status-current-format " #{?@os-agent,#[fg=#{@os-agent-fg}]#{@os-agent},}#[default,fg=#{?@os-thm-blue,#{@os-thm-blue},default},bg=#{?@os-thm-surface0,#{@os-thm-surface0},default},bold] #I #W#{?window_zoomed_flag, Z,}#{?window_last_flag,#[fg=#{?@os-thm-yellow,#{@os-thm-yellow},default}] 󰕌,} "
# Single-space separator between windows (paired with the trailing pad above
# this gives 2 cells of breathing room — visually close to a tab gutter).

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
