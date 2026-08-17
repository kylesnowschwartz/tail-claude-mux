package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withStubSleep swaps sleep for a no-op that counts calls, restoring the
// real time.Sleep afterward so no other test blocks on it.
func withStubSleep(t *testing.T) *int32 {
	t.Helper()
	var calls int32
	prev := sleep
	sleep = func(time.Duration) { atomic.AddInt32(&calls, 1) }
	t.Cleanup(func() { sleep = prev })
	return &calls
}

// waitHandler builds a /wait + /state httptest server from a sequence of
// canned /wait responses (by index, clamped to the last entry once
// exhausted) and a static /state body.
func waitHandler(t *testing.T, waitResponses []waitResponse, waitStatus []int, stateBody string) *httptest.Server {
	t.Helper()
	var waitCalls int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wait":
			i := int(atomic.AddInt32(&waitCalls, 1)) - 1
			status := 200
			if len(waitStatus) > 0 {
				status = waitStatus[min(i, len(waitStatus)-1)]
			}
			w.WriteHeader(status)
			if status != 200 {
				_, _ = w.Write([]byte("boom"))
				return
			}
			if len(waitResponses) == 0 {
				_ = json.NewEncoder(w).Encode(waitResponse{})
				return
			}
			_ = json.NewEncoder(w).Encode(waitResponses[min(i, len(waitResponses)-1)])
		case "/state":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(stateBody))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
}

func TestWait_TerminalStatusesMapToOutcomes(t *testing.T) {
	cases := []struct {
		status  string
		outcome string
	}{
		{"done", "finished"},
		{"error", "error"},
		{"interrupted", "error"},
		{"gone", "session-dead"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			withStubSleep(t)
			withStubTmux(t, func(args ...string) (string, error) { return "%1\n", nil })
			srv := waitHandler(t, []waitResponse{{Session: "s", Status: tc.status, Terminal: true}}, nil, `{"sessions":[]}`)
			defer srv.Close()

			var out bytes.Buffer
			code := runWait([]string{"-session", "s", "-port", portArg(srv), "-minutes", "1"}, &out)
			if code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			var got waitOutcome
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("bad JSON: %v (%s)", err, out.String())
			}
			if got.Outcome != tc.outcome {
				t.Errorf("outcome = %q, want %q (full: %+v)", got.Outcome, tc.outcome, got)
			}
		})
	}
}

func TestWait_WaitingRequiresFiveSecondReconfirm(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) { return "%1\n", nil })
	// First poll: waiting (not timed out) -> triggers 5s reconfirm.
	// Confirm poll: still waiting -> reported.
	srv := waitHandler(t, []waitResponse{
		{Session: "s", Status: "waiting"},
		{Session: "s", Status: "waiting"},
	}, nil, `{"sessions":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-port", portArg(srv), "-minutes", "1"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "waiting" {
		t.Fatalf("outcome = %q, want waiting (full: %+v)", got.Outcome, got)
	}
}

func TestWait_WaitingThenRunningKeepsLooping(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) { return "%1\n", nil })
	// waiting -> reconfirm sees running (not waiting) -> keep looping ->
	// next round: done.
	srv := waitHandler(t, []waitResponse{
		{Session: "s", Status: "waiting"},
		{Session: "s", Status: "running"},
		{Session: "s", Status: "done", Terminal: true},
	}, nil, `{"sessions":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-port", portArg(srv), "-minutes", "1"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "finished" {
		t.Fatalf("outcome = %q, want finished (full: %+v)", got.Outcome, got)
	}
}

func TestWait_BackoffOnBadResponses(t *testing.T) {
	sleepCalls := withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) { return "%1\n", nil })
	// The first three /wait calls 400 (the fallback /state also has no
	// matching pane, so it can't resolve either); the fourth succeeds. Each
	// failed poll must sleep before retrying — the spike's 10.6M hot-loop
	// regression was this branch treating a bad response as "poll again"
	// with no sleep. The loop terminating at exactly 4 polls is the bound.
	srv := waitHandler(t,
		[]waitResponse{{}, {}, {}, {Session: "s", Status: "done", Terminal: true}},
		[]int{400, 400, 400, 200},
		`{"sessions":[]}`,
	)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-pane", "%1", "-port", portArg(srv), "-minutes", "1", "-poll-seconds", "0"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "finished" {
		t.Fatalf("outcome = %q, want finished (full: %+v)", got.Outcome, got)
	}
	if got.Polls != 4 {
		t.Errorf("polls = %d, want 4 (3 failed + 1 success)", got.Polls)
	}
	if atomic.LoadInt32(sleepCalls) < 3 {
		t.Errorf("expected sleep to be called once per failed poll (>=3), got %d calls", *sleepCalls)
	}
}

func TestWait_PaneQueryIsURLEncoded(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) { return "%154\n", nil })
	var sawRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wait" {
			sawRawQuery = r.URL.RawQuery
			// net/http's query parsing already percent-decodes values; the
			// contract this test pins is that the CLIENT percent-encoded
			// the raw "%154" pane id in the first place (RawQuery below),
			// so the server sees the literal pane id back out, not a
			// mis-parsed escape sequence.
			if pane := r.URL.Query().Get("pane"); pane != "%154" {
				t.Fatalf("server-side pane decode = %q; want %%154", pane)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(waitResponse{Session: "s", Status: "done", Terminal: true})
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-pane", "%154", "-port", portArg(srv), "-minutes", "1"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if sawRawQuery == "" {
		t.Fatal("server never saw a /wait request")
	}
	if !strings.Contains(sawRawQuery, "%25154") {
		t.Errorf("raw query = %q; want the client to have percent-encoded the leading %% (pane=%%25154)", sawRawQuery)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "finished" {
		t.Fatalf("outcome = %q, want finished", got.Outcome)
	}
}

func TestWait_PaneGoneYieldsSessionDead(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "display-message" {
			return "", fmt.Errorf("no such pane")
		}
		return "", nil
	})
	srv := waitHandler(t, []waitResponse{{Session: "s", Status: "running"}}, nil, `{"sessions":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-pane", "%999", "-port", portArg(srv), "-minutes", "1"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "session-dead" {
		t.Fatalf("outcome = %q, want session-dead (full: %+v)", got.Outcome, got)
	}
}

func TestWait_SessionGoneYieldsSessionDead(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "has-session" {
			return "", fmt.Errorf("no such session")
		}
		return "%1\n", nil
	})
	srv := waitHandler(t, []waitResponse{{Session: "s", Status: "running"}}, nil, `{"sessions":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-port", portArg(srv), "-minutes", "1"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "session-dead" {
		t.Fatalf("outcome = %q, want session-dead (full: %+v)", got.Outcome, got)
	}
}

func TestWait_MinutesBudgetExceededYieldsTimeout(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) { return "%1\n", nil })
	srv := waitHandler(t, []waitResponse{{Session: "s", Status: "idle"}}, nil, `{"sessions":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-port", portArg(srv), "-minutes", "0", "-poll-seconds", "0"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "timeout" {
		t.Fatalf("outcome = %q, want timeout (full: %+v)", got.Outcome, got)
	}
}

func TestWait_StateFallbackWhenWaitFails(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "capture-pane" {
			return "idle prompt\nready>", nil
		}
		return "%154\n", nil
	})
	stateBody := `{"sessions":[{"name":"s","agents":[{"status":"done","paneId":"%154"}]}]}`
	srv := waitHandler(t, nil, []int{500}, stateBody)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-pane", "%154", "-port", portArg(srv), "-minutes", "1", "-poll-seconds", "0"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got waitOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Outcome != "finished" {
		t.Fatalf("outcome = %q, want finished (full: %+v)", got.Outcome, got)
	}
}

func TestWait_StdoutContractPin(t *testing.T) {
	withStubSleep(t)
	withStubTmux(t, func(args ...string) (string, error) { return "%1\n", nil })
	srv := waitHandler(t, []waitResponse{{Session: "s", Status: "done", Terminal: true}}, nil, `{"sessions":[]}`)
	defer srv.Close()

	var out bytes.Buffer
	code := runWait([]string{"-session", "s", "-port", portArg(srv), "-minutes", "1"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	for _, key := range []string{"outcome", "polls", "state"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing key %q in stdout JSON: %v", key, got)
		}
	}
	if _, ok := got["outcome"].(string); !ok {
		t.Errorf("outcome is not a string: %v", got["outcome"])
	}
}

func portArg(srv *httptest.Server) string {
	return strconv.Itoa(portOf(srv))
}
