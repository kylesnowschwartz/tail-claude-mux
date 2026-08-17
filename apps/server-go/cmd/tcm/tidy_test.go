package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// tidyResultHandler answers GET /result with statusOf[pane], defaulting to
// "done" for unlisted panes.
func tidyResultHandler(t *testing.T, statusOf map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/result" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		pane := r.URL.Query().Get("pane")
		status, ok := statusOf[pane]
		if !ok {
			status = "done"
		}
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
}

// assertOnlyKillWindow fails the test if any tmux call other than
// kill-window (or the read-only list-panes/display-message/has-session
// probes) reached the seam — kill-window is the only destructive verb tcm
// tidy is allowed to pass to tmux.
func assertOnlyKillWindow(t *testing.T) func(args ...string) (string, error) {
	t.Helper()
	return func(args ...string) (string, error) {
		switch args[0] {
		case "kill-window":
			if len(args) != 3 || args[1] != "-t" {
				t.Fatalf("unexpected kill-window args: %v", args)
			}
			return "", nil
		case "list-panes":
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@2\t50\ttest-session\t%2\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@3\t10\ttest-session\t%3\tsh -c codex\n" +
				"@4\t5\ttest-session\t%4\t-zsh\n", nil
		case "display-message":
			return "test-session\n", nil
		case "has-session":
			return "", nil
		default:
			t.Fatalf("unexpected tmux verb: %v", args)
			return "", nil
		}
	}
}

func TestTidy_SignatureMatchesOnlyDelegatePanes(t *testing.T) {
	withStubTmux(t, assertOnlyKillWindow(t))
	srv := tidyResultHandler(t, nil)
	defer srv.Close()

	var out bytes.Buffer
	code := runTidy([]string{"-dry-run", "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got tidyOutcome
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, out.String())
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %v, want exactly the 2 codex+tcm-delegate panes", got.Candidates)
	}
	for _, c := range got.Candidates {
		if c.WindowID != "@1" && c.WindowID != "@2" {
			t.Errorf("unexpected candidate window: %+v", c)
		}
	}
}

func TestTidy_DefaultScopeIsCurrentSession(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		switch args[0] {
		case "list-panes":
			return "@1\t100\tother-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@2\t50\ttest-session\t%2\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	srv := tidyResultHandler(t, nil)
	defer srv.Close()

	var out bytes.Buffer
	runTidy([]string{"-dry-run", "-port", strconv.Itoa(portOf(srv))}, &out)
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 1 || got.Candidates[0].WindowID != "@2" {
		t.Fatalf("default scope should exclude other-session's window: %+v", got.Candidates)
	}
}

func TestTidy_AllSessionsWidensScope(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "list-panes" {
			return "@1\t100\tother-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@2\t50\ttest-session\t%2\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		}
		return "", nil
	})
	srv := tidyResultHandler(t, nil)
	defer srv.Close()

	var out bytes.Buffer
	runTidy([]string{"-dry-run", "-all-sessions", "-port", strconv.Itoa(portOf(srv))}, &out)
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 2 {
		t.Fatalf("-all-sessions should include both windows: %+v", got.Candidates)
	}
}

func TestTidy_NonTerminalStatusExcludedWithoutAll(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		switch args[0] {
		case "list-panes":
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	srv := tidyResultHandler(t, map[string]string{"%1": "running"})
	defer srv.Close()

	var out bytes.Buffer
	code := runTidy([]string{"-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 1 || got.Candidates[0].Action != "skip" {
		t.Fatalf("non-terminal status must be skipped without -all: %+v", got.Candidates)
	}
	if got.Closed != 0 {
		t.Errorf("closed = %d, want 0", got.Closed)
	}

	// -all forces the close even though the status is non-terminal.
	var out2 bytes.Buffer
	runTidy([]string{"-all", "-port", strconv.Itoa(portOf(srv))}, &out2)
	var got2 tidyOutcome
	_ = json.Unmarshal(out2.Bytes(), &got2)
	if got2.Candidates[0].Action != "closed" {
		t.Errorf("with -all: action = %q, want closed", got2.Candidates[0].Action)
	}
}

func TestTidy_KeepSparesMostRecent(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		switch args[0] {
		case "list-panes":
			return "@1\t200\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@2\t100\ttest-session\t%2\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@3\t50\ttest-session\t%3\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	srv := tidyResultHandler(t, nil) // all "done" -> closeable
	defer srv.Close()

	var out bytes.Buffer
	runTidy([]string{"-keep", "1", "-port", strconv.Itoa(portOf(srv))}, &out)
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 3 {
		t.Fatalf("candidates = %v, want 3", got.Candidates)
	}
	if got.Candidates[0].WindowID != "@1" || got.Candidates[0].Action != "keep" {
		t.Errorf("most-recent window @1 should be kept: %+v", got.Candidates[0])
	}
	if got.Candidates[1].Action != "closed" || got.Candidates[2].Action != "closed" {
		t.Errorf("older windows should close: %+v", got.Candidates[1:])
	}
	if got.Closed != 2 {
		t.Errorf("closed = %d, want 2", got.Closed)
	}
}

func TestTidy_DryRunKillsNothing(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "kill-window" {
			t.Fatalf("-dry-run must never call kill-window")
		}
		switch args[0] {
		case "list-panes":
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	srv := tidyResultHandler(t, nil)
	defer srv.Close()

	var out bytes.Buffer
	runTidy([]string{"-dry-run", "-port", strconv.Itoa(portOf(srv))}, &out)
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Candidates[0].Action != "would-close" {
		t.Errorf("action = %q, want would-close", got.Candidates[0].Action)
	}
	if got.Closed != 0 {
		t.Errorf("closed = %d, want 0", got.Closed)
	}
}

// TestTidy_KillWindowIsTheOnlyDestructiveVerb asserts across a live run
// (not dry-run) that kill-window is the only destructive tmux verb tcm
// tidy ever calls — never kill-session, never kill-server.
func TestTidy_KillWindowIsTheOnlyDestructiveVerb(t *testing.T) {
	withStubTmux(t, assertOnlyKillWindow(t))
	srv := tidyResultHandler(t, nil)
	defer srv.Close()

	var out bytes.Buffer
	code := runTidy([]string{"-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if got.Closed != 2 {
		t.Fatalf("closed = %d, want 2 (both delegate windows, all-done status)", got.Closed)
	}
}

// TestTidy_ContractPin pins the exact JSON keys tcm tidy emits.
func TestTidy_ContractPin(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		switch args[0] {
		case "list-panes":
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	srv := tidyResultHandler(t, nil)
	defer srv.Close()

	var out bytes.Buffer
	runTidy([]string{"-dry-run", "-port", strconv.Itoa(portOf(srv))}, &out)

	var raw map[string]any
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	for _, key := range []string{"dryRun", "candidates", "closed"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in output: %v", key, raw)
		}
	}
	candidates, ok := raw["candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatalf("candidates = %v", raw["candidates"])
	}
	cand, ok := candidates[0].(map[string]any)
	if !ok {
		t.Fatalf("candidate not an object: %v", candidates[0])
	}
	for _, key := range []string{"windowId", "sessionName", "paneId", "status", "action"} {
		if _, ok := cand[key]; !ok {
			t.Errorf("missing key %q in candidate: %v", key, cand)
		}
	}
}
