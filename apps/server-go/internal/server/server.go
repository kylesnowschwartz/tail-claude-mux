// Package server is the Go tcm server: HTTP routes + WebSocket broadcast
// hub + client-command dispatch, mirroring the route surface of
// packages/runtime/src/server/index.ts.
//
// Stage 3 skeleton scope, deliberately:
//
//   - GET /state, POST /hook (parse + drop; watchers are stage 4), and
//     the WS session-list commands are live.
//   - mark-seen / dismiss-agent / focus-agent-pane / kill-agent-pane are
//     accepted no-ops until the tracker lands.
//   - set-theme / report-width / equalize-width are accepted but do NOT
//     write config.json while the bun server owns it — A/B runs must not
//     fight over shared config files.
//   - No idle shutdown: an A/B server should not exit while the bun
//     server keeps the sidebar alive.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ws"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// Server owns the hub and the state builder. All state mutation runs
// under mu — commands and refresh ticks serialize the same way the bun
// server's single JS thread does.
type Server struct {
	Builder *state.Builder

	mu        sync.Mutex
	clients   map[*client]bool
	lastState []byte // last broadcast ServerState, JSON-encoded
}

// client is one connected TUI instance plus the identity it reported.
type client struct {
	conn        *ws.Conn
	tty         string // from "identify"
	sessionName string // from "identify-pane"
}

// New returns a Server around the builder.
func New(b *state.Builder) *Server {
	return &Server{Builder: b, clients: map[*client]bool{}}
}

// Handler returns the HTTP handler with every route mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("POST /hook", s.handleHook)
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		s.broadcast()
		w.WriteHeader(http.StatusNoContent)
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
	data, err := json.MarshalIndent(s.Builder.Build(), "", "  ")
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleHook is the /hook ingress. Stage 3 parses and drops; the watcher
// stages route payloads to adapters. The contract holds already: always
// 200, a malformed payload never blocks the agent.
func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
		if len(body) > 1<<20 { // matches the bun server's tolerance for big ps snapshots
			break
		}
	}
	if p, ok := wire.ParseHookPayload(body); ok {
		log.Printf("hook: %s session=%s (dropped — watchers land in stage 4)", p.Event, p.SessionID)
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
		// Optimistic focus update, ported from the bun handler.
		s.Builder.SetFocused(cmd.Name)
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

	case wire.CmdMarkSeen, wire.CmdDismissAgent, wire.CmdFocusAgentPane, wire.CmdKillAgentPane:
		// Tracker commands — live in stage 4/5.

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

// encodeState builds and encodes ServerState with a stable ts for change
// comparison: ts is excluded from the diff by zeroing it in the comparison
// copy — simplest correct form is to compare the encoding without ts, but
// re-encoding twice per tick for two small JSON docs is cheaper to just
// accept; instead we zero ts pre-encode and stamp it on send.
func (s *Server) encodeState() ([]byte, error) {
	st := s.Builder.Build()
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
