package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// sleep is time.Sleep behind a seam so tests exercise backoff and the 5s
// waiting re-confirm without actually sleeping.
var sleep = time.Sleep

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
// prompt, ported verbatim from tcm-watch-leg.sh's grep -qiE pattern.
var promptPattern = regexp.MustCompile(`(?i)press enter|y/n|approve|allow|permission|continue\?|› *[0-9]\.`)

// waitConfig is runWait's parsed flags plus the seams tests substitute.
type waitConfig struct {
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
	session := fs.String("session", "", "TCM session name to watch")
	pane := fs.String("pane", "", "pane id to watch (recommended; disambiguates multi-agent sessions)")
	port := fs.Int("port", 7391, "tcm server port")
	minutes := fs.Int("minutes", 20, "overall watch budget in minutes")
	pollSeconds := fs.Int("poll-seconds", 30, "fallback poll interval in seconds, and backoff delay on a bad /wait response")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session == "" {
		fmt.Fprintln(os.Stderr, "wait: -session is required")
		return 2
	}

	cfg := waitConfig{session: *session, pane: *pane, port: *port, minutes: *minutes, pollSeconds: *pollSeconds}
	outcome := watchToTerminal(cfg)
	_ = writeJSON(stdout, outcome)
	return 0
}

// watchToTerminal drives the poll loop: /wait long-poll primary, pane/session
// liveness checked before each round, /state + pane-quiescence fallback when
// /wait itself is unreachable. minutes budget exceeded without a terminal
// state yields "timeout".
func watchToTerminal(cfg waitConfig) waitOutcome {
	deadline := time.Now().Add(time.Duration(cfg.minutes) * time.Minute)
	polls := 0
	lastState := ""
	lastHash := ""
	quiescenceCount := 0
	unreadable := 0

	for {
		if !time.Now().Before(deadline) {
			return waitOutcome{Outcome: "timeout", Polls: polls, State: lastState}
		}

		if cfg.pane != "" && !paneAlive(cfg.pane) {
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
			sleep(time.Duration(cfg.pollSeconds) * time.Second)
			continue
		}

		resp, ok := pollWait(cfg, timeoutSeconds)
		if !ok {
			// /wait itself is unreachable (non-200/transport/unparseable):
			// the spike's 10.6M hot-loop regression was this branch treating
			// a bad response as "poll again" with no sleep.
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
		case "done":
			return waitOutcome{Outcome: "finished", Polls: polls, State: resp.Status}
		case "error", "interrupted":
			return waitOutcome{Outcome: "error", Polls: polls, State: resp.Status}
		case "gone":
			return waitOutcome{Outcome: "session-dead", Polls: polls, State: resp.Status}
		case "waiting":
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
				if confirm.Status == "waiting" {
					return waitOutcome{Outcome: "waiting", Polls: polls, State: "waiting"}
				}
			}
			continue
		default:
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
	if cfg.pane != "" {
		values.Set("pane", cfg.pane)
	}
	status, body, err := getURL(baseURL(cfg.port) + "/wait?" + values.Encode())
	if err != nil || status != 200 {
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
			if cfg.pane == "" || agent.PaneID == cfg.pane {
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

// sessionExists checks tmux session liveness through the tmuxOutput seam.
// tmuxOutput only returns stdout, so has-session (which prints nothing) is
// probed with display-message instead — any tmux error means the session
// is gone.
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
