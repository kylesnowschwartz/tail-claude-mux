package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// withStubTmux swaps tmuxOutput for the duration of a test and restores it
// afterward.
func withStubTmux(t *testing.T, fn func(args ...string) (string, error)) {
	t.Helper()
	prev := tmuxOutput
	tmuxOutput = fn
	t.Cleanup(func() { tmuxOutput = prev })
}

func portOf(srv *httptest.Server) int {
	url := strings.TrimPrefix(srv.URL, "http://")
	_, portStr, _ := strings.Cut(url, ":")
	p, _ := strconv.Atoi(portStr)
	return p
}

func TestDelegate_HappyPath(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "display-message" && args[1] == "-p" && len(args) == 3 && args[2] == "#{session_name}" {
			return "owner-session\n", nil
		}
		// pane liveness probe
		return "%154\n", nil
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/spawn-agent" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		cmd, ok := body["command"].([]any)
		if !ok || len(cmd) == 0 || cmd[len(cmd)-1] != "--" {
			t.Fatalf("spawn body command must end with \"--\": %v", cmd)
		}
		json.NewEncoder(w).Encode(map[string]string{
			"sessionName":  "delegate-1",
			"paneId":       "%154",
			"windowId":     "@39",
			"delegationId": "123e4567-e89b-42d3-a456-426614174000",
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runDelegate([]string{
		"-dir", "/tmp",
		"-name", "spike-ok",
		"-brief", "do the thing",
		"-port", strconv.Itoa(portOf(srv)),
	}, &out)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; output: %s", code, out.String())
	}
	var got delegateOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "spawned" || got.SessionName != "delegate-1" || got.PaneID != "%154" ||
		got.WindowID != "@39" || got.OwnerSession != "owner-session" ||
		got.DelegationID != "123e4567-e89b-42d3-a456-426614174000" || !got.Alive {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

func TestDelegate_DeadPaneAfterSpawnReportsSpawnFailed(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "display-message" && args[1] == "-p" && len(args) == 3 && args[2] == "#{session_name}" {
			return "owner-session\n", nil
		}
		// pane liveness probe: tmux echoes an empty line for a dead pane.
		return "\n", nil
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"sessionName":  "delegate-1",
			"paneId":       "%154",
			"windowId":     "@39",
			"delegationId": "123e4567-e89b-42d3-a456-426614174000",
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runDelegate([]string{
		"-dir", "/tmp",
		"-name", "spike-dead",
		"-brief", "do the thing",
		"-port", strconv.Itoa(portOf(srv)),
	}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; output: %s", code, out.String())
	}
	var got delegateOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "spawn-failed" || got.Detail != "pane-died-after-spawn" {
		t.Errorf("unexpected outcome: %+v", got)
	}
	if got.SessionName != "delegate-1" || got.PaneID != "%154" || got.WindowID != "@39" {
		t.Errorf("diagnostic fields must survive the dead-pane path: %+v", got)
	}
	if got.Alive {
		t.Errorf("alive = true, want false")
	}
}

func TestDelegate_NoOwnerWithoutTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	withStubTmux(t, func(args ...string) (string, error) {
		t.Fatalf("tmux must not be called without $TMUX set; got args=%v", args)
		return "", nil
	})

	var out bytes.Buffer
	code := runDelegate([]string{
		"-dir", "/tmp",
		"-name", "spike-ok",
		"-brief", "do the thing",
	}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got delegateOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "spawn-failed" || got.Detail != "no-tmux-owner" {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

func TestDelegate_NonHTTP200(t *testing.T) {
	t.Setenv("TMUX", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"dir is required"}`)
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runDelegate([]string{
		"-dir", "/tmp",
		"-name", "spike-ok",
		"-brief", "do the thing",
		"-owner", "owner-session",
		"-port", strconv.Itoa(portOf(srv)),
	}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got delegateOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "spawn-failed" || !strings.Contains(got.Detail, "HTTP 400") {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

func TestDelegate_ConnectionRefused(t *testing.T) {
	t.Setenv("TMUX", "")

	var out bytes.Buffer
	code := runDelegate([]string{
		"-dir", "/tmp",
		"-name", "spike-ok",
		"-brief", "do the thing",
		"-owner", "owner-session",
		"-port", "1", // nothing listens on port 1
	}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got delegateOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "spawn-failed" {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

func TestDelegate_UnparseableBody(t *testing.T) {
	t.Setenv("TMUX", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runDelegate([]string{
		"-dir", "/tmp",
		"-name", "spike-ok",
		"-brief", "do the thing",
		"-owner", "owner-session",
		"-port", strconv.Itoa(portOf(srv)),
	}, &out)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var got delegateOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, out.String())
	}
	if got.Outcome != "spawn-failed" {
		t.Errorf("unexpected outcome: %+v", got)
	}
}

// TestDelegate_ContractPin pins the exact JSON keys tcm delegate emits so
// downstream consumers (skills, wait, followup) can't drift silently.
func TestDelegate_ContractPin(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0")
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "display-message" && len(args) == 3 {
			return "owner-session\n", nil
		}
		return "%154\n", nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"sessionName": "delegate-1", "paneId": "%154", "windowId": "@39",
			"delegationId": "123e4567-e89b-42d3-a456-426614174000",
		})
	}))
	defer srv.Close()

	var out bytes.Buffer
	runDelegate([]string{"-dir", "/tmp", "-name", "n", "-brief", "b", "-port", strconv.Itoa(portOf(srv))}, &out)

	var raw map[string]any
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	for _, key := range []string{"outcome", "sessionName", "paneId", "windowId", "ownerSession", "delegationId", "alive"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in output: %v", key, raw)
		}
	}
}

// tmux display-message exits 0 with an empty line for an unknown -t target
// on tmux 3.7b, so exit status alone cannot prove a pane is alive.
func TestPaneAlive_EmptyOutputWithZeroExitIsDead(t *testing.T) {
	restore := tmuxOutput
	defer func() { tmuxOutput = restore }()
	tmuxOutput = func(args ...string) (string, error) { return "\n", nil }
	if paneAlive("%214") {
		t.Fatal("paneAlive must be false when tmux echoes an empty line for the target")
	}
	tmuxOutput = func(args ...string) (string, error) { return "%214\n", nil }
	if !paneAlive("%214") {
		t.Fatal("paneAlive must be true when tmux echoes the pane id back")
	}
}
