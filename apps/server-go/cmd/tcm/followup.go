package main

import (
	"encoding/json"
	"errors"
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
	AskID       string `json:"askId,omitempty"`
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
	Delegation string `json:"delegation,omitempty"`
	Session    string `json:"session,omitempty"`
	Message    string `json:"message"`
	Pane       string `json:"pane,omitempty"`
	AskRouting string `json:"askRouting,omitempty"`
}

// followupResponse is the subset of POST /followup's 200 response tcm
// followup reads.
type followupResponse struct {
	MessageFile string `json:"messageFile"`
	RolloutPath string `json:"rolloutPath"`
	Mode        string `json:"mode"`
	AskID       string `json:"askId"`
}

// runFollowup sends a reply to a delegate. An answered ask returns
// immediately; a resumed delegate is verified through its rollout receipt.
// It returns 1 for usage or transport failures and 0 for delegation outcomes.
func runFollowup(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("followup", flag.ContinueOnError)
	delegationID := fs.String("delegation", "", "delegation id")
	session := fs.String("session", "", "TCM session name to deliver the follow-up to")
	pane := fs.String("pane", "", "pane id (omit to let the server resolve it)")
	messageFile := fs.String("message-file", "", "path to the follow-up message file")
	port := fs.Int("port", 7391, "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateFollowupAddress(*delegationID, *session, *pane); err != nil {
		fmt.Fprintln(os.Stderr, "followup:", err)
		return 2
	}
	if *messageFile == "" {
		fmt.Fprintln(os.Stderr, "followup: -message-file is required")
		return 2
	}

	source, err := os.ReadFile(*messageFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "followup: reading -message-file:", err)
		return 2
	}

	outcome := deliverFollowup(*port, *delegationID, *session, *pane, string(source))
	if outcome.Outcome == "answered" {
		_ = writeJSON(stdout, struct {
			Outcome  string `json:"outcome"`
			AskID    string `json:"askId"`
			HTTPCode string `json:"httpCode"`
		}{Outcome: outcome.Outcome, AskID: outcome.AskID, HTTPCode: outcome.HTTPCode})
	} else {
		_ = writeJSON(stdout, outcome)
	}
	if outcome.Outcome == "error" {
		return 1
	}
	return 0
}

func validateFollowupAddress(delegationID, session, pane string) error {
	if (delegationID != "" && session == "" && pane == "") || (delegationID == "" && session != "") {
		return nil
	}
	return errors.New("exactly one address is required: -delegation or -session with optional -pane")
}

// deliverFollowup runs the POST-then-verify ladder: an initial follow-up
// POST, receipt greps at 5s and 25s after delivery, one retry POST, then a
// final grep 30s later. A 409 on the first POST is a refusal or an ambiguous
// set of asks. A 409 on the retry is not:
// the first POST already respawned the pane, so that conflict means the
// resume took; only the final receipt grep classifies the retry path. The
// ladder trades latency for certainty: a delivered message can take up to
// a minute to confirm because the server only writes a rollout receipt
// once the delegate's turn actually reads the resumed prompt.
func deliverFollowup(port int, delegationID, session, pane, message string) followupOutcome {
	body, err := json.Marshal(followupBody{Delegation: delegationID, Session: session, Message: message, Pane: pane})
	if err != nil {
		return followupOutcome{Outcome: "error", Detail: err.Error()}
	}

	status, respBody, err := postJSON(baseURL(port)+"/followup", body)
	if err != nil {
		return followupOutcome{Outcome: "error", Detail: "transport failure: " + err.Error()}
	}
	if status == 409 {
		code, detail := refusal(respBody)
		if code == "ambiguous-ask" || strings.HasPrefix(detail, "ambiguous-ask:") {
			return followupOutcome{Outcome: "ambiguous-ask", HTTPCode: "409", Detail: detail}
		}
		return followupOutcome{Outcome: "refused-409", HTTPCode: "409", Detail: detail}
	}
	if status != 200 {
		return followupOutcome{Outcome: "error", HTTPCode: fmt.Sprintf("%d", status), Detail: fmt.Sprintf("unexpected-http-%d: %s", status, excerpt(respBody))}
	}

	var resp followupResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return followupOutcome{Outcome: "error", HTTPCode: "200", Detail: "unparseable response body: " + err.Error()}
	}
	if resp.Mode == "answer" {
		return followupOutcome{Outcome: "answered", AskID: resp.AskID, HTTPCode: "200"}
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
		retryRequest, marshalErr := json.Marshal(followupBody{
			Delegation: delegationID, Session: session, Message: message, Pane: pane, AskRouting: "off",
		})
		if marshalErr != nil {
			return followupOutcome{Outcome: "error", HTTPCode: httpCode, Detail: marshalErr.Error()}
		}
		retryStatus, retryBody, retryErr := postJSON(baseURL(port)+"/followup", retryRequest)
		if retryErr != nil {
			httpCode = "transport-error"
		} else {
			httpCode = fmt.Sprintf("%d", retryStatus)
			if retryStatus == 200 {
				var retryResp followupResponse
				if err := json.Unmarshal(retryBody, &retryResp); err == nil {
					if retryResp.Mode == "answer" {
						return followupOutcome{Outcome: "answered", AskID: retryResp.AskID, HTTPCode: "200"}
					}
					if retryResp.MessageFile != "" {
						messageFile = retryResp.MessageFile
					}
					if retryResp.RolloutPath != "" {
						rolloutPath = retryResp.RolloutPath
					}
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

// refusal extracts the server's stable code and explanation from a 409 body.
func refusal(body []byte) (string, string) {
	var decoded struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Error == "" {
		return "", "follow-up refused"
	}
	return decoded.Code, decoded.Error
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
