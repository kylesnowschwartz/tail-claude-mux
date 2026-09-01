package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

type answerOutcome struct {
	Outcome string `json:"outcome"`
	AskID   string `json:"askId,omitempty"`
}

func runAnswer(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	delegationID := fs.String("delegation", "", "delegation id")
	askID := fs.String("ask", "", "ask message id")
	message := fs.String("message", "", "answer text")
	messageFile := fs.String("message-file", "", "path to a file containing the answer")
	port := fs.Int("port", defaultServerPort(), "tcm server port")
	if err := fs.Parse(args); err != nil {
		return emitTalkbackError(stdout, err.Error(), 2)
	}
	if *delegationID == "" || *askID == "" {
		return emitTalkbackError(stdout, "answer: -delegation and -ask are required", 2)
	}
	text, err := resolveMessageText(*message, *messageFile)
	if err != nil {
		return emitTalkbackError(stdout, "answer: "+err.Error(), 2)
	}
	raw, err := json.Marshal(delegationMessageBody{
		Delegation: *delegationID, From: "orchestrator", Kind: "answer", Text: text, ReplyTo: *askID,
	})
	if err != nil {
		return emitTalkbackError(stdout, err.Error(), 1)
	}
	status, responseBody, err := postJSON(baseURL(*port)+"/delegation-message", raw)
	if err != nil {
		return emitTalkbackError(stdout, "transport failure: "+err.Error(), 1)
	}
	if status == 409 {
		_ = writeJSON(stdout, answerOutcome{Outcome: "already-answered"})
		return 0
	}
	if status != 200 {
		return emitTalkbackError(stdout, fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(responseBody))), 1)
	}
	_ = writeJSON(stdout, answerOutcome{Outcome: "answered", AskID: *askID})
	return 0
}
