package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// requestTimeout bounds an ordinary (non-long-poll) call to the tcm server:
// every HTTP call here must time out rather than hang forever if the server
// stops responding.
const requestTimeout = 30 * time.Second

// httpClient is shared by postJSON and getURL for ordinary calls.
var httpClient = &http.Client{Timeout: requestTimeout}

// tmuxOutput runs a tmux command and returns its stdout. Tests stub this to
// avoid depending on a real tmux server.
var tmuxOutput = func(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	return string(out), err
}

// baseURL builds the tcm server's address from a -port flag value.
func baseURL(port int) string {
	return fmt.Sprintf("http://%s:%d", wire.ServerHost, port)
}

func defaultServerPort() int {
	port, err := strconv.Atoi(os.Getenv("TCM_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return wire.ServerPort
	}
	return port
}

// writeJSON prints v to w as a single line of JSON, exactly the shape the
// caller passed in — no extra fields, no reformatting.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

// postJSON POSTs body (already JSON-encoded) to the tcm server and returns
// the response status and body. A non-nil error means the request never
// reached the server (connection refused, DNS, etc.).
func postJSON(url string, body []byte) (status int, respBody []byte, err error) {
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// getURL performs a GET against the tcm server and returns the response
// status and body. A non-nil error means the request never reached the
// server.
func getURL(url string) (status int, respBody []byte, err error) {
	return getURLWithClient(httpClient, url)
}

// getURLWithClient performs a GET using the given client, so a caller doing
// a long-poll (tcm wait's /wait request) can supply a timeout wider than
// requestTimeout instead of sharing the ordinary-call client.
func getURLWithClient(client *http.Client, url string) (status int, respBody []byte, err error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}
