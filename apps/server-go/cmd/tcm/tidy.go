package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// delegateSignature matches a pane's start command against the durable
// spawn signature `codex --profile tcm-delegate` (set by tcm delegate's
// spawn body). This never matches a codex shell opened by hand, so the
// sweep is safe to run at any time.
var delegateSignature = regexp.MustCompile(`codex.*--profile.*tcm-delegate`)

// statusUntracked marks a delegate window whose pane GET /result 404s for
// ("session not found") — the tracker has nothing recorded for it, most
// often because the server restarted after the delegate finished. It is
// not one of wire's tracked AgentStatus values; resultStatus assigns it
// from the HTTP status, never from a server-emitted status field.
const statusUntracked = "untracked"

// tidyTerminalStatus is the set of statuses that mean a delegate's window
// is safe to close without -all, even if its codex process is still idling
// at the prompt: a delegate's codex keeps running interactively after
// finishing a turn, so process state alone can't tell "done" from "still
// working". An untracked window is also safe to close on its own — nothing
// is tracking it, so there is no live turn -all would be needed to override.
var tidyTerminalStatus = map[string]bool{
	wire.StatusDone:        true,
	wire.StatusError:       true,
	wire.StatusInterrupted: true,
	statusUntracked:        true,
}

// tidyCandidate is one matched delegate window in tcm tidy's stdout JSON.
type tidyCandidate struct {
	WindowID    string `json:"windowId"`
	SessionName string `json:"sessionName"`
	PaneID      string `json:"paneId"`
	Status      string `json:"status"`
	Action      string `json:"action"`
}

// tidyOutcome is the JSON shape tcm tidy prints to stdout.
type tidyOutcome struct {
	DryRun     bool            `json:"dryRun"`
	Candidates []tidyCandidate `json:"candidates"`
	Closed     int             `json:"closed"`
}

// tidyWindow tracks one delegate window while candidates are being built:
// its most recent pane_start_command match, activity epoch (for --keep's
// newest-first ordering), and the terminal-status verdict from GET /result.
type tidyWindow struct {
	windowID    string
	sessionName string
	paneID      string
	activity    int64
	status      string
	closeable   bool
}

// runTidy closes finished TCM codex delegate tmux windows. It returns the
// process exit code.
func runTidy(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("tidy", flag.ContinueOnError)
	allSessions := fs.Bool("all-sessions", false, "sweep every tmux session, not just the current one")
	keep := fs.Int("keep", 0, "keep the N most-recently-active delegate windows")
	all := fs.Bool("all", false, "close every matched delegate regardless of status")
	dryRun := fs.Bool("dry-run", false, "print what would close, change nothing")
	port := fs.Int("port", 7391, "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	scope := ""
	if !*allSessions {
		scope = currentTmuxSession()
		if scope == "" {
			fmt.Fprintln(os.Stderr, "tidy: can't determine the current tmux session; pass -all-sessions to sweep every session")
			return 2
		}
	}

	windows, err := scanDelegateWindows(scope, *allSessions, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tidy:", err)
		return 1
	}

	// Newest-first so -keep preserves the most-recently-active windows.
	sort.Slice(windows, func(i, j int) bool { return windows[i].activity > windows[j].activity })

	outcome := tidyOutcome{DryRun: *dryRun}
	for i, w := range windows {
		cand := tidyCandidate{WindowID: w.windowID, SessionName: w.sessionName, PaneID: w.paneID, Status: w.status}
		switch {
		case *keep > 0 && i < *keep:
			cand.Action = "keep"
		case !w.closeable && !*all:
			cand.Action = "skip"
		case *dryRun:
			cand.Action = "would-close"
		default:
			if _, err := tmuxOutput("kill-window", "-t", w.windowID); err != nil {
				cand.Action = "gone"
			} else {
				cand.Action = "closed"
				outcome.Closed++
			}
		}
		outcome.Candidates = append(outcome.Candidates, cand)
	}

	_ = writeJSON(stdout, outcome)
	return 0
}

// currentTmuxSession resolves the owning tmux session the same way tcm
// delegate does, only inside a running tmux client.
func currentTmuxSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := tmuxOutput("display-message", "-p", "#{session_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// scanDelegateWindows lists every pane across (scoped) sessions, keeps the
// ones whose start command matches the delegate signature, and asks the
// server for each matched window's authoritative status.
func scanDelegateWindows(scope string, allSessions bool, port int) ([]tidyWindow, error) {
	out, err := tmuxOutput("list-panes", "-a", "-F", "#{window_id}\t#{window_activity}\t#{session_name}\t#{pane_id}\t#{pane_start_command}")
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}

	byWindow := map[string]*tidyWindow{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 {
			continue
		}
		windowID, activityStr, sessionName, paneID, startCmd := fields[0], fields[1], fields[2], fields[3], fields[4]
		if !delegateSignature.MatchString(startCmd) {
			continue
		}
		if !allSessions && sessionName != scope {
			continue
		}
		activity, _ := strconv.ParseInt(activityStr, 10, 64)

		w, ok := byWindow[windowID]
		if !ok {
			w = &tidyWindow{windowID: windowID, sessionName: sessionName, paneID: paneID}
			byWindow[windowID] = w
			order = append(order, windowID)
		}
		if activity > w.activity {
			w.activity = activity
		}

		status := resultStatus(port, sessionName, paneID)
		if status == "" {
			status = "unknown"
		}
		w.status = status
		if tidyTerminalStatus[status] {
			w.closeable = true
		}
	}

	windows := make([]tidyWindow, 0, len(order))
	for _, id := range order {
		windows = append(windows, *byWindow[id])
	}
	return windows, nil
}

// resultStatus asks the server for one pane's authoritative status via GET
// /result. A 404 means the session isn't tracked, mapped to statusUntracked.
// Any other transport failure, non-200, or unparseable body yields "" —
// treated as "unknown" and never closed unless -all is given.
func resultStatus(port int, session, pane string) string {
	values := url.Values{}
	values.Set("session", session)
	values.Set("pane", pane)
	status, body, err := getURL(baseURL(port) + "/result?" + values.Encode())
	if err != nil {
		return ""
	}
	if status == 404 {
		return statusUntracked
	}
	if status != 200 {
		return ""
	}
	var decoded struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	return decoded.Status
}
