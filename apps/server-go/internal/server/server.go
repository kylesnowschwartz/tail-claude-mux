// Package server is the Go tcm server: HTTP routes + WebSocket broadcast
// hub + client-command dispatch, mirroring the route surface of
// packages/runtime/src/server/index.ts.
//
// Stage 4 scope: the Claude Code hook watcher, tracker, pane scanner, and
// liveness sweeps are live (see watch.go). Still deliberately deferred:
//
//   - kill-agent-pane is an accepted no-op (its pid-verification gate
//     lands with stage 5).
//   - set-theme / report-width / equalize-width are accepted but do NOT
//     write config.json while the bun server owns it — A/B runs must not
//     fight over shared config files.
//   - No idle shutdown: an A/B server should not exit while the bun
//     server keeps the sidebar alive.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/config"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/explain"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/metadata"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/panescan"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/piwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/theming"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ws"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// Server owns the hub, the state builder, and the agent pipeline. All
// state mutation runs under mu — commands, hooks, and refresh ticks
// serialize the same way the bun server's single JS thread does.
type Server struct {
	Builder      *state.Builder
	Tracker      *tracker.Tracker
	Watcher      *ccwatch.Adapter
	PiWatcher    *piwatch.Adapter
	CodexWatcher *codexwatch.Adapter
	Scanner      *panescan.Scanner
	Metadata     *metadata.Store
	BuildInfo    string

	// Restart is invoked ~50ms after answering POST /restart (the dev
	// loop's `restart.sh` ingress). main wires it to a self-exec;
	// reloadTUI asks the next incarnation to kill + respawn sidebars so
	// TUIs pick up new code (bun: TCM_RELOAD_TUI env).
	Restart func(reloadTUI bool)
	// Quit is invoked ~50ms after POST /quit broadcasts quit-notify to
	// every sidebar (stop.sh's graceful path). main wires it to exit.
	Quit func()

	// ScriptsDir is apps/tui/scripts (start.sh lives there); empty
	// disables sidebar bootstrap. SidebarPosition is "left" or "right".
	ScriptsDir      string
	SidebarPosition string
	// CompanionPane is the optional user command spawned in a pane below
	// every sidebar; the zero value (empty Command) disables the feature.
	CompanionPane state.CompanionPaneConfig

	// Theming (main wires; nil-safe). HeaderEnabled is the @tcm-header
	// gate read once at boot — runtime toggling requires restart, same
	// as the bun server.
	Palette       *theming.PaletteWriter
	Header        *theming.HeaderSync
	HeaderEnabled bool

	mu         sync.Mutex
	followupMu sync.Mutex
	clients    map[*client]bool
	lastState  []byte // last broadcast ServerState, JSON-encoded

	// Watcher plumbing (see watch.go).
	watchersSeeded    bool
	broadcastTimer    *time.Timer
	lastSessions      []string // names from the last Build, for empty-presence sweeps
	lastSeenByThread  map[string]lastSeen
	dirSessionCache   map[string]string
	dirSessionCacheAt time.Time
	panesCache        []tmux.Pane
	panesCacheAt      time.Time
	waitPollInterval  time.Duration // tests shorten the in-memory /wait poll.

	// Sidebar lifecycle + width enforcement state (see sidebar.go).
	ensureSidebarTimer      *time.Timer
	ensureSidebarWindowID   string
	sidebarVisible          bool
	pendingEnforcement      bool
	pendingEnforcementTimer *time.Timer
	saveTimer               *time.Timer
	themeFileMtime          time.Time
}

// client is one connected TUI instance plus the identity it reported.
type client struct {
	conn        *ws.Conn
	tty         string // from "identify"
	sessionName string // from "identify-pane"
}

// New returns a Server around the builder and agent pipeline. Tracker is
// required; watchers and scanner may be nil (tests).
func New(b *state.Builder, tr *tracker.Tracker, w *ccwatch.Adapter, pi *piwatch.Adapter, codex *codexwatch.Adapter, sc *panescan.Scanner) *Server {
	if tr != nil {
		b.Agents = tr
	}
	md := metadata.NewStore()
	b.Metadata = md
	return &Server{
		Builder: b, Tracker: tr, Watcher: w, PiWatcher: pi, CodexWatcher: codex, Scanner: sc, Metadata: md,
		clients:          map[*client]bool{},
		lastSeenByThread: map[string]lastSeen{},
	}
}

// Handler returns the HTTP handler with every route mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("GET /wait", s.handleWait)
	mux.HandleFunc("GET /explain", s.handleExplain)
	mux.HandleFunc("POST /hook", s.handleHook)
	mux.HandleFunc("POST /focus", s.handleFocus)
	mux.HandleFunc("POST /spawn-agent", s.handleSpawnAgent)
	mux.HandleFunc("POST /followup", s.handleFollowup)
	mux.HandleFunc("POST /set-status", s.handleSetStatus)
	mux.HandleFunc("POST /set-progress", s.handleSetProgress)
	mux.HandleFunc("POST /log", s.handleLog)
	mux.HandleFunc("POST /notify", s.handleLog)
	mux.HandleFunc("POST /clear-log", s.handleClearLog)
	mux.HandleFunc("POST /ensure-sidebar", s.handleEnsureSidebar)
	mux.HandleFunc("POST /pane-exited", s.handlePaneExited)
	mux.HandleFunc("POST /toggle", s.handleToggle)
	mux.HandleFunc("POST /client-resized", s.handleClientResized)
	mux.HandleFunc("POST /switch-index", s.handleSwitchIndex)
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		s.broadcast()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		reloadTUI := r.URL.Query().Get("reload-tui") != "false"
		log.Printf("POST /restart reload-tui=%v", reloadTUI)
		_, _ = fmt.Fprint(w, "restarting")
		if s.Restart != nil {
			// Respond before restarting so the caller gets confirmation
			// (mirrors the bun server's 50ms grace).
			time.AfterFunc(50*time.Millisecond, func() { s.Restart(reloadTUI) })
		}
	})
	mux.HandleFunc("POST /quit", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("POST /quit")
		_, _ = fmt.Fprint(w, "quitting")
		go s.quitAll()
	})
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

// Run starts the periodic refresh loop; it returns when interval <= 0.
func (s *Server) Run(interval time.Duration) {
	if interval <= 0 {
		return
	}
	for range time.Tick(interval) {
		s.checkExternalTheme()
		s.broadcastIfChanged()
	}
}

// checkExternalTheme re-applies the tmux palette when the-themer rewrites
// ~/.config/tcm/active-theme.json. The bun server used an fsnotify
// watcher; polling on the refresh tick trades ≤2s of lag for zero watcher
// plumbing — state.Build already re-reads the file per broadcast, so only
// the tmux-side palette application needs this trigger.
func (s *Server) checkExternalTheme() {
	info, err := os.Stat(filepath.Join(s.Builder.ConfigDir, "active-theme.json"))
	if err != nil {
		return
	}
	mtime := info.ModTime()
	s.mu.Lock()
	changed := !mtime.Equal(s.themeFileMtime) && !s.themeFileMtime.IsZero()
	s.themeFileMtime = mtime
	if changed {
		log.Printf("theme: active-theme.json changed — reapplying palette")
		s.applyThemeLocked("external-theme-change")
		s.broadcastLocked()
	}
	s.mu.Unlock()
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.handleWS(w, r)
		return
	}
	fmt.Fprint(w, "tcm server (go)")
	if s.BuildInfo != "" {
		fmt.Fprintf(w, " %s", s.BuildInfo)
	}
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.prepareStateLocked(), "", "  ")
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleExplain is GET /explain?session=<name>[&thread=<id>]: a live
// debugging report of every tracked agent's prune-tier standing, with a
// fresh liveness probe per entry (index.ts /explain route).
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	thread := r.URL.Query().Get("thread")
	w.Header().Set("Content-Type", "application/json")
	if session == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing required query parameter: session"}`))
		return
	}

	now := time.Now().UnixMilli()
	s.mu.Lock()
	var reports []explain.Report
	if s.Tracker != nil {
		for _, ev := range s.Tracker.GetAgents(session) {
			if thread != "" && ev.ThreadID != thread {
				continue
			}
			reports = append(reports, explain.Build(ev, now, s.probeLiveness(ev)))
		}
	}
	s.mu.Unlock()

	var threadJSON any
	if thread != "" {
		threadJSON = thread
	}
	out, err := json.MarshalIndent(map[string]any{
		"session": session,
		"thread":  threadJSON,
		"count":   len(reports),
		"reports": reports,
	}, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("explain served session=%s thread=%q count=%d", session, thread, len(reports))
	_, _ = w.Write(out)
}

// handleHook is the /hook ingress: parse, then fan out to every watcher
// under the state lock — each adapter filters on the payload's agent
// discriminator itself (missing falls through as Claude Code). The
// contract: always 200, a malformed payload never blocks the agent
// (dropped, not 4xx'd).
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	// 1 MiB bound matches the bun server's tolerance for big ps snapshots.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if p, ok := wire.ParseHookPayload(body); ok {
		s.mu.Lock()
		if s.Watcher != nil {
			s.Watcher.HandleHook(p)
		}
		if s.PiWatcher != nil {
			s.PiWatcher.HandleHook(p)
		}
		if s.CodexWatcher != nil {
			s.CodexWatcher.HandleHook(p)
		}
		s.mu.Unlock()
	} else {
		log.Printf("hook: rejected-malformed")
	}
	w.WriteHeader(http.StatusOK)
}

// handleFocus is the tmux focus hook: switching sessions outside the
// sidebar marks the target session active and clears its unseen flags.
func (s *Server) handleFocus(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	// A session switch reflows pane widths; the next report-width is an
	// echo, not a drag (index.ts: /focus sets pendingEnforcement).
	s.setPendingEnforcement()
	if session := parseFocusContext(string(body)); session != "" {
		s.mu.Lock()
		s.Builder.SetFocused(session)
		if s.Tracker != nil {
			s.Tracker.HandleFocus(session)
		}
		s.broadcastLocked()
		s.mu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		return
	}
	c := &client{conn: conn}

	s.mu.Lock()
	s.clients[c] = true
	n := len(s.clients)
	// Send current state to the new client immediately (bun: open() sends
	// lastState or triggers a broadcast).
	st, buildErr := s.encodeState()
	s.mu.Unlock()

	log.Printf("ws: client connected (%d)", n)
	if buildErr == nil {
		_ = conn.WriteText(string(st))
	}

	go s.readLoop(c)
}

func (s *Server) readLoop(c *client) {
	defer func() {
		c.conn.Close()
		s.mu.Lock()
		delete(s.clients, c)
		n := len(s.clients)
		s.mu.Unlock()
		log.Printf("ws: client disconnected (%d)", n)
	}()
	for {
		msg, err := c.conn.ReadText()
		if err != nil {
			return
		}
		var cmd wire.ClientCommand
		if json.Unmarshal([]byte(msg), &cmd) != nil {
			continue // bun: malformed command frames are ignored
		}
		s.handleCommand(c, cmd)
	}
}

// handleCommand dispatches one ClientCommand. See the package comment for
// which commands are live vs accepted no-ops in the skeleton.
func (s *Server) handleCommand(c *client, cmd wire.ClientCommand) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Trace client commands (sans the chatty width reports): the cheapest
	// way to tell "the click never fired" from "the click fired the wrong
	// zone" — both have burned real debugging hours when silent.
	if cmd.Type != wire.CmdReportWidth {
		log.Printf("ws-cmd %s session=%s pane=%s thread=%s", cmd.Type, cmd.Session, cmd.PaneID, shortThreadIDSuffix(cmd.ThreadID))
	}

	switch cmd.Type {
	case wire.CmdIdentify:
		c.tty = cmd.ClientTTY

	case wire.CmdIdentifyPane:
		c.sessionName = cmd.SessionName
		reply, _ := json.Marshal(wire.YourSession{
			Type: wire.TypeYourSess,
			Name: cmd.SessionName,
			// ClientTty stays null until the hook context (stage 4) can
			// provide the authoritative per-session TTY.
		})
		_ = c.conn.WriteText(string(reply))

	case wire.CmdSwitchSession:
		tty := cmd.ClientTTY
		if tty == "" {
			tty = c.tty
		}
		if err := s.Builder.Tmux.SwitchClient(cmd.Name, tty); err != nil {
			log.Printf("switch-session %q: %v", cmd.Name, err)
		}
		// Optimistic focus update, ported from the bun handler; focusing a
		// session marks it active and clears its unseen flags.
		s.Builder.SetFocused(cmd.Name)
		if s.Tracker != nil {
			s.Tracker.HandleFocus(cmd.Name)
		}
		s.broadcastLocked()

	case wire.CmdSwitchIndex:
		if cmd.Index == nil {
			return
		}
		s.switchToIndexLocked(*cmd.Index, c.tty)

	case wire.CmdFocusSession:
		s.Builder.SetFocused(cmd.Name)
		s.broadcastLocked()

	case wire.CmdMoveFocus:
		st := s.Builder.Build()
		idx := -1
		for i, sess := range st.Sessions {
			if sess.Name == s.Builder.Focused() {
				idx = i
				break
			}
		}
		if to := idx + cmd.Delta; idx >= 0 && to >= 0 && to < len(st.Sessions) {
			s.Builder.SetFocused(st.Sessions[to].Name)
		}
		s.broadcastLocked()

	case wire.CmdNewSession:
		if err := s.Builder.Tmux.NewSession("", ""); err != nil {
			log.Printf("new-session: %v", err)
		}
		s.broadcastLocked()

	case wire.CmdKillSession:
		if err := s.Builder.Tmux.KillSession(cmd.Name); err != nil {
			log.Printf("kill-session %q: %v", cmd.Name, err)
		}
		s.broadcastLocked()

	case wire.CmdHideSession:
		s.Builder.Order.Hide(cmd.Name)
		s.broadcastLocked()

	case wire.CmdShowAll:
		s.Builder.Order.ShowAll()
		s.broadcastLocked()

	case wire.CmdReorderSession:
		s.Builder.Order.Reorder(cmd.Name, cmd.Delta)
		s.broadcastLocked()

	case wire.CmdRefresh:
		s.broadcastLocked()

	case wire.CmdQuit:
		log.Printf("quit command received")
		// quitAll retakes s.mu for the notify broadcast — dispatch off
		// this locked path.
		go s.quitAll()

	case wire.CmdMarkSeen:
		if s.Tracker != nil && s.Tracker.MarkSeen(cmd.Name) {
			s.broadcastLocked()
		}

	case wire.CmdDismissAgent:
		pid := 0
		if cmd.PID != nil {
			pid = *cmd.PID
		}
		if s.Tracker != nil && s.Tracker.Dismiss(cmd.Session, cmd.Agent, cmd.ThreadID, cmd.PaneID, pid) {
			s.broadcastLocked()
		}

	case wire.CmdFocusAgentPane:
		if s.Tracker != nil {
			if cmd.ClientTTY == "" {
				cmd.ClientTTY = c.tty
			}
			s.focusAgentPane(cmd)
		}

	case wire.CmdKillAgentPane:
		if s.Tracker != nil {
			s.killAgentPane(cmd)
		}

	case wire.CmdSetTheme:
		if err := config.Save(s.Builder.ConfigDir, map[string]any{"theme": cmd.Theme}); err != nil {
			log.Printf("save theme: %v", err)
		}
		s.applyThemeLocked("set-theme")
		s.broadcastLocked()

	case wire.CmdReportWidth:
		s.reportWidthLocked(c, cmd)

	case wire.CmdEqualizeWidth:
		s.equalizeWidthLocked()
	}
}

// switchToIndexLocked switches the client's tmux session to the Nth
// visible sidebar row (switch-index command and /switch-index route).
// Runs with s.mu held.
func (s *Server) switchToIndexLocked(index int, clientTty string) {
	st := s.Builder.Build()
	if index >= 0 && index < len(st.Sessions) {
		name := st.Sessions[index].Name
		if err := s.Builder.Tmux.SwitchClient(name, clientTty); err != nil {
			log.Printf("switch-index %d → %q: %v", index, name, err)
		}
		s.Builder.SetFocused(name)
	}
	s.broadcastLocked()
}

// handleSwitchIndex is the POST /switch-index route (tmux plugin
// keybinds): ?index=N plus a hook context body for the client tty.
func (s *Server) handleSwitchIndex(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.URL.Query().Get("index"))
	if err != nil {
		http.Error(w, "missing index", http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	tty := parseClientTty(string(body))
	s.mu.Lock()
	s.switchToIndexLocked(index, tty)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// parseClientTty extracts the clientTty from a "clientTty|session|windowId"
// hook context body ("" when absent or legacy format).
func parseClientTty(body string) string {
	trimmed := strings.Trim(strings.TrimSpace(body), `"'`)
	if parts := strings.Split(trimmed, "|"); len(parts) == 3 {
		return parts[0]
	}
	return ""
}

// broadcast recomputes state and pushes to every client.
func (s *Server) broadcast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked()
}

// broadcastIfChanged pushes only when the encoded state differs from the
// last broadcast — the refresh tick's cheap change detection.
func (s *Server) broadcastIfChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) == 0 {
		return
	}
	data, err := s.encodeState()
	if err != nil || string(data) == string(s.lastState) {
		return
	}
	s.sendAllLocked(data)
}

func (s *Server) broadcastLocked() {
	data, err := s.encodeState()
	if err != nil {
		log.Printf("broadcast: %v", err)
		return
	}
	s.sendAllLocked(data)
}

// prepareStateLocked runs the tracker maintenance the bun server performs
// on every broadcast — reconcile stale running entries against the
// authoritative probe BEFORE pruning (a stale running whose process is
// still alive is invisible to pruneStuck's liveness guard), then the prune
// tiers — and builds the state. Runs with s.mu held.
func (s *Server) prepareStateLocked() wire.ServerState {
	if s.Tracker != nil {
		s.Tracker.ReconcileStaleRunning(reconcileStaleMS, s.probeLiveness)
		s.Tracker.PruneStuck(stuckRunningTimeoutMS)
		s.Tracker.PruneTerminal()
	}
	st := s.Builder.Build()
	s.lastSessions = s.lastSessions[:0]
	valid := make(map[string]bool, len(st.Sessions))
	for _, sess := range st.Sessions {
		s.lastSessions = append(s.lastSessions, sess.Name)
		valid[sess.Name] = true
	}
	s.Metadata.PruneSessions(valid)
	// Header sync rides every state build (bun: broadcastStateImmediate).
	// Internally diffed per window — no tmux execs when nothing changed.
	if s.Header != nil {
		theme, _ := theming.ResolveActiveTheme(s.Builder.ConfigDir)
		s.Header.Sync(st.Sessions, theme, s.HeaderEnabled)
	}
	return st
}

// encodeState builds and encodes ServerState with a stable ts for change
// comparison: ts is excluded from the diff by zeroing it in the comparison
// copy — simplest correct form is to compare the encoding without ts, but
// re-encoding twice per tick for two small JSON docs is cheaper to just
// accept; instead we zero ts pre-encode and stamp it on send.
func (s *Server) encodeState() ([]byte, error) {
	st := s.prepareStateLocked()
	st.TS = 0
	return json.Marshal(st)
}

func (s *Server) sendAllLocked(data []byte) {
	s.lastState = data
	stamped, err := stampTS(data)
	if err != nil {
		stamped = data
	}
	for c := range s.clients {
		if err := c.conn.WriteText(string(stamped)); err != nil {
			c.conn.Close()
			delete(s.clients, c)
		}
	}
}

// stampTS injects the send-time ts into an encoded ServerState (the
// encoding carries ts:0 so change detection ignores the clock).
func stampTS(data []byte) ([]byte, error) {
	var st wire.ServerState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	st.TS = time.Now().UnixMilli()
	return json.Marshal(st)
}
