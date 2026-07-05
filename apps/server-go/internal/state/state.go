// Package state assembles wire.ServerState from tmux + git + config —
// the computeState() port from packages/runtime/src/server/index.ts.
//
// Skeleton scope (stage 3): agentState is null and agents/eventTimestamps
// are empty — the tracker arrives with the watcher stages. Everything the
// TUI renders for the session list (name, git, panes, windows, uptime,
// ordering, theme, focus) is real.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// AgentSource supplies the tracker-derived per-session fields. The
// tracker satisfies it; nil means "no agents" (stage-3 skeleton behavior).
type AgentSource interface {
	GetState(session string) *wire.AgentEvent
	GetAgents(session string) []wire.AgentEvent
	GetEventTimestamps(session string) []int64
	IsUnseen(session string) bool
}

// MetadataSource supplies per-session presentation metadata (the ln zone's
// activity log + programmatic status/progress). nil Get results stay nil —
// the wire carries metadata: null for quiet sessions.
type MetadataSource interface {
	Get(session string) *wire.SessionMetadata
}

// Builder holds the pieces computeState needs. Fields are set once at
// startup; Build is called from the server's single command/refresh loop.
type Builder struct {
	Tmux     *tmux.Tmux
	Git      *gitinfo.Cache
	Order    *sessionorder.Order
	Agents   AgentSource
	Metadata MetadataSource

	// ConfigDir is ~/.config/tcm — the home of config.json and
	// active-theme.json.
	ConfigDir string

	// SidebarWidth mirrors the bun server's configuredWidth (loaded from
	// config.json; report-width persistence arrives in a later stage).
	SidebarWidth int

	// focusedSession persists across builds the way the bun server keeps
	// it module-level: reset only when the session disappears.
	focusedSession string
}

// Focused returns the current focused session name ("" = none).
func (b *Builder) Focused() string { return b.focusedSession }

// SetFocused overrides the focused session (switch-session optimistic
// update, focus-session command).
func (b *Builder) SetFocused(name string) { b.focusedSession = name }

// Build computes the full ServerState broadcast.
func (b *Builder) Build() wire.ServerState {
	muxSessions := b.Tmux.ListSessions()
	sort.SliceStable(muxSessions, func(i, j int) bool {
		if muxSessions[i].CreatedAt != muxSessions[j].CreatedAt {
			return muxSessions[i].CreatedAt < muxSessions[j].CreatedAt
		}
		return muxSessions[i].Name < muxSessions[j].Name
	})

	currentSession, hasCurrent := b.Tmux.CurrentSession("")

	names := make([]string, len(muxSessions))
	byName := map[string]tmux.Session{}
	for i, s := range muxSessions {
		names[i] = s.Name
		byName[s.Name] = s
	}
	b.Order.Sync(names)
	if hasCurrent {
		b.Order.Show(currentSession)
	}
	ordered := b.Order.Apply(names)

	panes := b.Tmux.ListAllPanes()
	activeDirs := tmux.ActiveDirs(panes)
	paneCounts := tmux.PaneCounts(panes)
	now := time.Now()

	sessions := make([]wire.SessionData, 0, len(ordered))
	for _, name := range ordered {
		s := byName[name]
		dir := s.Dir
		if d, ok := activeDirs[name]; ok {
			dir = d
		}
		git := b.Git.Get(dir)
		sd := wire.SessionData{
			Name:       name,
			CreatedAt:  s.CreatedAt,
			Dir:        dir,
			Branch:     git.Branch,
			Dirty:      git.Dirty,
			IsWorktree: git.IsWorktree,
			Panes:      paneCounts[name],
			Windows:    s.Windows,
			Uptime:     formatUptime(now.Unix() - s.CreatedAt),
			// Non-nil slices matter: the TUI iterates these, and null
			// is not [].
			Agents:          []wire.AgentEvent{},
			EventTimestamps: []int64{},
		}
		if b.Agents != nil {
			sd.Unseen = b.Agents.IsUnseen(name)
			sd.AgentState = b.Agents.GetState(name)
			sd.Agents = b.Agents.GetAgents(name)
			sd.EventTimestamps = b.Agents.GetEventTimestamps(name)
		}
		if b.Metadata != nil {
			sd.Metadata = b.Metadata.Get(name)
		}
		sessions = append(sessions, sd)
	}

	// Focus persistence, ported: none when there are no sessions; keep the
	// existing focus while it exists; otherwise prefer the current session,
	// then the first.
	switch {
	case len(sessions) == 0:
		b.focusedSession = ""
	case b.focusedSession == "" || !containsSession(sessions, b.focusedSession):
		b.focusedSession = sessions[0].Name
		if hasCurrent && containsSession(sessions, currentSession) {
			b.focusedSession = currentSession
		}
	}

	st := wire.ServerState{
		Type:         wire.TypeState,
		Sessions:     sessions,
		SidebarWidth: b.SidebarWidth,
		Theme:        b.themeConfig(),
		TS:           now.UnixMilli(),
	}
	if b.focusedSession != "" {
		st.FocusedSession = &b.focusedSession
	}
	if hasCurrent {
		st.CurrentSession = &currentSession
	}
	return st
}

// themeConfig ports effectiveThemeConfig: an external palette written by
// the-themer (~/.config/tcm/active-theme.json) takes precedence over the
// config.json theme (builtin name or inline palette). Both pass through
// opaquely — the client's resolveTheme handles either shape.
func (b *Builder) themeConfig() json.RawMessage {
	if raw, err := os.ReadFile(filepath.Join(b.ConfigDir, "active-theme.json")); err == nil {
		if isJSONObject(raw) {
			return json.RawMessage(raw)
		}
	}
	var cfg struct {
		Theme json.RawMessage `json:"theme"`
	}
	if raw, err := os.ReadFile(filepath.Join(b.ConfigDir, "config.json")); err == nil {
		if json.Unmarshal(raw, &cfg) == nil && len(cfg.Theme) > 0 {
			return cfg.Theme
		}
	}
	return nil
}

// LoadSidebarWidth reads sidebarWidth from config.json, defaulting to the
// bun server's DEFAULT_SIDEBAR_WIDTH.
func LoadSidebarWidth(configDir string) int {
	const defaultWidth = 33
	raw, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		return defaultWidth
	}
	var cfg struct {
		SidebarWidth int `json:"sidebarWidth"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.SidebarWidth <= 0 {
		return defaultWidth
	}
	return cfg.SidebarWidth
}

// formatUptime renders seconds as the bun server does: 17d9h / 3h42m / 12m.
func formatUptime(diff int64) string {
	if diff < 0 {
		return ""
	}
	days := diff / 86400
	hours := (diff % 86400) / 3600
	mins := (diff % 3600) / 60
	switch {
	case days > 0:
		return itoa(days) + "d" + itoa(hours) + "h"
	case hours > 0:
		return itoa(hours) + "h" + itoa(mins) + "m"
	default:
		return itoa(mins) + "m"
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func containsSession(sessions []wire.SessionData, name string) bool {
	for _, s := range sessions {
		if s.Name == name {
			return true
		}
	}
	return false
}

func isJSONObject(raw []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil
}
