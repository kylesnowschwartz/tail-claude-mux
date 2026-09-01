package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
)

type delegationMessageBody struct {
	Delegation string `json:"delegation"`
	From       string `json:"from"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	ReplyTo    string `json:"replyTo,omitempty"`
}

type tellOutcome struct {
	Outcome   string `json:"outcome"`
	MessageID string `json:"messageId"`
}

type talkbackErrorOutcome struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

func runTell(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("tell", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	delegationID := fs.String("delegation", os.Getenv("TCM_DELEGATION_ID"), "delegation id")
	message := fs.String("message", "", "message text")
	messageFile := fs.String("message-file", "", "path to a file containing the message")
	port := fs.Int("port", defaultServerPort(), "tcm server port")
	if err := fs.Parse(args); err != nil {
		return emitTalkbackError(stdout, err.Error(), 2)
	}
	if *delegationID == "" {
		return emitTalkbackError(stdout, "delegation is required: pass -delegation or set TCM_DELEGATION_ID", 2)
	}
	text, err := resolveMessageText(*message, *messageFile)
	if err != nil {
		return emitTalkbackError(stdout, "tell: "+err.Error(), 2)
	}
	stored, code, detail := sendDelegationMessage(*port, delegationMessageBody{
		Delegation: *delegationID, From: "delegate", Kind: "note", Text: text,
	})
	if code != 0 {
		return emitTalkbackError(stdout, detail, code)
	}
	_ = writeJSON(stdout, tellOutcome{Outcome: "sent", MessageID: stored.ID})
	return 0
}

func resolveMessageText(message, messageFile string) (string, error) {
	if message != "" && messageFile != "" {
		return "", errors.New("pass either -message or -message-file, not both")
	}
	if message != "" {
		return message, nil
	}
	if messageFile == "" {
		return "", errors.New("-message or -message-file is required")
	}
	raw, err := os.ReadFile(messageFile)
	if err != nil {
		return "", fmt.Errorf("reading -message-file: %w", err)
	}
	if len(raw) == 0 {
		return "", errors.New("message file is empty")
	}
	return string(raw), nil
}

func sendDelegationMessage(port int, body delegationMessageBody) (delegation.Message, int, string) {
	raw, err := json.Marshal(body)
	if err != nil {
		return delegation.Message{}, 1, err.Error()
	}
	status, responseBody, err := postJSON(baseURL(port)+"/delegation-message", raw)
	if err != nil {
		return delegation.Message{}, 1, "transport failure: " + err.Error()
	}
	if status != 200 {
		return delegation.Message{}, 1, fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(responseBody)))
	}
	var stored delegation.Message
	if err := json.Unmarshal(responseBody, &stored); err != nil {
		return delegation.Message{}, 1, "unparseable response body: " + err.Error()
	}
	return stored, 0, ""
}

func emitTalkbackError(stdout io.Writer, detail string, code int) int {
	_ = writeJSON(stdout, talkbackErrorOutcome{Outcome: "error", Detail: detail})
	return code
}
