package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func runWhoami(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	delegationID := fs.String("delegation", os.Getenv("TCM_DELEGATION_ID"), "delegation id")
	port := fs.Int("port", defaultServerPort(), "tcm server port")
	if err := fs.Parse(args); err != nil {
		return emitTalkbackError(stdout, err.Error(), 2)
	}
	if *delegationID == "" {
		return emitTalkbackError(stdout, "delegation is required: pass -delegation or set TCM_DELEGATION_ID", 2)
	}
	values := url.Values{"delegation": {*delegationID}}
	status, body, err := getURL(baseURL(*port) + "/delegation?" + values.Encode())
	if err != nil {
		return emitTalkbackError(stdout, "transport failure: "+err.Error(), 1)
	}
	if status != http.StatusOK {
		return emitTalkbackError(stdout, fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(body))), 1)
	}
	_, _ = stdout.Write(body)
	return 0
}
