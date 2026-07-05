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
	"strings"
	"sync"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/metadata"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/panescan"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ws"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// Server owns the hub, the state builder, and the agent pipeline. All
// state mutation runs under mu — commands, hooks, and refresh ticks
// serialize the same way the bun server's single JS thread does.
type Server struct {
	Builder  *state.Builder
	Tracker  *tracker.Tracker
	Watcher  *ccwatch.Adapter
	Scanner  *panescan.Scanner
	Metadata *metadata.Store

	// Restart is invoked ~50ms after answering POST /restart (the dev
	// loop's `restart.sh` ingress). main wires it to a self-exec.
	Restart func()
	// Quit is invoked ~50ms after POST /quit broadcasts quit-notify to
	// every sidebar (stop.sh's graceful path). main wires it to exit.
	Quit func()

	mu        sync.Mutex
	clients   map[*client]bool
	lastState []byte // last broadcast ServerState, JSON-encoded

	// Watcher plumbing (see watch.go).
	watchersSeeded    bool
	broadcastTimer    *time.Timer
	lastSessions      []string // names from the last Build, for empty-presence sweeps
	lastSeenByThread  map[string]lastSeen
	dirSessionCache   map[string]string
	dirSessionCacheAt time.Time
	panesCache        []tmux.Pane
	panesCacheAt      time.Time
}

// client is one connected TUI instance plus the identity it reported.
type client struct {
	conn        *ws.Conn
	tty         string // from "identify"
	sessionName string // from "identify-pane"
}

// New returns a Server around the builder and agent pipeline. Tracker is
// required; watcher and scanner may be nil (tests).
func New(b *state.Builder, tr *tracker.Tracker, w *ccwatch.Adapter, sc *panescan.Scanner) *Server {
	if tr != nil {
		b.Agents = tr
	}
	md := metadata.NewStore()
	b.Metadata = md
	return &Server{
		Builder: b, Tracker: tr, Watcher: w, Scanner: sc, Metadata: md,
		clients:          map[*client]bool{},
		lastSeenByThread: map[string]lastSeen{},
	}
}

// Handler returns the HTTP handler with every route mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("POST /hook", s.handleHook)
	mux.HandleFunc("POST /focus", s.handleFocus)
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		s.broadcast()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("POST /restart")
		_, _ = fmt.Fprint(w, "restarting")
		if s.Restart != nil {
			// Respond before restarting so the caller gets confirmation
			// (mirrors the bun server's 50ms grace).
			time.AfterFunc(50*time.Millisecond, s.Restart)
		}
	})
	mux.HandleFunc("POST /quit", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("POST /quit")
		s.mu.Lock()
		if quit, err := json.Marshal(wire.QuitNotify{Type: wire.TypeQuit}); err == nil {
			for c := range s.clients {
				_ = c.conn.WriteText(string(quit))
			}
		}
		s.mu.Unlock()
		_, _ = fmt.Fprint(w, "quitting")
		if s.Quit != nil {
			time.AfterFunc(50*time.Millisecond, s.Quit)
		}
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
		s.broadcastIfChanged()
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.handleWS(w, r)
		return
	}
	fmt.Fprint(w, "tcm server (go)")
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

// handleHook is the /hook ingress: parse, then dispatch to the watcher
// under the state lock. The contract: always 200, a malformed payload
// never blocks the agent (dropped, not 4xx'd).
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	// 1 MiB bound matches the bun server's tolerance for big ps snapshots.
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if p, ok := wire.ParseHookPayload(body); ok {
		if s.Watcher != nil {
			s.mu.Lock()
			s.Watcher.HandleHook(p)
			s.mu.Unlock()
		}
	} else {
		log.Printf("hook: rejected-malformed")
	}
	w.WriteHeader(http.StatusOK)
}

// handleFocus is the tmux focus hook: switching sessions outside the
// sidebar marks the target session active and clears its unseen flags.
func (s *Server) handleFocus(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
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
		st := s.Builder.Build()
		if *cmd.Index >= 0 && *cmd.Index < len(st.Sessions) {
			name := st.Sessions[*cmd.Index].Name
			if err := s.Builder.Tmux.SwitchClient(name, c.tty); err != nil {
				log.Printf("switch-index %d → %q: %v", *cmd.Index, name, err)
			}
			s.Builder.SetFocused(name)
		}
		s.broadcastLocked()

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
		s.broadcastLocked()

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
			s.focusAgentPane(cmd)
		}

	case wire.CmdKillAgentPane:
		// Deliberately inert until stage 5: killing needs the pid
		// re-verification gate (pane recycling) before it is safe.
		log.Printf("kill-agent-pane: not implemented (stage 5)")

	case wire.CmdSetTheme, wire.CmdReportWidth, wire.CmdEqualizeWidth:
		// Config writers — deliberately inert while the bun server owns
		// config.json (A/B safety).
	}
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
