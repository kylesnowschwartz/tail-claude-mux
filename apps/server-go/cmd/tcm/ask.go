package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
)

type askOutcome struct {
	Outcome string `json:"outcome"`
	AskID   string `json:"askId"`
	Answer  string `json:"answer,omitempty"`
	// ResumeWith is the literal command that resumes polling this ask after a
	// no-answer-yet timeout, so the delegate needs no other documentation.
	ResumeWith string `json:"resumeWith,omitempty"`
}

type delegationAnswerResponse struct {
	Answered bool                `json:"answered"`
	TimedOut bool                `json:"timedOut"`
	Message  *delegation.Message `json:"message"`
}

func runAsk(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	delegationID := fs.String("delegation", os.Getenv("TCM_DELEGATION_ID"), "delegation id")
	message := fs.String("message", "", "question text")
	messageFile := fs.String("message-file", "", "path to a file containing the question")
	askID := fs.String("ask", "", "existing ask id to resume polling")
	minutes := fs.Int("minutes", 10, "answer wait budget in minutes")
	port := fs.Int("port", defaultServerPort(), "tcm server port")
	if err := fs.Parse(args); err != nil {
		return emitTalkbackError(stdout, err.Error(), 2)
	}
	if *delegationID == "" {
		return emitTalkbackError(stdout, "delegation is required: pass -delegation or set TCM_DELEGATION_ID", 2)
	}
	if *minutes < 0 {
		return emitTalkbackError(stdout, "ask: -minutes must not be negative", 2)
	}
	if *askID != "" && (*message != "" || *messageFile != "") {
		return emitTalkbackError(stdout, "ask: -ask cannot be combined with -message or -message-file", 2)
	}
	currentAskID := *askID
	if currentAskID == "" {
		text, err := resolveMessageText(*message, *messageFile)
		if err != nil {
			return emitTalkbackError(stdout, "ask: "+err.Error(), 2)
		}
		stored, code, detail := sendDelegationMessage(*port, delegationMessageBody{
			Delegation: *delegationID, From: "delegate", Kind: "ask", Text: text,
		})
		if code != 0 {
			return emitTalkbackError(stdout, detail, code)
		}
		currentAskID = stored.ID
	}

	answer, answered, code, detail := pollDelegationAnswer(*port, *delegationID, currentAskID, *minutes)
	if code != 0 {
		return emitTalkbackError(stdout, detail, code)
	}
	if !answered {
		_ = writeJSON(stdout, askOutcome{
			Outcome: "no-answer-yet", AskID: currentAskID,
			ResumeWith: fmt.Sprintf("tcm ask -ask %s", currentAskID),
		})
		return 0
	}
	_ = writeJSON(stdout, askOutcome{Outcome: "answered", AskID: currentAskID, Answer: answer})
	return 0
}

func pollDelegationAnswer(port int, delegationID, askID string, minutes int) (string, bool, int, string) {
	values := url.Values{}
	values.Set("delegation", delegationID)
	values.Set("ask", askID)
	seconds := minutes * 60
	values.Set("timeout", strconv.Itoa(seconds))
	clientTimeout := time.Duration(seconds)*time.Second + waitClientGrace
	if clientTimeout < time.Second+waitClientGrace {
		clientTimeout = time.Second + waitClientGrace
	}
	client := &http.Client{Timeout: clientTimeout}
	status, body, err := getURLWithClient(client, baseURL(port)+"/delegation-answer?"+values.Encode())
	if err != nil {
		return "", false, 1, "transport failure: " + err.Error()
	}
	if status != http.StatusOK {
		return "", false, 1, fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	var response delegationAnswerResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return "", false, 1, "unparseable response body: " + err.Error()
	}
	if !response.Answered {
		return "", false, 0, ""
	}
	if response.Message == nil {
		return "", false, 1, "answered response is missing message"
	}
	return response.Message.Text, true, 0, ""
}
