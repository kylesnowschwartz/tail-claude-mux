package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestResult_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/result" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"session": "delegate-1", "status": "done", "finalMessage": "RESULT: spike-ok",
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runResult([]string{"-session", "delegate-1", "-pane", "%154", "-port", strconv.Itoa(portOf(srv))}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output: %s", code, out.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got["finalMessage"] != "RESULT: spike-ok" {
		t.Errorf("unexpected output: %v", got)
	}
}

// TestResult_PaneQueryEncoding pins that a pane id starting with "%" must
// arrive at the server decoded to the literal value, not mangled by
// percent-escaping.
func TestResult_PaneQueryEncoding(t *testing.T) {
	var gotPane string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPane = r.URL.Query().Get("pane")
		json.NewEncoder(w).Encode(map[string]any{"session": "s", "status": "done"})
	}))
	defer srv.Close()

	var out bytes.Buffer
	runResult([]string{"-session", "delegate-1", "-pane", "%154", "-port", strconv.Itoa(portOf(srv))}, &out)

	if gotPane != "%154" {
		t.Errorf("server saw pane=%q, want %q", gotPane, "%154")
	}
}

func TestResult_FlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing address"},
		{name: "session without pane", args: []string{"-session", "work"}},
		{name: "pane without session", args: []string{"-pane", "%7"}},
		{name: "delegation and thread", args: []string{"-delegation", "delegation-id", "-thread", "thread-id"}},
		{name: "delegation and session pane", args: []string{"-delegation", "delegation-id", "-session", "work", "-pane", "%7"}},
		{name: "thread and session pane", args: []string{"-thread", "thread-id", "-session", "work", "-pane", "%7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := runResult(tt.args, &out); code != 2 {
				t.Fatalf("exit code = %d, want 2; output: %s", code, out.String())
			}
		})
	}
}

func TestResult_AddressFlagQueries(t *testing.T) {
	tests := []struct {
		name, flagName, address, queryName string
	}{
		{name: "delegation", flagName: "-delegation", address: "delegation%id", queryName: "delegation"},
		{name: "thread", flagName: "-thread", address: "thread/id", queryName: "thread"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get(tt.queryName); got != tt.address {
					t.Fatalf("server saw %s=%q, want %q", tt.queryName, got, tt.address)
				}
				json.NewEncoder(w).Encode(map[string]any{"threadId": "thread-id", "status": "done"})
			}))
			defer srv.Close()

			var out bytes.Buffer
			if code := runResult([]string{tt.flagName, tt.address, "-port", strconv.Itoa(portOf(srv))}, &out); code != 0 {
				t.Fatalf("exit code = %d; output: %s", code, out.String())
			}
		})
	}
}

func TestResult_NonHTTP200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"session not found"}`)
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runResult([]string{"-session", "delegate-1", "-pane", "%154", "-port", strconv.Itoa(portOf(srv))}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got resultErrorOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "error" {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

func TestResult_UnparseableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runResult([]string{"-session", "delegate-1", "-pane", "%154", "-port", strconv.Itoa(portOf(srv))}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got resultErrorOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "error" {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

func TestResult_ConnectionRefused(t *testing.T) {
	var out bytes.Buffer
	code := runResult([]string{"-session", "delegate-1", "-pane", "%154", "-port", "1"}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got resultErrorOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "error" {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

// TestResult_ContractPin pins the exact JSON shape on failure so downstream
// consumers can't drift silently.
func TestResult_ContractPin(t *testing.T) {
	var out bytes.Buffer
	runResult([]string{"-session", "delegate-1", "-pane", "%154", "-port", "1"}, &out)

	var raw map[string]any
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	for _, key := range []string{"outcome", "detail"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in output: %v", key, raw)
		}
	}
}
