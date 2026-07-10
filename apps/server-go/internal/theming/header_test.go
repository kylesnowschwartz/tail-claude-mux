package theming

import (
	"errors"
	"reflect"
	"testing"
	"unicode/utf8"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// Every glyph must be exactly one rune. Guards the \U escape-width trap:
// Go's \U takes exactly 8 hex digits, so a 6-digit codepoint padded wrong
// (\U000100CC0) silently becomes codepoint U+100CC + a literal '0' — two
// runes that render as garbage in the header tabs.
func TestBuildAgentGlyphs_SingleRuneEach(t *testing.T) {
	for _, clawd := range []bool{true, false} {
		for name, glyph := range BuildAgentGlyphs(clawd) {
			if n := utf8.RuneCountInString(glyph); n != 1 {
				t.Errorf("clawd=%v glyph %q = %q: %d runes, want 1", clawd, name, glyph, n)
			}
		}
	}
	if got := BuildAgentGlyphs(true)["claude-code"]; got != string(rune(0x100CC0)) {
		t.Errorf("clawd glyph = %U, want U+100CC0", []rune(got))
	}
}

func TestBuildAgentGlyphs(t *testing.T) {
	tests := []struct {
		name           string
		clawdInstalled bool
		wantClaude     string
	}{
		{"clawd installed uses mascot", true, "\U00100CC0"},
		{"no clawd falls back to star", false, "★"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAgentGlyphs(tt.clawdInstalled)
			if got["claude-code"] != tt.wantClaude {
				t.Errorf("claude-code glyph = %q, want %q", got["claude-code"], tt.wantClaude)
			}
			want := map[string]string{"pi": "π", "codex": "\U000F02D9", "amp": "♦", "generic": "\U000F167A"}
			for agent, glyph := range want {
				if got[agent] != glyph {
					t.Errorf("%s glyph = %q, want %q", agent, got[agent], glyph)
				}
			}
		})
	}
}

func TestPickAgentForWindow(t *testing.T) {
	tests := []struct {
		name   string
		agents []string
		want   string
	}{
		{"empty defaults to generic", nil, "generic"},
		{"single agent", []string{"pi"}, "pi"},
		{"priority: claude-code beats pi", []string{"pi", "claude-code"}, "claude-code"},
		{"priority: codex beats amp", []string{"amp", "codex"}, "codex"},
		{"unknown agent falls through to first", []string{"mystery", "another"}, "mystery"},
		{"known beats unknown regardless of order", []string{"mystery", "amp"}, "amp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PickAgentForWindow(tt.agents); got != tt.want {
				t.Errorf("PickAgentForWindow(%v) = %q, want %q", tt.agents, got, tt.want)
			}
		})
	}
}

func TestSeverityLabel(t *testing.T) {
	tests := []struct {
		status   string
		liveness string
		want     string
	}{
		{wire.StatusRunning, "", SeverityWorking},
		{wire.StatusRunning, wire.LivenessExited, SeverityWorking},
		{wire.StatusWaiting, "", SeverityWaiting},
		{wire.StatusError, wire.LivenessAlive, SeverityError},
		{wire.StatusDone, wire.LivenessAlive, SeverityReady},
		{wire.StatusDone, wire.LivenessExited, SeverityStopped},
		{wire.StatusInterrupted, "", SeverityStopped},
		{wire.StatusIdle, wire.LivenessAlive, SeverityReady},
		{wire.StatusIdle, wire.LivenessExited, SeverityReady},
		{"", "", SeverityReady},
		{"", wire.LivenessAlive, SeverityReady},
	}
	for _, tt := range tests {
		if got := SeverityLabel(tt.status, tt.liveness); got != tt.want {
			t.Errorf("SeverityLabel(%q, %q) = %q, want %q", tt.status, tt.liveness, got, tt.want)
		}
	}
}

func TestSeverityColour(t *testing.T) {
	theme := BuiltinTheme("catppuccin-mocha")
	tests := []struct {
		name  string
		agent wire.AgentEvent
		want  string
	}{
		{"working is blue", wire.AgentEvent{Status: wire.StatusRunning}, "#89b4fa"},
		{"waiting is yellow", wire.AgentEvent{Status: wire.StatusWaiting}, "#f9e2af"},
		{"ready is green", wire.AgentEvent{Status: wire.StatusIdle, Liveness: wire.LivenessAlive}, "#a6e3a1"},
		{"stopped is surface2", wire.AgentEvent{Status: wire.StatusDone, Liveness: wire.LivenessExited}, "#585b70"},
		{"error is red", wire.AgentEvent{Status: wire.StatusError}, "#f38ba8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SeverityColour(tt.agent, theme); got != tt.want {
				t.Errorf("SeverityColour = %q, want %q", got, tt.want)
			}
		})
	}
}

// alive builds an alive agent in a pane for planner inputs.
func alive(agent, status, paneID string) wire.AgentEvent {
	return wire.AgentEvent{Agent: agent, Status: status, PaneID: paneID, Liveness: wire.LivenessAlive}
}

func sessionsWith(agents ...wire.AgentEvent) []wire.SessionData {
	return []wire.SessionData{{Name: "s", Agents: agents}}
}

func TestPlanHeaderSync(t *testing.T) {
	theme := BuiltinTheme("catppuccin-mocha")
	glyphs := BuildAgentGlyphs(false)

	tests := []struct {
		name         string
		sessions     []wire.SessionData
		enabled      bool
		paneToWindow map[string]string
		prevWindows  map[string]WindowState
		wantCommands [][]string
		wantWindows  map[string]WindowState
	}{
		{
			name:         "disabled gate produces nothing and resets cache",
			sessions:     sessionsWith(alive("claude-code", wire.StatusRunning, "%1")),
			enabled:      false,
			paneToWindow: map[string]string{"%1": "@1"},
			prevWindows:  map[string]WindowState{"@9": {Glyph: "x"}},
			wantCommands: nil,
			wantWindows:  map[string]WindowState{},
		},
		{
			name:         "fresh window emits the option triple",
			sessions:     sessionsWith(alive("claude-code", wire.StatusRunning, "%1")),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1"},
			prevWindows:  map[string]WindowState{},
			wantCommands: [][]string{
				{"set-option", "-w", "-t", "@1", "@tcm-agent", "★"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-fg", "#89b4fa"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-type", "claude-code"},
			},
			wantWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
		},
		{
			name:         "unchanged window emits nothing",
			sessions:     sessionsWith(alive("claude-code", wire.StatusRunning, "%1")),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1"},
			prevWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
			wantCommands: nil,
			wantWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
		},
		{
			name:         "changed fg re-emits all three options",
			sessions:     sessionsWith(alive("claude-code", wire.StatusIdle, "%1")),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1"},
			prevWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
			wantCommands: [][]string{
				{"set-option", "-w", "-t", "@1", "@tcm-agent", "★"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-fg", "#a6e3a1"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-type", "claude-code"},
			},
			wantWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#a6e3a1", Agent: "claude-code"},
			},
		},
		{
			name:         "vacated live window is cleaned up",
			sessions:     nil,
			enabled:      true,
			paneToWindow: map[string]string{"%9": "@1"},
			prevWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
			wantCommands: [][]string{
				{"set-option", "-wu", "-t", "@1", "@tcm-agent"},
				{"set-option", "-wu", "-t", "@1", "@tcm-agent-fg"},
				{"set-option", "-wu", "-t", "@1", "@tcm-agent-type"},
			},
			wantWindows: map[string]WindowState{},
		},
		{
			// A dead window in the cache must be skipped — `set-option -wu`
			// against it errors and aborts the whole chained command.
			name:         "dead window is not cleaned up",
			sessions:     nil,
			enabled:      true,
			paneToWindow: map[string]string{"%9": "@2"},
			prevWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
			wantCommands: nil,
			wantWindows:  map[string]WindowState{},
		},
		{
			name: "dominant agent picked by priority",
			sessions: sessionsWith(
				alive("pi", wire.StatusRunning, "%1"),
				alive("claude-code", wire.StatusIdle, "%2"),
			),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1", "%2": "@1"},
			prevWindows:  map[string]WindowState{},
			wantCommands: [][]string{
				{"set-option", "-w", "-t", "@1", "@tcm-agent", "★"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-fg", "#a6e3a1"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-type", "claude-code"},
			},
			wantWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#a6e3a1", Agent: "claude-code"},
			},
		},
		{
			// Two entries of the dominant agent in one window: the higher
			// severity wins even when pushed later (idle must not hide a
			// running entry).
			name: "severity tie-break among same-name entries",
			sessions: sessionsWith(
				alive("claude-code", wire.StatusIdle, "%1"),
				alive("claude-code", wire.StatusRunning, "%2"),
			),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1", "%2": "@1"},
			prevWindows:  map[string]WindowState{},
			wantCommands: [][]string{
				{"set-option", "-w", "-t", "@1", "@tcm-agent", "★"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-fg", "#89b4fa"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-type", "claude-code"},
			},
			wantWindows: map[string]WindowState{
				"@1": {Glyph: "★", FG: "#89b4fa", Agent: "claude-code"},
			},
		},
		{
			name: "non-alive, pane-less, and unmapped agents are skipped",
			sessions: sessionsWith(
				wire.AgentEvent{Agent: "claude-code", Status: wire.StatusRunning, PaneID: "%1", Liveness: wire.LivenessExited},
				wire.AgentEvent{Agent: "pi", Status: wire.StatusRunning, Liveness: wire.LivenessAlive},
				alive("codex", wire.StatusRunning, "%404"),
			),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1"},
			prevWindows:  map[string]WindowState{},
			wantCommands: nil,
			wantWindows:  map[string]WindowState{},
		},
		{
			name:         "unknown agent renders the generic glyph",
			sessions:     sessionsWith(alive("mystery", wire.StatusRunning, "%1")),
			enabled:      true,
			paneToWindow: map[string]string{"%1": "@1"},
			prevWindows:  map[string]WindowState{},
			wantCommands: [][]string{
				{"set-option", "-w", "-t", "@1", "@tcm-agent", "\U000F167A"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-fg", "#89b4fa"},
				{"set-option", "-w", "-t", "@1", "@tcm-agent-type", "mystery"},
			},
			wantWindows: map[string]WindowState{
				"@1": {Glyph: "\U000F167A", FG: "#89b4fa", Agent: "mystery"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanHeaderSync(PlanInput{
				Sessions:     tt.sessions,
				Theme:        theme,
				Enabled:      tt.enabled,
				Glyphs:       glyphs,
				PaneToWindow: tt.paneToWindow,
				PrevWindows:  tt.prevWindows,
			})
			if !reflect.DeepEqual(got.Commands, tt.wantCommands) {
				t.Errorf("Commands = %v, want %v", got.Commands, tt.wantCommands)
			}
			if !reflect.DeepEqual(got.NewWindows, tt.wantWindows) {
				t.Errorf("NewWindows = %v, want %v", got.NewWindows, tt.wantWindows)
			}
		})
	}
}

// TestPlanHeaderSyncTransparentColour covers the toTmuxColour translation
// on the fg path: a palette value of "transparent" must reach tmux as
// "default".
func TestPlanHeaderSyncTransparentColour(t *testing.T) {
	theme := BuiltinTheme("catppuccin-mocha")
	theme.Palette.Green = "transparent"
	got := PlanHeaderSync(PlanInput{
		Sessions:     sessionsWith(alive("pi", wire.StatusIdle, "%1")),
		Theme:        theme,
		Enabled:      true,
		Glyphs:       BuildAgentGlyphs(false),
		PaneToWindow: map[string]string{"%1": "@1"},
		PrevWindows:  map[string]WindowState{},
	})
	if got.NewWindows["@1"].FG != "default" {
		t.Errorf("FG = %q, want %q", got.NewWindows["@1"].FG, "default")
	}
}

func TestReadHeaderEnabled(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"on", "on", nil, true},
		{"on with whitespace", " on\n", nil, true},
		{"off", "off", nil, false},
		{"unset", "", nil, false},
		{"tmux error", "", errors.New("no server"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm, calls := fakeTmux(func([]string) (string, error) { return tt.out, tt.err })
			if got := ReadHeaderEnabled(tm); got != tt.want {
				t.Errorf("ReadHeaderEnabled = %v, want %v", got, tt.want)
			}
			wantCall := []string{"show-option", "-gqv", "@tcm-header"}
			if len(*calls) != 1 || !equalArgs((*calls)[0], wantCall) {
				t.Errorf("tmux call = %v, want %v", *calls, wantCall)
			}
		})
	}
}

// headerHarness drives HeaderSync against a scriptable tmux fake.
type headerHarness struct {
	sync      *HeaderSync
	calls     *[][]string
	panesOut  string
	panesErr  error
	writeErr  error
	logged    []string
	theme     Theme
	listCalls int
}

func newHeaderHarness(t *testing.T) *headerHarness {
	t.Helper()
	h := &headerHarness{theme: BuiltinTheme("catppuccin-mocha")}
	tm, calls := fakeTmux(func(args []string) (string, error) {
		if args[0] == "list-panes" {
			h.listCalls++
			return h.panesOut, h.panesErr
		}
		return "", h.writeErr
	})
	h.calls = calls
	h.sync = NewHeaderSync(tm, false, func(msg string, _ map[string]any) {
		h.logged = append(h.logged, msg)
	})
	return h
}

func (h *headerHarness) run(sessions []wire.SessionData) {
	h.sync.Sync(sessions, h.theme, true)
}

// writeCalls returns the recorded non-list-panes invocations.
func (h *headerHarness) writeCalls() [][]string {
	var out [][]string
	for _, c := range *h.calls {
		if c[0] != "list-panes" {
			out = append(out, c)
		}
	}
	return out
}

func TestHeaderSyncEmitsChainedCommands(t *testing.T) {
	h := newHeaderHarness(t)
	h.panesOut = "%1|@1\n%2|@2"
	h.run(sessionsWith(alive("claude-code", wire.StatusRunning, "%1")))

	writes := h.writeCalls()
	if len(writes) != 1 {
		t.Fatalf("write invocations = %d, want 1 chained call", len(writes))
	}
	want := []string{
		"set-option", "-w", "-t", "@1", "@tcm-agent", "★", ";",
		"set-option", "-w", "-t", "@1", "@tcm-agent-fg", "#89b4fa", ";",
		"set-option", "-w", "-t", "@1", "@tcm-agent-type", "claude-code",
	}
	if !equalArgs(writes[0], want) {
		t.Errorf("chained call = %v, want %v", writes[0], want)
	}

	// Second sync with identical state: diff cache suppresses all writes.
	h.run(sessionsWith(alive("claude-code", wire.StatusRunning, "%1")))
	if len(h.writeCalls()) != 1 {
		t.Errorf("idempotent re-sync issued extra writes: %v", h.writeCalls())
	}
}

func TestHeaderSyncDisabledResetsCache(t *testing.T) {
	h := newHeaderHarness(t)
	h.panesOut = "%1|@1"
	sessions := sessionsWith(alive("claude-code", wire.StatusRunning, "%1"))
	h.run(sessions)
	if len(h.writeCalls()) != 1 {
		t.Fatalf("expected initial emit")
	}

	// Disabled: no tmux traffic at all, cache reset.
	h.sync.Sync(sessions, h.theme, false)
	if h.listCalls != 1 {
		t.Errorf("disabled sync still listed panes")
	}

	// Re-enable: full re-emit against the reset cache.
	h.run(sessions)
	if len(h.writeCalls()) != 2 {
		t.Errorf("re-enable did not re-emit: %v", h.writeCalls())
	}
}

func TestHeaderSyncReadFailurePreservesCache(t *testing.T) {
	h := newHeaderHarness(t)
	h.panesOut = "%1|@1"
	sessions := sessionsWith(alive("claude-code", wire.StatusRunning, "%1"))
	h.run(sessions)

	// list-panes flake: no writes, cache preserved.
	h.panesErr = errors.New("flake")
	h.run(sessions)
	h.panesErr = nil

	// Recovery scan sees unchanged state → no redundant re-emit.
	h.run(sessions)
	if len(h.writeCalls()) != 1 {
		t.Errorf("read failure did not preserve cache: %v", h.writeCalls())
	}
	if !containsString(h.logged, "sync read failed") {
		t.Errorf("read failure not logged; logged = %v", h.logged)
	}
}

func TestHeaderSyncEmptyPaneListPreservesCache(t *testing.T) {
	h := newHeaderHarness(t)
	h.panesOut = "%1|@1"
	sessions := sessionsWith(alive("claude-code", wire.StatusRunning, "%1"))
	h.run(sessions)

	h.panesOut = "" // ambiguous: flake vs genuinely empty server
	h.run(sessions)
	h.panesOut = "%1|@1"
	h.run(sessions)
	if len(h.writeCalls()) != 1 {
		t.Errorf("empty pane list did not preserve cache: %v", h.writeCalls())
	}
}

func TestHeaderSyncWriteFailureResetsCache(t *testing.T) {
	h := newHeaderHarness(t)
	h.panesOut = "%1|@1"
	sessions := sessionsWith(alive("claude-code", wire.StatusRunning, "%1"))

	// First sync fails mid-chain: cache must reset so the next broadcast
	// re-emits the full per-window state.
	h.writeErr = errors.New("aborted chain")
	h.run(sessions)
	h.writeErr = nil
	h.run(sessions)

	writes := h.writeCalls()
	if len(writes) != 2 {
		t.Fatalf("write invocations = %d, want failed + self-heal", len(writes))
	}
	if !equalArgs(writes[0], writes[1]) {
		t.Errorf("self-heal did not re-emit the full state: %v vs %v", writes[0], writes[1])
	}
	if !containsString(h.logged, "sync write failed") {
		t.Errorf("write failure not logged; logged = %v", h.logged)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
