package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
)

func TestTellUsesDelegationAndPortEnvironment(t *testing.T) {
	delegationID := "123e4567-e89b-42d3-a456-426614174000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/delegation-message" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body delegationMessageBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Delegation != delegationID || body.From != "delegate" || body.Kind != "note" || body.Text != "review complete" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(delegation.Message{ID: "message-id", From: body.From, Kind: body.Kind, Text: body.Text})
	}))
	defer srv.Close()
	t.Setenv("TCM_DELEGATION_ID", delegationID)
	t.Setenv("TCM_PORT", strconv.Itoa(portOf(srv)))

	var out bytes.Buffer
	if code := runTell([]string{"-message", "review complete"}, &out); code != 0 {
		t.Fatalf("exit code = %d: %s", code, out.String())
	}
	if got, want := out.String(), "{\"outcome\":\"sent\",\"messageId\":\"message-id\"}\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAskPostsThenReturnsAnswer(t *testing.T) {
	delegationID := "123e4567-e89b-42d3-a456-426614174000"
	var postCalls, getCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/delegation-message":
			postCalls++
			var body delegationMessageBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Kind != "ask" || body.Text != "continue?" {
				t.Fatalf("body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(delegation.Message{ID: "ask-id", From: body.From, Kind: body.Kind, Text: body.Text})
		case "/delegation-answer":
			getCalls++
			if r.URL.Query().Get("delegation") != delegationID || r.URL.Query().Get("ask") != "ask-id" || r.URL.Query().Get("timeout") != "60" {
				t.Fatalf("query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(delegationAnswerResponse{
				Answered: true, Message: &delegation.Message{ID: "answer-id", Text: "yes"},
			})
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runAsk([]string{"-delegation", delegationID, "-message", "continue?", "-minutes", "1", "-port", portArg(srv)}, &out)
	if code != 0 || postCalls != 1 || getCalls != 1 {
		t.Fatalf("exit=%d post=%d get=%d output=%s", code, postCalls, getCalls, out.String())
	}
	if got, want := out.String(), "{\"outcome\":\"answered\",\"askId\":\"ask-id\",\"answer\":\"yes\"}\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAskResumesWithoutPosting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/delegation-answer" || r.URL.Query().Get("ask") != "existing-ask" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(delegationAnswerResponse{TimedOut: true})
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runAsk([]string{"-delegation", "delegation-id", "-ask", "existing-ask", "-port", portArg(srv)}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d: %s", code, out.String())
	}
	if got, want := out.String(), "{\"outcome\":\"no-answer-yet\",\"askId\":\"existing-ask\",\"resumeWith\":\"tcm ask -ask existing-ask\"}\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAnswerOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "answered", status: http.StatusOK, want: "{\"outcome\":\"answered\",\"askId\":\"ask-id\"}\n"},
		{name: "already answered", status: http.StatusConflict, want: "{\"outcome\":\"already-answered\"}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body delegationMessageBody
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.From != "orchestrator" || body.Kind != "answer" || body.ReplyTo != "ask-id" || body.Text != "yes" {
					t.Fatalf("body = %#v", body)
				}
				w.WriteHeader(test.status)
				if test.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(delegation.Message{ID: "answer-id"})
				}
			}))
			defer srv.Close()
			var out bytes.Buffer
			code := runAnswer([]string{"-delegation", "delegation-id", "-ask", "ask-id", "-message", "yes", "-port", portArg(srv)}, &out)
			if code != 0 || out.String() != test.want {
				t.Fatalf("exit=%d output=%q want=%q", code, out.String(), test.want)
			}
		})
	}
}

func TestMessagesAndWhoamiPrintServerJSONVerbatim(t *testing.T) {
	delegationID := "123e4567-e89b-42d3-a456-426614174000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("delegation") != delegationID {
			t.Fatalf("delegation = %q", r.URL.Query().Get("delegation"))
		}
		switch r.URL.Path {
		case "/delegation-messages":
			_, _ = w.Write([]byte("{\"messages\":[]}\n"))
		case "/delegation":
			_, _ = w.Write([]byte("{\"delegation_id\":\"" + delegationID + "\"}\n"))
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("TCM_DELEGATION_ID", delegationID)
	t.Setenv("TCM_PORT", portArg(srv))

	var messagesOut bytes.Buffer
	if code := runMessages([]string{"-delegation", delegationID}, &messagesOut); code != 0 || messagesOut.String() != "{\"messages\":[]}\n" {
		t.Fatalf("messages exit=%d output=%q", code, messagesOut.String())
	}
	var whoamiOut bytes.Buffer
	if code := runWhoami(nil, &whoamiOut); code != 0 || whoamiOut.String() != "{\"delegation_id\":\""+delegationID+"\"}\n" {
		t.Fatalf("whoami exit=%d output=%q", code, whoamiOut.String())
	}
}

func TestDelegateTalkbackRequiresDelegation(t *testing.T) {
	t.Setenv("TCM_DELEGATION_ID", "")
	for _, run := range []struct {
		name string
		fn   func([]string, *bytes.Buffer) int
	}{
		{name: "tell", fn: func(args []string, out *bytes.Buffer) int { return runTell(args, out) }},
		{name: "ask", fn: func(args []string, out *bytes.Buffer) int { return runAsk(args, out) }},
		{name: "whoami", fn: func(args []string, out *bytes.Buffer) int { return runWhoami(args, out) }},
	} {
		t.Run(run.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := run.fn(nil, &out); code != 2 {
				t.Fatalf("exit code = %d: %s", code, out.String())
			}
			var outcome talkbackErrorOutcome
			if err := json.Unmarshal(out.Bytes(), &outcome); err != nil || outcome.Outcome != "error" || outcome.Detail == "" {
				t.Fatalf("outcome = %#v error=%v", outcome, err)
			}
		})
	}
}
