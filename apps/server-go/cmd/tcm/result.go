package main

import (
	"encoding/json"
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

// runResult fetches GET /result for a delegate session/pane and prints the
// server's response body unchanged. It returns the process exit code.
func runResult(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("result", flag.ContinueOnError)
	session := fs.String("session", "", "delegate session name")
	pane := fs.String("pane", "", "delegate pane id")
	port := fs.Int("port", 7391, "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	query := url.Values{}
	query.Set("session", *session)
	query.Set("pane", *pane)

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

func emitResultError(stdout io.Writer, detail string) int {
	_ = writeJSON(stdout, resultErrorOutcome{Outcome: "error", Detail: detail})
	return 1
}
