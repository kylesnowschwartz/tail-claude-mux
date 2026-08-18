package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// sleep is time.Sleep behind a seam so tests exercise backoff and the 5s
// waiting re-confirm without actually sleeping.
var sleep = time.Sleep

// waitClientGrace is added on top of a /wait long-poll's requested timeout
// to build the HTTP client's own deadline, so a hung server still fails the
// request instead of blocking forever. Tests shrink this to keep a
// stub-server-never-responds case fast.
var waitClientGrace = 30 * time.Second

// waitOutcome is the JSON shape tcm wait prints to stdout. State is the last
// server-reported status this run observed (empty when /wait was never
// reachable).
type waitOutcome struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	Polls   int    `json:"polls"`
	State   string `json:"state,omitempty"`
}

// waitResponse mirrors internal/server's GET /wait JSON body.
type waitResponse struct {
	Session        string `json:"session"`
	Status         string `json:"status"`
	Terminal       bool   `json:"terminal"`
	TimedOut       bool   `json:"timedOut"`
	ElapsedSeconds int64  `json:"elapsedSeconds"`
}

// stateAgent is the subset of GET /state's agents[] entries tcm wait reads
// to find this pane's status when /wait itself is unreachable.
type stateAgent struct {
	Status string `json:"status"`
	PaneID string `json:"paneId"`
}

type stateSession struct {
	Name   string       `json:"name"`
	Agents []stateAgent `json:"agents"`
}

type stateResponse struct {
	Sessions []stateSession `json:"sessions"`
}

// promptPattern matches a pane's tail when it is sitting at an approval
// prompt: press-enter/y-n/approve/allow/permission/continue/numbered-choice
// text in the pane's last few non-blank lines.
var promptPattern = regexp.MustCompile(`(?i)press enter|y/n|approve|allow|permission|continue\?|› *[0-9]\.`)

// waitConfig is runWait's parsed flags plus the seams tests substitute.
type waitConfig struct {
	delegation  string
	session     string
	pane        string
	port        int
	minutes     int
	pollSeconds int
}

// runWait watches one delegate to a terminal outcome and prints the result
// as one line of JSON. It returns the process exit code (0 unless usage is
// wrong; a terminal, error, or timeout outcome is still exit 0 — the JSON
// "outcome" field is the signal, matching tcm-watch-leg.sh's contract of
// always printing exactly one RESOLUTION line).
func runWait(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	delegationID := fs.String("delegation", "", "delegation id")
	session := fs.String("session", "", "TCM session name to watch")
	pane := fs.String("pane", "", "pane id to watch (required; disambiguates multi-agent sessions)")
	port := fs.Int("port", 7391, "tcm server port")
	minutes := fs.Int("minutes", 20, "overall watch budget in minutes")
	pollSeconds := fs.Int("poll-seconds", 30, "fallback poll interval in seconds, and backoff delay on a bad /wait response")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateWaitAddress(*delegationID, *session, *pane); err != nil {
		fmt.Fprintln(os.Stderr, "wait: "+err.Error())
		return 2
	}

	cfg := waitConfig{delegation: *delegationID, session: *session, pane: *pane, port: *port, minutes: *minutes, pollSeconds: *pollSeconds}
	deadline := time.Now().Add(time.Duration(cfg.minutes) * time.Minute)
	var outcome waitOutcome
	if cfg.delegation != "" {
		outcome = watchDelegationToTerminal(cfg, deadline)
	} else {
		outcome = watchToTerminal(cfg, deadline)
	}
	_ = writeJSON(stdout, outcome)
	return 0
}

func validateWaitAddress(delegationID, session, pane string) error {
	sessionPane := session != "" && pane != ""
	partialSessionPane := (session == "") != (pane == "")
	if partialSessionPane || (delegationID != "") == sessionPane {
		return errors.New("exactly one address is required: -delegation, or -session with -pane")
	}
	return nil
}

func watchDelegationToTerminal(cfg waitConfig, deadline time.Time) waitOutcome {
	polls := 0
	lastState := ""
	for {
		if !time.Now().Before(deadline) {
			return waitOutcome{Outcome: "timeout", Polls: polls, State: lastState}
		}
		remaining := time.Until(deadline)
		timeoutSeconds := int(remaining.Seconds())
		if timeoutSeconds > 540 {
			timeoutSeconds = 540
		}
		if timeoutSeconds < 5 {
			return waitOutcome{Outcome: "timeout", Polls: polls, State: lastState}
		}

		resp, ok := pollDelegationWait(cfg, timeoutSeconds)
		polls++
		if !ok {
			sleep(time.Duration(cfg.pollSeconds) * time.Second)
			continue
		}
		lastState = resp.Status
		switch resp.Status {
		case wire.StatusDone:
			return waitOutcome{Outcome: "finished", Polls: polls, State: resp.Status}
		case wire.StatusError, wire.StatusInterrupted:
			return waitOutcome{Outcome: "error", Polls: polls, State: resp.Status}
		case wire.StatusGone:
			return waitOutcome{Outcome: "session-dead", Polls: polls, State: resp.Status}
		case wire.StatusWaiting:
			if resp.TimedOut {
				continue
			}
			sleep(5 * time.Second)
			confirm, confirmOK := pollDelegationWait(cfg, 5)
			polls++
			if confirmOK {
				lastState = confirm.Status
				if confirm.Status == wire.StatusWaiting {
					return waitOutcome{Outcome: "waiting", Polls: polls, State: wire.StatusWaiting}
				}
			}
			continue
		default:
			if resp.Terminal {
				return waitOutcome{Outcome: "error", Polls: polls, State: resp.Status}
			}
		}
	}
}

// watchToTerminal drives the poll loop: /wait long-poll primary, pane/session
// liveness checked before each round, /state + pane-quiescence fallback when
// /wait itself is unreachable. The budget expiring (deadline reached, or
// less than 5s of it left — too little to make a long-poll worthwhile)
// without a terminal state yields "timeout".
func watchToTerminal(cfg waitConfig, deadline time.Time) waitOutcome {
	polls := 0
	lastState := ""
	lastHash := ""
	quiescenceCount := 0
	unreadable := 0

	for {
		if !time.Now().Before(deadline) {
			return waitOutcome{Outcome: "timeout", Polls: polls, State: lastState}
		}

		if !paneAlive(cfg.pane) {
			return waitOutcome{Outcome: "session-dead", Detail: "pane-gone", Polls: polls, State: lastState}
		}
		if !sessionExists(cfg.session) {
			return waitOutcome{Outcome: "session-dead", Detail: "session-gone", Polls: polls, State: lastState}
		}

		remaining := time.Until(deadline)
		timeoutSeconds := int(remaining.Seconds())
		if timeoutSeconds > 540 {
			timeoutSeconds = 540
		}
		if timeoutSeconds < 5 {
			return waitOutcome{Outcome: "timeout", Polls: polls, State: lastState}
		}

		resp, ok := pollWait(cfg, timeoutSeconds)
		if !ok {
			// /wait itself is unreachable (non-200/transport/unparseable):
			// a failed poll must sleep before retrying; an instant re-poll
			// with no sleep busy-loops.
			outcome, done := pollStateFallback(cfg, &lastState, &lastHash, &quiescenceCount, &unreadable)
			polls++
			if done {
				outcome.Polls = polls
				return outcome
			}
			sleep(time.Duration(cfg.pollSeconds) * time.Second)
			continue
		}
		polls++
		lastState = resp.Status

		switch resp.Status {
		case wire.StatusDone:
			return waitOutcome{Outcome: "finished", Polls: polls, State: resp.Status}
		case wire.StatusError, wire.StatusInterrupted:
			return waitOutcome{Outcome: "error", Polls: polls, State: resp.Status}
		case wire.StatusGone:
			return waitOutcome{Outcome: "session-dead", Polls: polls, State: resp.Status}
		case wire.StatusWaiting:
			if resp.TimedOut {
				continue
			}
			// An approval prompt can flash and self-resolve; confirm it
			// holds for 5s before reporting.
			sleep(5 * time.Second)
			confirm, confirmOK := pollWait(cfg, 5)
			polls++
			if confirmOK {
				lastState = confirm.Status
				if confirm.Status == wire.StatusWaiting {
					return waitOutcome{Outcome: "waiting", Polls: polls, State: "waiting"}
				}
			}
			continue
		default:
			// resp.Terminal is the server's own verdict (wire.IsTerminalStatus)
			// on whether the turn is over; a status this switch doesn't
			// recognize but the server still marked terminal is an error
			// outcome rather than an infinite "keep polling".
			if resp.Terminal {
				return waitOutcome{Outcome: "error", Polls: polls, State: resp.Status}
			}
			// idle/running (or a timed-out non-terminal poll): keep looping.
			continue
		}
	}
}

// pollWait performs one long-poll GET /wait?session&pane&timeout and reports
// whether the response was a well-formed 200. Pane and session are passed
// through url.Values so a pane id's leading "%" round-trips correctly.
func pollWait(cfg waitConfig, timeoutSeconds int) (waitResponse, bool) {
	values := url.Values{}
	values.Set("session", cfg.session)
	values.Set("timeout", fmt.Sprintf("%d", timeoutSeconds))
	values.Set("pane", cfg.pane)
	// The server's long-poll can legitimately run up to timeoutSeconds; the
	// client's own timeout adds a 30s grace on top so a hung server still
	// fails the request instead of blocking forever.
	client := &http.Client{Timeout: time.Duration(timeoutSeconds)*time.Second + waitClientGrace}
	status, body, err := getURLWithClient(client, baseURL(cfg.port)+"/wait?"+values.Encode())
	if err != nil || status != 200 {
		return waitResponse{}, false
	}
	var resp waitResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return waitResponse{}, false
	}
	return resp, true
}

func pollDelegationWait(cfg waitConfig, timeoutSeconds int) (waitResponse, bool) {
	values := url.Values{}
	values.Set("delegation", cfg.delegation)
	values.Set("timeout", fmt.Sprintf("%d", timeoutSeconds))
	client := &http.Client{Timeout: time.Duration(timeoutSeconds)*time.Second + waitClientGrace}
	status, body, err := getURLWithClient(client, baseURL(cfg.port)+"/wait?"+values.Encode())
	if err != nil || status != http.StatusOK {
		return waitResponse{}, false
	}
	var resp waitResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return waitResponse{}, false
	}
	return resp, true
}

// pollStateFallback runs one round of the degraded path: GET /state, find
// this pane's agent entry, and fold it into the quiescence tally. Returns
// the outcome and whether it is terminal.
func pollStateFallback(cfg waitConfig, lastState, lastHash *string, quiescenceCount, unreadable *int) (waitOutcome, bool) {
	agentStatus, found := statePaneStatus(cfg)
	if found {
		*lastState = agentStatus
	}
	if agentStatus == "error" {
		return waitOutcome{Outcome: "error", State: agentStatus}, true
	}
	if agentStatus == "running" {
		*quiescenceCount = 0
		return waitOutcome{}, false
	}

	content := capturePane(cfg.pane)
	if content == "" {
		*unreadable++
		if *unreadable >= 3 {
			return waitOutcome{Outcome: "timeout", Detail: "pane-unreadable", State: *lastState}, true
		}
		return waitOutcome{}, false
	}
	*unreadable = 0
	hash := hashContent(content)
	if hash != *lastHash {
		*quiescenceCount = 0
		*lastHash = hash
		return waitOutcome{}, false
	}
	*quiescenceCount++
	if *quiescenceCount < 3 {
		return waitOutcome{}, false
	}
	if promptPattern.MatchString(tailLines(content, 4)) {
		return waitOutcome{Outcome: "waiting", State: *lastState}, true
	}
	return waitOutcome{Outcome: "finished", State: *lastState}, true
}

// statePaneStatus finds cfg.pane's agent status inside GET /state's JSON,
// scoped to this one pane rather than session-wide — a horizontally-owned
// session can carry several agents' worth of panes.
func statePaneStatus(cfg waitConfig) (string, bool) {
	status, body, err := getURL(baseURL(cfg.port) + "/state")
	if err != nil || status != 200 {
		return "", false
	}
	var state stateResponse
	if err := json.Unmarshal(body, &state); err != nil {
		return "", false
	}
	for _, session := range state.Sessions {
		if session.Name != cfg.session {
			continue
		}
		for _, agent := range session.Agents {
			if agent.PaneID == cfg.pane {
				return agent.Status, true
			}
		}
	}
	return "", false
}

// tailLines returns the last n non-blank lines of content, matching
// tcm-watch-leg.sh's `grep -v '^[[:space:]]*$' | tail -4`.
func tailLines(content string, n int) string {
	var nonBlank []string
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			nonBlank = append(nonBlank, line)
		}
	}
	if len(nonBlank) > n {
		nonBlank = nonBlank[len(nonBlank)-n:]
	}
	return strings.Join(nonBlank, "\n")
}

func hashContent(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// sessionExists checks tmux session liveness with has-session. has-session
// prints nothing on success, so tmuxOutput's stdout return is unused here —
// any tmux error means the session is gone.
func sessionExists(session string) bool {
	_, err := tmuxOutput("has-session", "-t", "="+session)
	return err == nil
}

// capturePane returns the pane's visible content, or "" on any tmux error
// (dead pane, dead session) — same as an unreadable capture in the shell
// script.
func capturePane(pane string) string {
	if pane == "" {
		return ""
	}
	out, err := tmuxOutput("capture-pane", "-p", "-t", pane)
	if err != nil {
		return ""
	}
	return out
}
