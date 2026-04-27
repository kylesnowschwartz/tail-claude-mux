# tcm tmux header — passive reader of @tcm-thm-* and @tcm-agent* options
# written by the tcm server. See docs/specs/tmux-header.md.
#
# This file applies status-line variables only. The server is the single
# writer of theme tokens (@tcm-thm-*) and per-window agent state (@tcm-agent*).
# Sourced by tcm.tmux when @tcm-header == "on".

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
set -g status-style "fg=default,bg=#{?@tcm-thm-base,#{@tcm-thm-base},default}"
set -g window-status-style "fg=default"
set -g window-status-current-style "fg=#{?@tcm-thm-blue,#{@tcm-thm-blue},default},bg=#{?@tcm-thm-surface0,#{@tcm-thm-surface0},default},bold"

# Clear legacy oh-my-tmux / tmux-default tab indicators that duplicate our
# vocabulary. tcm surfaces the same information through:
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
# Glyph slot rules (single source of truth: vocab.ts re-exports from
# `@tcm/runtime`; the runtime emits per-window `@tcm-agent` and server-global
# `@tcm-shell-glyph` / `@tcm-last-window-glyph` options that this format reads):
#   • An agent is alive in this window  →  the agent's identity glyph in
#     its severity colour (Stage 5 vocabulary).
#   • No agent in this window           →  @tcm-shell-glyph (nf-cod-terminal)
#     in `theme.overlay0` — "this is just a shell, nothing demanding attention."
#
# `#W` is tmux's window name; oh-my-tmux sets it to the basename of the pane's
# pwd by default but respects manual renames. The previous `#I:#{=12:#{b:...}}`
# scheme bypassed renames; tokyo-night-tmux popularised `#I #W` and the rename
# behaviour is what users expect.
#
# Last-window indicator: yellow @tcm-last-window-glyph (nf-md-arrow_u_left_top)
# marks the most-recently-visited window so prefix-l navigation has a glance
# target. Width grows by 2 cells only on the marked tab; harmless given
# exactly one window carries the flag.
set -g window-status-format " #{?@tcm-agent,#[fg=#{@tcm-agent-fg}]#{@tcm-agent},#[fg=#{?@tcm-thm-overlay0,#{@tcm-thm-overlay0},default}]#{?@tcm-shell-glyph,#{@tcm-shell-glyph},}}#[default] #I #W#{?window_zoomed_flag, Z,}#{?window_last_flag,#[fg=#{?@tcm-thm-yellow,#{@tcm-thm-yellow},default}] #{?@tcm-last-window-glyph,#{@tcm-last-window-glyph},},} "
set -g window-status-current-format " #{?@tcm-agent,#[fg=#{@tcm-agent-fg}]#{@tcm-agent},#[fg=#{?@tcm-thm-overlay0,#{@tcm-thm-overlay0},default}]#{?@tcm-shell-glyph,#{@tcm-shell-glyph},}}#[default,fg=#{?@tcm-thm-blue,#{@tcm-thm-blue},default},bg=#{?@tcm-thm-surface0,#{@tcm-thm-surface0},default},bold] #I #W#{?window_zoomed_flag, Z,}#{?window_last_flag,#[fg=#{?@tcm-thm-yellow,#{@tcm-thm-yellow},default}] #{?@tcm-last-window-glyph,#{@tcm-last-window-glyph},},} "
# Single-space separator between windows (paired with the trailing pad above
# this gives 2 cells of breathing room — visually close to a tab gutter).

# Status-left: session pill on accent. No trailing space outside the pill —
# the windows carry their own leading pad, so a space here would render with
# `bg=default` and create a stray cell when default differs from the bar bg.
set -g status-left "#[fg=#{?@tcm-thm-base,#{@tcm-thm-base},default},bg=#{?@tcm-thm-blue,#{@tcm-thm-blue},default},bold] #S #[default]"
set -g status-left-length 40

# Status-right: keep oh-my-tmux semantic content (prefix indicator, pairing,
# sync) but route through palette options. Pairing and sync only render when
# active.
set -g status-right " #{?client_prefix,#[fg=#{?@tcm-thm-base,#{@tcm-thm-base},default}]#[bg=#{?@tcm-thm-yellow,#{@tcm-thm-yellow},default}]#[bold] ^A #[default],} #{?session_many_attached,#[fg=#{?@tcm-thm-base,#{@tcm-thm-base},default}]#[bg=#{?@tcm-thm-yellow,#{@tcm-thm-yellow},default}]#[bold] 2+ #[default],} #{?pane_synchronized,#[fg=#{?@tcm-thm-base,#{@tcm-thm-base},default}]#[bg=#{?@tcm-thm-red,#{@tcm-thm-red},default}]#[bold] SY #[default],}"
set -g status-right-length 40
