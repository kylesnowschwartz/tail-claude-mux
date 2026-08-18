package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// resultErrorOutcome is the JSON shape tcm result prints on failure.
type resultErrorOutcome struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

// runResult fetches GET /result for one agent address and prints the server's
// response body unchanged. It returns the process exit code.
func runResult(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("result", flag.ContinueOnError)
	delegationID := fs.String("delegation", "", "delegation id")
	threadID := fs.String("thread", "", "agent thread id")
	session := fs.String("session", "", "delegate session name")
	pane := fs.String("pane", "", "delegate pane id")
	port := fs.Int("port", 7391, "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateResultAddress(*delegationID, *threadID, *session, *pane); err != nil {
		_ = writeJSON(stdout, resultErrorOutcome{Outcome: "error", Detail: err.Error()})
		return 2
	}

	query := url.Values{}
	if *delegationID != "" {
		query.Set("delegation", *delegationID)
	}
	if *threadID != "" {
		query.Set("thread", *threadID)
	}
	if *session != "" {
		query.Set("session", *session)
		query.Set("pane", *pane)
	}

	status, body, err := getURL(baseURL(*port) + "/result?" + query.Encode())
	if err != nil {
		return emitResultError(stdout, err.Error())
	}
	if status != 200 {
		return emitResultError(stdout, fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(body))))
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return emitResultError(stdout, fmt.Sprintf("unparseable response body: %v", err))
	}
	_ = writeJSON(stdout, decoded)
	return 0
}

func validateResultAddress(delegationID, threadID, session, pane string) error {
	sessionPane := session != "" && pane != ""
	partialSessionPane := (session == "") != (pane == "")
	modeCount := 0
	for _, selected := range []bool{delegationID != "", threadID != "", sessionPane} {
		if selected {
			modeCount++
		}
	}
	if modeCount != 1 || partialSessionPane {
		return errors.New("exactly one address is required: -delegation, -thread, or -session with -pane")
	}
	return nil
}

func emitResultError(stdout io.Writer, detail string) int {
	_ = writeJSON(stdout, resultErrorOutcome{Outcome: "error", Detail: detail})
	return 1
}
