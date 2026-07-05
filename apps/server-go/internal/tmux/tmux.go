// Package tmux is the Go port of packages/mux/providers/tmux — the read
// and switch surface the server skeleton needs. Every query is one tmux
// exec with a tab-separated format string, mirroring the TS client's
// SESSION/CLIENT specs.
package tmux

import (
	"os/exec"
	"strconv"
	"strings"
)

// sep is the field delimiter in tmux -F format strings (tab, universally
// supported by tmux; matches the TS client's SEP).
const sep = "\t"

// stashSession is the hidden session the sidebar stash lives in; it is
// excluded from every listing (matches provider.ts STASH_SESSION).
const stashSession = "_tcm_stash"

// Session is one row of `tmux list-sessions` (client.ts SESSION_SPEC).
type Session struct {
	ID        string
	Name      string
	CreatedAt int64 // epoch seconds (tmux session_created)
	Attached  int
	Windows   int
	Dir       string
}

// Client is one row of `tmux list-clients` (client.ts CLIENT_SPEC).
type Client struct {
	Name        string
	TTY         string
	PID         int
	SessionName string
	Width       int
	Height      int
}

// Runner executes a tmux command and returns trimmed stdout. The default
// execs the tmux binary; tests inject a fake.
type Runner func(args ...string) (string, error)

// ExecRunner runs the real tmux binary.
func ExecRunner(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// Tmux queries one tmux server through its Runner.
type Tmux struct {
	Run Runner
}

// New returns a Tmux backed by the real binary.
func New() *Tmux { return &Tmux{Run: ExecRunner} }

// ListSessions returns all sessions except the sidebar stash, in tmux
// order. An unreachable tmux server yields an empty list, not an error —
// the sidebar renders empty, same as the TS provider.
func (t *Tmux) ListSessions() []Session {
	out, err := t.Run("list-sessions", "-F",
		"#{session_id}"+sep+"#{session_name}"+sep+"#{session_created}"+sep+
			"#{session_attached}"+sep+"#{session_windows}"+sep+"#{session_path}")
	if err != nil || out == "" {
		return nil
	}
	var sessions []Session
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != 6 || f[1] == stashSession {
			continue
		}
		sessions = append(sessions, Session{
			ID:        f[0],
			Name:      f[1],
			CreatedAt: atoi64(f[2]),
			Attached:  atoi(f[3]),
			Windows:   atoi(f[4]),
			Dir:       f[5],
		})
	}
	return sessions
}

// ListClients returns all attached clients (client.ts listClients).
func (t *Tmux) ListClients() []Client {
	out, err := t.Run("list-clients", "-F",
		"#{client_name}"+sep+"#{client_tty}"+sep+"#{client_pid}"+sep+
			"#{session_name}"+sep+"#{client_width}"+sep+"#{client_height}")
	if err != nil || out == "" {
		return nil
	}
	var clients []Client
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != 6 {
			continue
		}
		clients = append(clients, Client{
			Name: f[0], TTY: f[1], PID: atoi(f[2]),
			SessionName: f[3], Width: atoi(f[4]), Height: atoi(f[5]),
		})
	}
	return clients
}

// CurrentSession resolves which session is "current" for a client
// (client.ts resolveCurrentSession, verbatim semantics): with a clientTty,
// that client's session or none; without one, the lone client's session
// when exactly one is attached, otherwise none — refuse to guess.
func (t *Tmux) CurrentSession(clientTty string) (string, bool) {
	return ResolveCurrentSession(t.ListClients(), clientTty)
}

// ResolveCurrentSession is the pure resolution over a client list; see
// CurrentSession.
func ResolveCurrentSession(clients []Client, clientTty string) (string, bool) {
	if len(clients) == 0 {
		return "", false
	}
	if clientTty != "" {
		for _, c := range clients {
			if c.TTY == clientTty {
				return nonEmpty(c.SessionName)
			}
		}
		return "", false
	}
	if len(clients) == 1 {
		return nonEmpty(clients[0].SessionName)
	}
	return "", false
}

// SwitchClient switches a client to the target session; empty clientTty
// switches the most recently active client (tmux default).
func (t *Tmux) SwitchClient(target, clientTty string) error {
	args := []string{"switch-client"}
	if clientTty != "" {
		args = append(args, "-c", clientTty)
	}
	args = append(args, "-t", target)
	_, err := t.Run(args...)
	return err
}

// NewSession creates a detached session (client.ts newSession).
func (t *Tmux) NewSession(name, dir string) error {
	args := []string{"new-session", "-d"}
	if name != "" {
		args = append(args, "-s", name)
	}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	_, err := t.Run(args...)
	return err
}

// KillSession kills the target session.
func (t *Tmux) KillSession(target string) error {
	_, err := t.Run("kill-session", "-t", target)
	return err
}

// ActiveSessionDirs returns the active pane's cwd per session in one
// list-panes call (client.ts getActiveSessionDirs, filter included: skip
// sidebar panes by @tcm-sidebar marker or pane title). First hit per
// session wins.
func (t *Tmux) ActiveSessionDirs() map[string]string {
	dirs := map[string]string{}
	out, err := t.Run("list-panes", "-a",
		"-f", "#{&&:#{window_active},#{&&:#{!=:#{@tcm-sidebar},1},#{!=:#{pane_title},tcm-sidebar}}}",
		"-F", "#{session_name}"+sep+"#{pane_current_path}")
	if err != nil || out == "" {
		return dirs
	}
	for line := range strings.SplitSeq(out, "\n") {
		name, cwd, ok := strings.Cut(line, sep)
		if !ok || name == "" {
			continue
		}
		if _, seen := dirs[name]; !seen {
			dirs[name] = cwd
		}
	}
	return dirs
}

// AllPaneCounts returns pane counts per session in one list-panes call
// (client.ts getAllPaneCounts).
func (t *Tmux) AllPaneCounts() map[string]int {
	counts := map[string]int{}
	out, err := t.Run("list-panes", "-a", "-F", "#{session_name}")
	if err != nil || out == "" {
		return counts
	}
	for line := range strings.SplitSeq(out, "\n") {
		if line != "" {
			counts[line]++
		}
	}
	return counts
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func nonEmpty(s string) (string, bool) { return s, s != "" }
