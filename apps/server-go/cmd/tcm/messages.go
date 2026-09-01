package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func runMessages(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("messages", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	delegationID := fs.String("delegation", "", "delegation id")
	port := fs.Int("port", defaultServerPort(), "tcm server port")
	if err := fs.Parse(args); err != nil {
		return emitTalkbackError(stdout, err.Error(), 2)
	}
	if *delegationID == "" {
		return emitTalkbackError(stdout, "messages: -delegation is required", 2)
	}
	values := url.Values{"delegation": {*delegationID}}
	status, body, err := getURL(baseURL(*port) + "/delegation-messages?" + values.Encode())
	if err != nil {
		return emitTalkbackError(stdout, "transport failure: "+err.Error(), 1)
	}
	if status != http.StatusOK {
		return emitTalkbackError(stdout, fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(body))), 1)
	}
	_, _ = stdout.Write(body)
	return 0
}
