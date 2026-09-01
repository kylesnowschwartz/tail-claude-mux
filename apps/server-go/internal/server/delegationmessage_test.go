package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
)

func TestDelegationMessageEndpoints(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Delegations: store, waitPollInterval: 5 * time.Millisecond}
	handler := s.Handler()

	tests := []struct {
		name string
		body string
		code int
		text string
	}{
		{name: "unknown delegation", body: `{"delegation":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","from":"delegate","kind":"note","text":"hello"}`, code: http.StatusNotFound, text: "delegation not found"},
		{name: "empty text", body: `{"delegation":"` + record.DelegationID + `","from":"delegate","kind":"note","text":""}`, code: http.StatusBadRequest, text: "message text is empty"},
		{name: "invalid combination", body: `{"delegation":"` + record.DelegationID + `","from":"orchestrator","kind":"note","text":"hello"}`, code: http.StatusBadRequest, text: "invalid message from/kind combination"},
		{name: "answer missing ask", body: `{"delegation":"` + record.DelegationID + `","from":"orchestrator","kind":"answer","text":"yes","replyTo":"missing"}`, code: http.StatusBadRequest, text: "replyTo must name an existing ask"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/delegation-message", strings.NewReader(test.body)))
			if response.Code != test.code || !strings.Contains(response.Body.String(), test.text) {
				t.Fatalf("response = %d %q, want %d containing %q", response.Code, response.Body.String(), test.code, test.text)
			}
		})
	}

	ask := postDelegationMessage(t, handler, record.DelegationID, "delegate", "ask", "continue?", "")
	answer := postDelegationMessage(t, handler, record.DelegationID, "orchestrator", "answer", "yes", ask.ID)
	if answer.ReplyTo != ask.ID {
		t.Fatalf("answer ReplyTo = %q, want %q", answer.ReplyTo, ask.ID)
	}
	double := httptest.NewRecorder()
	doubleBody := delegationMessageRequest{Delegation: record.DelegationID, From: "orchestrator", Kind: "answer", Text: "again", ReplyTo: ask.ID}
	raw, err := json.Marshal(doubleBody)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(double, httptest.NewRequest(http.MethodPost, "/delegation-message", bytes.NewReader(raw)))
	if double.Code != http.StatusConflict || !strings.Contains(double.Body.String(), "ask is already answered") {
		t.Fatalf("double answer = %d %q", double.Code, double.Body.String())
	}

	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/delegation-messages?delegation="+record.DelegationID, nil))
	var messages delegationMessagesResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &messages); err != nil {
		t.Fatal(err)
	}
	if listed.Code != http.StatusOK || len(messages.Messages) != 2 {
		t.Fatalf("messages response = %d %#v", listed.Code, messages)
	}

	answered := httptest.NewRecorder()
	path := "/delegation-answer?delegation=" + record.DelegationID + "&ask=" + url.QueryEscape(ask.ID) + "&timeout=1"
	handler.ServeHTTP(answered, httptest.NewRequest(http.MethodGet, path, nil))
	var answerBody delegationAnswerResponse
	if err := json.Unmarshal(answered.Body.Bytes(), &answerBody); err != nil {
		t.Fatal(err)
	}
	if answered.Code != http.StatusOK || !answerBody.Answered || answerBody.Message == nil || answerBody.Message.ID != answer.ID {
		t.Fatalf("answer response = %d %#v", answered.Code, answerBody)
	}
}

func TestDelegationAnswerLongPoll(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := store.AppendMessage(record.DelegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Delegations: store, waitPollInterval: 5 * time.Millisecond}
	done := make(chan delegationAnswerResponse, 1)
	go func() {
		response := httptest.NewRecorder()
		path := "/delegation-answer?delegation=" + record.DelegationID + "&ask=" + ask.ID + "&timeout=1"
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		var body delegationAnswerResponse
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		done <- body
	}()
	answer, err := store.AppendMessage(record.DelegationID, delegation.Message{From: "orchestrator", Kind: "answer", Text: "yes", ReplyTo: ask.ID})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-done:
		if !body.Answered || body.Message == nil || body.Message.ID != answer.ID {
			t.Fatalf("response = %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("answer poll did not wake")
	}
}

func TestDelegationAnswerTimeout(t *testing.T) {
	store, err := delegation.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(delegation.CreateParams{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := store.AppendMessage(record.DelegationID, delegation.Message{From: "delegate", Kind: "ask", Text: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Delegations: store, waitPollInterval: 5 * time.Millisecond}
	response := httptest.NewRecorder()
	path := "/delegation-answer?delegation=" + record.DelegationID + "&ask=" + ask.ID + "&timeout=0.01"
	s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	var body delegationAnswerResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Answered || !body.TimedOut || body.Message != nil {
		t.Fatalf("response = %d %#v", response.Code, body)
	}
}

func postDelegationMessage(t *testing.T, handler http.Handler, delegationID, from, kind, text, replyTo string) delegation.Message {
	t.Helper()
	raw, err := json.Marshal(delegationMessageRequest{Delegation: delegationID, From: from, Kind: kind, Text: text, ReplyTo: replyTo})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/delegation-message", bytes.NewReader(raw)))
	if response.Code != http.StatusOK {
		t.Fatalf("POST /delegation-message = %d: %s", response.Code, response.Body.String())
	}
	var message delegation.Message
	if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	return message
}
