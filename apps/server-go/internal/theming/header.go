package theming

// Tmux header synchroniser — the Go port of
// packages/runtime/src/server/tmux-header-sync.ts.
//
// Single-writer translation from tcm state -> tmux user options that the
// status line in integrations/tmux-plugin/scripts/header.tmux reads to
// render per-window agent glyphs and theme-aware colours. Palette and
// statusline glyphs are written declaratively by PaletteWriter (see
// palette.go) — this hot path only emits per-window agent state.
//
// Spec: docs/specs/tmux-header.md

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// --- Glyph table ---

// claude-code's glyph is detect-and-fall-back: if Clawd.ttf is installed at
// the OS-standard user-fonts path, emit U+100CC0 (Plane 16 PUA-B, the Clawd
// mascot); otherwise fall back to U+2605 (★). Run `just install-clawd` to
// install the vendored font. The remaining glyphs are drawn from
// widely-supported Unicode blocks (U+25xx, U+26xx, basic Greek).

// ClawdFontPath returns the OS-standard user-fonts path for Clawd.ttf.
// homeDir is injected (no os.UserHomeDir inside library logic).
func ClawdFontPath(homeDir string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(homeDir, "Library", "Fonts", "Clawd.ttf")
	}
	return filepath.Join(homeDir, ".local", "share", "fonts", "Clawd.ttf")
}

// IsClawdInstalled probes whether the Clawd mascot font is installed. Cheap
// (one stat call) — call once at server boot and pass the result to
// NewHeaderSync; a restart picks up post-hoc installs, same as the TS
// module-load probe.
func IsClawdInstalled(homeDir string) bool {
	_, err := os.Stat(ClawdFontPath(homeDir))
	return err == nil
}

// BuildAgentGlyphs returns the per-agent glyph table
// (tmux-header-sync.ts buildAgentGlyphs).
func BuildAgentGlyphs(clawdInstalled bool) map[string]string {
	claude := "★"
	if clawdInstalled {
		// U+100CC0 (Clawd.ttf PUA-B, TS "\u{100CC0}"). Go's \U escape is
		// exactly 8 hex digits — \U000100CC0 parses as U+100CC plus a
		// literal '0', which is how a mangled header-tab glyph shipped.
		claude = "\U00100CC0"
	}
	return map[string]string{
		"claude-code": claude,
		"pi":          "π",
		"codex":       "▲",
		"amp":         "♦",
		"generic":     "\U000F167A",
	}
}

// AgentPriority orders agents for the per-window dominant pick
// (tmux-header-sync.ts AGENT_PRIORITY).
var AgentPriority = []string{"claude-code", "pi", "codex", "amp"}

// Statusline-only glyphs: constants the statusline format references in
// fixed slots, emitted by the palette file as `set -gq @tcm-<name>-glyph`
// so header.tmux resolves them with `#{@tcm-<name>-glyph}`.
const (
	// StatuslineLastWindow is nf-md-arrow_u_left_top — the
	// last-visited-window marker.
	StatuslineLastWindow = "\U000F17B3"
	// StatuslineShell is nf-cod-terminal — the no-agent-in-window marker.
	StatuslineShell = "\uea85"
)

// PickAgentForWindow picks the dominant agent name for a window
// (tmux-header-sync.ts pickAgentForWindow).
func PickAgentForWindow(agents []string) string {
	for _, candidate := range AgentPriority {
		for _, a := range agents {
			if a == candidate {
				return candidate
			}
		}
	}
	if len(agents) > 0 {
		return agents[0]
	}
	return "generic"
}

// --- Pure planner ---

// WindowState is the per-window option triple the sync diffs against.
type WindowState struct {
	Glyph string
	FG    string
	Agent string
}

// PlanInput feeds PlanHeaderSync. Glyphs is the BuildAgentGlyphs table
// (explicit so the planner stays pure).
type PlanInput struct {
	Sessions     []wire.SessionData
	Theme        Theme
	Enabled      bool
	Glyphs       map[string]string
	PaneToWindow map[string]string
	PrevWindows  map[string]WindowState
}

// PlanOutput is the minimal command set plus the new diff cache.
type PlanOutput struct {
	Commands   [][]string
	NewWindows map[string]WindowState
}

// PlanHeaderSync computes the desired per-window agent state from server
// state, diffs against the previous values, and emits the minimal set of
// tmux invocations needed to reach the new state (tmux-header-sync.ts
// planTmuxHeaderSync). Palette + statusline glyphs are emitted by
// PaletteWriter at boot / theme change — not from this hot path.
func PlanHeaderSync(in PlanInput) PlanOutput {
	if !in.Enabled {
		// Gate off: produce no commands. Empty NewWindows resets the diff
		// cache so a later enable does a full re-emit.
		return PlanOutput{NewWindows: map[string]WindowState{}}
	}

	order, newWindows := computeWindowStates(in)
	var commands [][]string

	// Live windows: every tmux window has at least one pane, so the
	// value-set of PaneToWindow is the set of windows currently alive in
	// the server. Used below to skip cleanup writes for windows that have
	// already been closed — those would error with "no such window: @N",
	// aborting the chained tmux call and preventing the diff cache from
	// advancing. One stuck dead-window id used to wedge the sync forever.
	liveWindows := map[string]bool{}
	for _, windowID := range in.PaneToWindow {
		liveWindows[windowID] = true
	}

	// Per-window diffs, in first-seen session/agent order (the TS Map's
	// insertion order).
	for _, windowID := range order {
		next := newWindows[windowID]
		if prev, ok := in.PrevWindows[windowID]; ok && prev == next {
			continue
		}
		commands = append(commands,
			[]string{"set-option", "-w", "-t", windowID, "@tcm-agent", next.Glyph},
			[]string{"set-option", "-w", "-t", windowID, "@tcm-agent-fg", next.FG},
			[]string{"set-option", "-w", "-t", windowID, "@tcm-agent-type", next.Agent},
		)
	}

	// Cleanup: windows that had a glyph but no longer do, skipping windows
	// that are no longer alive in tmux (see liveWindows above). Sorted for
	// determinism — Go maps have no insertion order; the command set is
	// identical to the TS emission, only inter-window order may differ.
	var stale []string
	for windowID := range in.PrevWindows {
		if _, ok := newWindows[windowID]; ok {
			continue
		}
		if !liveWindows[windowID] {
			continue
		}
		stale = append(stale, windowID)
	}
	sort.Strings(stale)
	for _, windowID := range stale {
		commands = append(commands,
			[]string{"set-option", "-wu", "-t", windowID, "@tcm-agent"},
			[]string{"set-option", "-wu", "-t", windowID, "@tcm-agent-fg"},
			[]string{"set-option", "-wu", "-t", windowID, "@tcm-agent-type"},
		)
	}

	return PlanOutput{Commands: commands, NewWindows: newWindows}
}

// computeWindowStates groups alive agents by tmux window and resolves each
// window's dominant glyph/colour. Returns the first-seen window order
// alongside the map so command emission is deterministic.
func computeWindowStates(in PlanInput) ([]string, map[string]WindowState) {
	var order []string
	windowAgents := map[string][]wire.AgentEvent{}
	for _, session := range in.Sessions {
		for _, agent := range session.Agents {
			if agent.Liveness != wire.LivenessAlive {
				continue
			}
			if agent.PaneID == "" {
				continue
			}
			windowID, ok := in.PaneToWindow[agent.PaneID]
			if !ok {
				continue
			}
			if _, seen := windowAgents[windowID]; !seen {
				order = append(order, windowID)
			}
			windowAgents[windowID] = append(windowAgents[windowID], agent)
		}
	}

	result := map[string]WindowState{}
	for _, windowID := range order {
		agents := windowAgents[windowID]
		names := make([]string, len(agents))
		for i, a := range agents {
			names[i] = a.Agent
		}
		dominantName := PickAgentForWindow(names)
		// Among entries sharing the dominant agent name, pick the one with
		// the highest severity priority — a running entry must not hide
		// behind an idle one pushed earlier in the same window.
		dominant := agents[0]
		found := false
		for _, a := range agents {
			if a.Agent != dominantName {
				continue
			}
			if !found || severityRank(a) > severityRank(dominant) {
				dominant = a
				found = true
			}
		}
		glyph, ok := in.Glyphs[dominantName]
		if !ok {
			glyph = in.Glyphs["generic"]
		}
		fg := toTmuxColour(SeverityColour(dominant, in.Theme))
		result[windowID] = WindowState{Glyph: glyph, FG: fg, Agent: dominantName}
	}
	return order, result
}

// --- Severity colour resolution ---
//
// Mirrors the TUI panel's status→colour map (apps/tui/src/index.tsx), same
// as the TS header did. When/if these diverge, extract a shared resolver.

// Severity labels — the 5-state surface the header consumes.
const (
	SeverityWorking = "working"
	SeverityWaiting = "waiting"
	SeverityReady   = "ready"
	SeverityStopped = "stopped"
	SeverityError   = "error"
)

// severityRankByLabel ranks severities for the header tie-break; higher
// wins (tmux-header-sync.ts SEVERITY_RANK).
var severityRankByLabel = map[string]int{
	SeverityError:   5,
	SeverityWorking: 4,
	SeverityWaiting: 3,
	SeverityReady:   2,
	SeverityStopped: 1,
}

func severityRank(agent wire.AgentEvent) int {
	return severityRankByLabel[SeverityLabel(agent.Status, agent.Liveness)]
}

// SeverityLabel derives the five-state severity from agent status +
// liveness (tmux-header-sync.ts severityLabel). An empty status plays the
// TS null role.
func SeverityLabel(status, liveness string) string {
	switch status {
	case wire.StatusRunning:
		return SeverityWorking
	case wire.StatusWaiting:
		return SeverityWaiting
	case wire.StatusError:
		return SeverityError
	}
	// done / interrupted / idle / "" — liveness disambiguates.
	if liveness == wire.LivenessAlive {
		return SeverityReady
	}
	if status == wire.StatusDone || status == wire.StatusInterrupted {
		return SeverityStopped
	}
	return SeverityReady
}

// SeverityColour resolves the tmux fg colour for an agent's severity
// (tmux-header-sync.ts severityColour).
func SeverityColour(agent wire.AgentEvent, theme Theme) string {
	switch SeverityLabel(agent.Status, agent.Liveness) {
	case SeverityWorking:
		return theme.Palette.Blue
	case SeverityWaiting:
		return theme.Palette.Yellow
	case SeverityStopped:
		return theme.Palette.Surface2
	case SeverityError:
		return theme.Palette.Red
	default: // SeverityReady
		return theme.Palette.Green
	}
}

// --- Live sync ---

// ReadHeaderEnabled reads the @tcm-header gate (index.ts headerEnabled).
// The TS server reads it once at boot — toggling at runtime requires a
// server restart, same cost shape as other @tcm-* options read at TPM init.
func ReadHeaderEnabled(t *tmux.Tmux) bool {
	out, err := t.Run("show-option", "-gqv", "@tcm-header")
	return err == nil && strings.TrimSpace(out) == "on"
}

// HeaderSync owns the per-window diff cache across broadcasts
// (tmux-header-sync.ts's module-level lastWindows, made instance state).
type HeaderSync struct {
	tmux        *tmux.Tmux
	glyphs      map[string]string
	log         Logger
	lastWindows map[string]WindowState
}

// NewHeaderSync returns a sync issuing tmux commands through t.
// clawdInstalled selects the claude-code glyph (see IsClawdInstalled).
func NewHeaderSync(t *tmux.Tmux, clawdInstalled bool, log Logger) *HeaderSync {
	return &HeaderSync{
		tmux:        t,
		glyphs:      BuildAgentGlyphs(clawdInstalled),
		log:         log,
		lastWindows: map[string]WindowState{},
	}
}

// Sync ports syncTmuxHeaderOptions: call it from the state broadcast path.
// Idempotent and non-throwing — failures are logged and the diff cache is
// preserved or reset so the next broadcast self-heals.
func (s *HeaderSync) Sync(sessions []wire.SessionData, theme Theme, enabled bool) {
	if !enabled {
		s.lastWindows = map[string]WindowState{}
		return
	}

	paneToWindow, err := s.readPaneToWindow()
	if err != nil {
		// Read-side failure (list-panes flake): preserve cache. The next
		// successful scan recomputes against the correct prior state;
		// clearing here would leak stale per-window options when the
		// recovery scan re-emitted writes against an empty cache.
		s.logf("sync read failed", map[string]any{"error": err.Error()})
		return
	}
	if len(paneToWindow) == 0 {
		// Empty list-panes is ambiguous (transient flake vs genuinely
		// empty server). Same reasoning as above — preserve cache.
		return
	}

	plan := PlanHeaderSync(PlanInput{
		Sessions:     sessions,
		Theme:        theme,
		Enabled:      enabled,
		Glyphs:       s.glyphs,
		PaneToWindow: paneToWindow,
		PrevWindows:  s.lastWindows,
	})

	if len(plan.Commands) > 0 {
		if err := s.runTmuxCommands(plan.Commands); err != nil {
			// Write-side failure: the chained tmux command aborted at an
			// unknown point. Reset cache so the next broadcast re-emits the
			// full per-window state and self-heals.
			s.logf("sync write failed", map[string]any{"error": err.Error()})
			s.lastWindows = map[string]WindowState{}
			return
		}
	}

	s.lastWindows = plan.NewWindows
}

func (s *HeaderSync) readPaneToWindow() (map[string]string, error) {
	out, err := s.tmux.Run("list-panes", "-a", "-F", "#{pane_id}|#{window_id}")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if out == "" {
		return m, nil
	}
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		idx := strings.Index(line, "|")
		if idx <= 0 {
			continue
		}
		m[line[:idx]] = line[idx+1:]
	}
	return m, nil
}

// runTmuxCommands chains all commands into a single tmux invocation via `;`
// separators to amortise the process-spawn cost; execing without a shell,
// `;` is passed as its own argument.
func (s *HeaderSync) runTmuxCommands(commands [][]string) error {
	var chained []string
	for i, cmd := range commands {
		if i > 0 {
			chained = append(chained, ";")
		}
		chained = append(chained, cmd...)
	}
	_, err := s.tmux.Run(chained...)
	return err
}

func (s *HeaderSync) logf(msg string, data map[string]any) {
	if s.log != nil {
		s.log(msg, data)
	}
}
