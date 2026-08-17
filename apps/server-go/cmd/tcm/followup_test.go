package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
)

// writeTempFile writes content to a fresh temp file and returns its path.
func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "tcm-followup-*")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return f.Name()
}

func TestFollowup_DeliveredOnFirstGrep(t *testing.T) {
	withStubSleep(t)
	messageFile := "/tmp/followup-msg-123.txt"
	rollout := writeTempFile(t, "some rollout content mentioning "+messageFile+" as a receipt\n")

	var postCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&postCalls, 1)
		json.NewEncoder(w).Encode(followupResponse{MessageFile: messageFile, RolloutPath: rollout})
	}))
	defer srv.Close()

	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-pane", "%1", "-message-file", sourceFile, "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got followupOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "delivered" {
		t.Fatalf("outcome = %q, want delivered (full: %+v)", got.Outcome, got)
	}
	if got.Receipts == 0 {
		t.Errorf("receipts = 0, want > 0")
	}
	if atomic.LoadInt32(&postCalls) != 1 {
		t.Errorf("post calls = %d, want 1 (no retry needed)", postCalls)
	}
}

func TestFollowup_DeliveredOnPostRetryGrep(t *testing.T) {
	withStubSleep(t)
	messageFile := "/tmp/followup-msg-456.txt"

	// Rollout starts empty (no receipt); the retry handler rewrites it with
	// the receipt in place before the final grep.
	rollout := writeTempFile(t, "no receipt yet\n")

	var postCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&postCalls, 1)
		if n == 2 {
			if err := os.WriteFile(rollout, []byte("now mentions "+messageFile+"\n"), 0o644); err != nil {
				t.Fatalf("rewrite rollout: %v", err)
			}
		}
		json.NewEncoder(w).Encode(followupResponse{MessageFile: messageFile, RolloutPath: rollout})
	}))
	defer srv.Close()

	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got followupOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "delivered" {
		t.Fatalf("outcome = %q, want delivered (full: %+v)", got.Outcome, got)
	}
	if atomic.LoadInt32(&postCalls) != 2 {
		t.Errorf("post calls = %d, want 2 (initial + retry)", postCalls)
	}
}

func TestFollowup_RefusedOnFirstPost409Only(t *testing.T) {
	withStubSleep(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(map[string]string{"error": "delegate is busy"})
	}))
	defer srv.Close()

	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got followupOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "refused-409" {
		t.Fatalf("outcome = %q, want refused-409 (full: %+v)", got.Outcome, got)
	}
	if got.HTTPCode != "409" {
		t.Errorf("httpCode = %q, want 409", got.HTTPCode)
	}
}

func TestFollowup_RetryConflictIsNotRefusal(t *testing.T) {
	withStubSleep(t)
	messageFile := "/tmp/followup-msg-789.txt"
	rollout := writeTempFile(t, "no receipt anywhere\n")

	var postCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&postCalls, 1)
		if n == 1 {
			json.NewEncoder(w).Encode(followupResponse{MessageFile: messageFile, RolloutPath: rollout})
			return
		}
		// The retry POST 409s — the first POST already respawned the pane,
		// so this conflict is not a refusal; only the final grep decides.
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(map[string]string{"error": "already resumed"})
	}))
	defer srv.Close()

	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got followupOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "delivery-unverified" {
		t.Fatalf("outcome = %q, want delivery-unverified (a retry 409 must not be reported as refused-409); full: %+v", got.Outcome, got)
	}
	if got.HTTPCode != "409" {
		t.Errorf("httpCode = %q, want 409 (from the retry, not treated as refusal)", got.HTTPCode)
	}
}

func TestFollowup_UnverifiedAfterFullLadder(t *testing.T) {
	withStubSleep(t)
	messageFile := "/tmp/followup-msg-000.txt"
	rollout := writeTempFile(t, "receipt never lands\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(followupResponse{MessageFile: messageFile, RolloutPath: rollout})
	}))
	defer srv.Close()

	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got followupOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "delivery-unverified" {
		t.Fatalf("outcome = %q, want delivery-unverified (full: %+v)", got.Outcome, got)
	}
	if got.Receipts != 0 {
		t.Errorf("receipts = %d, want 0", got.Receipts)
	}
}

func TestFollowup_TransportErrorYieldsError(t *testing.T) {
	withStubSleep(t)
	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", "1"}, &out)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (error outcome is a CLI failure)", code)
	}
	var got followupOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "error" {
		t.Fatalf("outcome = %q, want error (full: %+v)", got.Outcome, got)
	}
}

func TestFollowup_DeliveredYieldsExitZero(t *testing.T) {
	withStubSleep(t)
	messageFile := "/tmp/followup-msg-exit0.txt"
	rollout := writeTempFile(t, "mentions "+messageFile+"\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(followupResponse{MessageFile: messageFile, RolloutPath: rollout})
	}))
	defer srv.Close()

	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	code := runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestFollowup_ContractPin pins the exact JSON keys tcm followup emits.
func TestFollowup_ContractPin(t *testing.T) {
	withStubSleep(t)
	sourceFile := writeTempFile(t, "hello")
	var out bytes.Buffer
	runFollowup([]string{"-session", "s", "-message-file", sourceFile, "-port", "1"}, &out)

	var raw map[string]any
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	for _, key := range []string{"outcome", "httpCode", "receipts", "messageFile", "rolloutPath", "detail"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in output: %v", key, raw)
		}
	}
}
