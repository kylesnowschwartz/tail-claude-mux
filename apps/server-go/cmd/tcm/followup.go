package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// followupOutcome is the JSON shape tcm followup prints to stdout.
type followupOutcome struct {
	Outcome     string `json:"outcome"`
	HTTPCode    string `json:"httpCode"`
	Receipts    int    `json:"receipts"`
	MessageFile string `json:"messageFile"`
	RolloutPath string `json:"rolloutPath"`
	Detail      string `json:"detail"`
}

// followupBody is the POST /followup request body. Pane is omitted when
// empty so the server resolves it, matching tcm-deliver.sh's two request
// shapes (with/without a pane argument).
type followupBody struct {
	Session string `json:"session"`
	Message string `json:"message"`
	Pane    string `json:"pane,omitempty"`
}

// followupResponse is the subset of POST /followup's 200 response tcm
// followup reads.
type followupResponse struct {
	MessageFile string `json:"messageFile"`
	RolloutPath string `json:"rolloutPath"`
}

// runFollowup delivers a follow-up message to a waiting delegate and
// verifies delivery by grepping the delegate's rollout for a receipt of the
// message file path, a verbatim port of tcm-deliver.sh. It returns the
// process exit code (always 0 — the JSON "outcome" field is the signal).
func runFollowup(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("followup", flag.ContinueOnError)
	session := fs.String("session", "", "TCM session name to deliver the follow-up to")
	pane := fs.String("pane", "", "pane id (omit to let the server resolve it)")
	messageFile := fs.String("message-file", "", "path to the follow-up message file")
	port := fs.Int("port", 7391, "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session == "" || *messageFile == "" {
		fmt.Fprintln(os.Stderr, "followup: -session and -message-file are required")
		return 2
	}

	source, err := os.ReadFile(*messageFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "followup: reading -message-file:", err)
		return 2
	}

	outcome := deliverFollowup(*port, *session, *pane, string(source))
	_ = writeJSON(stdout, outcome)
	return 0
}

// deliverFollowup runs the POST-then-verify ladder: an initial follow-up
// POST, receipt greps at 5s and 25s after delivery, one retry POST, then a
// final grep 30s later. A 409 on the FIRST post is a refusal — the server
// is declining to interrupt an active delegate. A 409 on the retry is not:
// the first POST already respawned the pane, so that conflict means the
// resume took; only the final receipt grep classifies the retry path.
func deliverFollowup(port int, session, pane, message string) followupOutcome {
	body, err := json.Marshal(followupBody{Session: session, Message: message, Pane: pane})
	if err != nil {
		return followupOutcome{Outcome: "error", Detail: err.Error()}
	}

	status, respBody, err := postJSON(baseURL(port)+"/followup", body)
	if err != nil {
		return followupOutcome{Outcome: "error", Detail: "transport failure: " + err.Error()}
	}
	if status == 409 {
		return followupOutcome{Outcome: "refused-409", HTTPCode: "409", Detail: refusalReason(respBody)}
	}
	if status != 200 {
		return followupOutcome{Outcome: "error", HTTPCode: fmt.Sprintf("%d", status), Detail: fmt.Sprintf("unexpected-http-%d: %s", status, excerpt(respBody))}
	}

	var resp followupResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return followupOutcome{Outcome: "error", HTTPCode: "200", Detail: "unparseable response body: " + err.Error()}
	}
	messageFile, rolloutPath := resp.MessageFile, resp.RolloutPath
	httpCode := "200"

	sleep(5 * time.Second)
	receipts := countReceipts(messageFile, rolloutPath)
	if receipts == 0 {
		sleep(25 * time.Second)
		receipts = countReceipts(messageFile, rolloutPath)
	}
	if receipts == 0 {
		retryStatus, retryBody, retryErr := postJSON(baseURL(port)+"/followup", body)
		if retryErr != nil {
			httpCode = "transport-error"
		} else {
			httpCode = fmt.Sprintf("%d", retryStatus)
			if retryStatus == 200 {
				var retryResp followupResponse
				if err := json.Unmarshal(retryBody, &retryResp); err == nil {
					messageFile, rolloutPath = retryResp.MessageFile, retryResp.RolloutPath
				}
			}
		}
		sleep(30 * time.Second)
		receipts = countReceipts(messageFile, rolloutPath)
	}

	if receipts > 0 {
		return followupOutcome{Outcome: "delivered", HTTPCode: httpCode, Receipts: receipts, MessageFile: messageFile, RolloutPath: rolloutPath, Detail: "receipt found"}
	}
	return followupOutcome{Outcome: "delivery-unverified", HTTPCode: httpCode, Receipts: 0, MessageFile: messageFile, RolloutPath: rolloutPath, Detail: fmt.Sprintf("final-receipt-grep-zero; retry-http=%s", httpCode)}
}

// countReceipts counts exact-string occurrences of messageFile inside the
// rollout file at rolloutPath (grep -F -c, not a regex: the path contains
// "." and other metacharacters that a regex would match loosely and fake a
// receipt). Either argument empty, or the rollout unreadable, yields 0.
func countReceipts(messageFile, rolloutPath string) int {
	if messageFile == "" || rolloutPath == "" {
		return 0
	}
	data, err := os.ReadFile(rolloutPath)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), messageFile)
}

// refusalReason extracts the server's refusal explanation from a 409 body,
// falling back to a generic message when the body isn't the expected shape.
func refusalReason(body []byte) string {
	var decoded struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Error == "" {
		return "follow-up refused"
	}
	return decoded.Error
}

// excerpt trims and truncates a response body for error detail, matching
// tcm-deliver.sh's `tr '\n' ' ' | cut -c1-240`.
func excerpt(body []byte) string {
	s := strings.ReplaceAll(string(body), "\n", " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}
