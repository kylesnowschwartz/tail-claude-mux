// Package tmux is the Go port of packages/mux/providers/tmux — the read
// and switch surface the server skeleton needs. Every query is one tmux
// exec with a tab-separated format string, mirroring the TS client's
// SESSION/CLIENT specs.
package tmux

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// sep is the field delimiter in tmux -F format strings (tab, universally
// supported by tmux; matches the TS client's SEP).
const sep = "\t"

// StashSession is the hidden session the sidebar stash lives in; it is
// excluded from every listing (matches provider.ts STASH_SESSION).
const StashSession = "_tcm_stash"

// IgnoreOption is the session-scoped user option external tools set on
// transient utility sessions (e.g. revdiff popup sessions) to keep tcm's
// hands off entirely: no sidebar/companion spawn, no dashboard card, no
// focus-follow. Set it immediately after new-session, before any client
// attaches.
const IgnoreOption = "@tcm-ignore"

// Session is one row of `tmux list-sessions` (client.ts SESSION_SPEC).
type Session struct {
	ID        string
	Name      string
	CreatedAt int64 // epoch seconds (tmux session_created)
	Attached  int
	Windows   int
	Dir       string
	Ignored   bool // @tcm-ignore session option
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
	Run     Runner
	spawnMu sync.Mutex
}

// New returns a Tmux backed by the real binary.
func New() *Tmux { return &Tmux{Run: ExecRunner} }

// ListSessions returns all sessions except the sidebar stash and
// @tcm-ignore'd sessions, in tmux order. Central exclusion, like the
// stash: dashboard cards, focus fallback, and dir→session hook routing
// all see the filtered view. An unreachable tmux server yields an empty
// list, not an error — the sidebar renders empty, same as the TS
// provider.
func (t *Tmux) ListSessions() []Session {
	sessions, _ := t.listSessions()
	var out []Session
	for _, s := range sessions {
		if !s.Ignored {
			out = append(out, s)
		}
	}
	return out
}

// listSessions is the error-reporting form used by operations that cannot
// safely treat an unreachable tmux server as an empty listing. Unlike the
// public ListSessions it keeps @tcm-ignore'd rows — spawn-agent name
// dedupe must still see them.
func (t *Tmux) listSessions() ([]Session, error) {
	// The ignore flag is the LAST field and must never render empty:
	// ExecRunner TrimSpace-trims the output, so a bare #{@tcm-ignore}
	// (empty when unset) would lose its tab on the final row and the
	// 7-field parse would drop that session. The conditional pins it to
	// "1"/"0".
	out, err := t.Run("list-sessions", "-F",
		"#{session_id}"+sep+"#{session_name}"+sep+"#{session_created}"+sep+
			"#{session_attached}"+sep+"#{session_windows}"+sep+"#{session_path}"+sep+
			"#{?"+IgnoreOption+",1,0}")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var sessions []Session
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != 7 || f[1] == StashSession {
			continue
		}
		sessions = append(sessions, Session{
			ID:        f[0],
			Name:      f[1],
			CreatedAt: atoi64(f[2]),
			Attached:  atoi(f[3]),
			Windows:   atoi(f[4]),
			Dir:       f[5],
			Ignored:   f[6] == "1",
		})
	}
	return sessions, nil
}

// SessionIgnored reports whether the exactly-named session carries the
// @tcm-ignore option. Deliberately a scan of the unfiltered listing, NOT
// `show-options -t "=name"`: tmux 3.7b rejects the `=` exact-match prefix
// for set-option/show-options, and plain `-t name` prefix-matches.
func (t *Tmux) SessionIgnored(name string) bool {
	sessions, _ := t.listSessions()
	for _, s := range sessions {
		if s.Name == name {
			return s.Ignored
		}
	}
	return false
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

// Pane is one row of `tmux list-panes -a` carrying every field the server
// needs — state building (counts, active dirs), pid routing, and the agent
// scanner all derive from ONE listing instead of issuing separate
// list-panes variants.
type Pane struct {
	Session      string
	ID           string
	PID          int
	Dir          string // pane_current_path
	WindowActive bool
	Sidebar      bool   // @tcm-sidebar marker or legacy tcm-sidebar title
	Companion    bool   // @tcm-companion marker or tcm-companion title
	WindowIndex  int    // -1 when unparseable
	PaneIndex    int    // -1 when unparseable
	WindowID     string // @-prefixed tmux window id
	Left         int    // pane_left column, -1 when unparseable
	Right        int    // pane_right column, -1 when unparseable
	Width        int    // pane_width columns, -1 when unparseable
	WindowWidth  int    // window_width columns, -1 when unparseable
	Height       int    // pane_height rows, -1 when unparseable
	WindowHeight int    // window_height rows, -1 when unparseable
	Ignored      bool   // @tcm-ignore option on the pane's session
	Title        string
}

// Managed reports whether the pane is tcm-managed (sidebar or companion)
// as opposed to a user's own pane. Every "skip tcm's panes" filter must
// use this, not a hand-written disjunction — a new managed pane kind then
// updates all of them at once.
func (p Pane) Managed() bool { return p.Sidebar || p.Companion }

// ManagedPanes derives the non-stash tcm-managed panes from a listing.
func ManagedPanes(panes []Pane) []Pane {
	var out []Pane
	for _, p := range panes {
		if p.Managed() && p.Session != StashSession {
			out = append(out, p)
		}
	}
	return out
}

// ListAllPanes lists every pane on the server. Title is the last field on
// purpose: it is the only one that can contain the separator. The ignore
// field uses the same #{?,1,0} conditional as listSessions so both parsers
// agree on 1/0 whatever value the option was set to.
func (t *Tmux) ListAllPanes() []Pane {
	out, err := t.Run("list-panes", "-a", "-F",
		"#{session_name}"+sep+"#{pane_id}"+sep+"#{pane_pid}"+sep+
			"#{pane_current_path}"+sep+"#{window_active}"+sep+"#{@tcm-sidebar}"+sep+
			"#{@tcm-companion}"+sep+
			"#{window_index}"+sep+"#{pane_index}"+sep+"#{window_id}"+sep+
			"#{pane_left}"+sep+"#{pane_right}"+sep+"#{pane_width}"+sep+
			"#{window_width}"+sep+"#{pane_height}"+sep+"#{window_height}"+sep+
			"#{?"+IgnoreOption+",1,0}"+sep+"#{pane_title}")
	if err != nil || out == "" {
		return nil
	}
	var panes []Pane
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.SplitN(line, sep, 18)
		if len(f) != 18 || f[0] == "" {
			continue
		}
		pid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		panes = append(panes, Pane{
			Session:      f[0],
			ID:           f[1],
			PID:          pid,
			Dir:          f[3],
			WindowActive: f[4] == "1",
			Sidebar:      f[5] == "1" || f[17] == SidebarPaneTitle,
			Companion:    f[6] == "1" || f[17] == CompanionPaneTitle,
			WindowIndex:  atoiOr(f[7], -1),
			PaneIndex:    atoiOr(f[8], -1),
			WindowID:     f[9],
			Left:         atoiOr(f[10], -1),
			Right:        atoiOr(f[11], -1),
			Width:        atoiOr(f[12], -1),
			WindowWidth:  atoiOr(f[13], -1),
			Height:       atoiOr(f[14], -1),
			WindowHeight: atoiOr(f[15], -1),
			Ignored:      f[16] == "1",
			Title:        f[17],
		})
	}
	return panes
}

// PaneCounts derives pane counts per session (client.ts getAllPaneCounts:
// every pane counts, sidebars included).
func PaneCounts(panes []Pane) map[string]int {
	counts := map[string]int{}
	for _, p := range panes {
		counts[p.Session]++
	}
	return counts
}

// ActiveDirs derives the active pane's cwd per session (client.ts
// getActiveSessionDirs: active window, tcm-managed panes excluded, first
// hit per session wins).
func ActiveDirs(panes []Pane) map[string]string {
	dirs := map[string]string{}
	for _, p := range panes {
		if !p.WindowActive || p.Managed() {
			continue
		}
		if _, seen := dirs[p.Session]; !seen {
			dirs[p.Session] = p.Dir
		}
	}
	return dirs
}

// PanePidIndex derives the pane shell pid → session index the pid-based
// hook router walks ancestry against.
func PanePidIndex(panes []Pane) map[int]string {
	m := map[int]string{}
	for _, p := range panes {
		if p.PID > 0 {
			m[p.PID] = p.Session
		}
	}
	return m
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func nonEmpty(s string) (string, bool) { return s, s != "" }
