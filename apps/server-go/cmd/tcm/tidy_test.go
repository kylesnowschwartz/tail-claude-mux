package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// tidyResultHandler answers GET /result with statusOf[pane], defaulting to
// "done" for unlisted panes.
func tidyResultHandler(t *testing.T, statusOf map[string]string) *httptest.Server {
	t.Helper()
	paneByDelegation := stubTidyDelegations(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/result" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		delegationID := r.URL.Query().Get("delegation")
		pane, ok := paneByDelegation[delegationID]
		if !ok {
			t.Fatalf("unexpected delegation: %q", delegationID)
		}
		status, ok := statusOf[pane]
		if !ok {
			status = "done"
		}
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
}

func stubTidyDelegations(t *testing.T) map[string]string {
	t.Helper()
	previous := listTidyDelegations
	var records []delegation.Record
	paneByDelegation := make(map[string]string)
	for _, session := range []string{"test-session", "other-session"} {
		for paneNumber := 1; paneNumber <= 4; paneNumber++ {
			paneID := fmt.Sprintf("%%%d", paneNumber)
			delegationID := session + "-delegation-" + strconv.Itoa(paneNumber)
			records = append(records, delegation.Record{
				DelegationID: delegationID,
				SessionName:  session,
				PaneID:       paneID,
				CreatedAt:    time.Unix(int64(paneNumber), 0),
			})
			paneByDelegation[delegationID] = paneID
		}
	}
	listTidyDelegations = func() ([]delegation.Record, error) { return records, nil }
	t.Cleanup(func() { listTidyDelegations = previous })
	return paneByDelegation
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

func TestTidy_NonTerminalStatusIsUnknownAndSkipped(t *testing.T) {
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
	if len(got.Candidates) != 1 || got.Candidates[0].Status != statusUnknown || got.Candidates[0].Action != "skip" {
		t.Fatalf("non-terminal status must be unknown and skipped: %+v", got.Candidates)
	}
	if !strings.Contains(got.Candidates[0].Reason, "running") {
		t.Errorf("reason = %q, want the non-terminal status", got.Candidates[0].Reason)
	}
	if got.Closed != 0 {
		t.Errorf("closed = %d, want 0", got.Closed)
	}
}

// tidy404Handler answers /result 404 for panes in notFoundPanes and 200/done
// for everything else.
func tidy404Handler(t *testing.T, notFoundPanes map[string]bool) *httptest.Server {
	t.Helper()
	paneByDelegation := stubTidyDelegations(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/result" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		pane := paneByDelegation[r.URL.Query().Get("delegation")]
		if notFoundPanes[pane] {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "delegation not yet bound"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "done"})
	}))
}

func TestTidy_UnboundDelegationIsUnknownAndSkipped(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		switch args[0] {
		case "list-panes":
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	srv := tidy404Handler(t, map[string]bool{"%1": true})
	defer srv.Close()

	var out bytes.Buffer
	code := runTidy([]string{"-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 1 || got.Candidates[0].Status != statusUnknown || got.Candidates[0].Action != "skip" {
		t.Fatalf("unbound delegation must be unknown and skipped: %+v", got.Candidates)
	}
	if got.Candidates[0].Reason != "delegation not yet bound" || got.Closed != 0 {
		t.Fatalf("unbound delegation outcome = %+v", got)
	}
}

func TestTidy_MissingDelegationRecordIsUnknownAndSkipped(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		switch args[0] {
		case "list-panes":
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		case "display-message":
			return "test-session\n", nil
		}
		return "", nil
	})
	previous := listTidyDelegations
	listTidyDelegations = func() ([]delegation.Record, error) { return nil, nil }
	t.Cleanup(func() { listTidyDelegations = previous })

	var out bytes.Buffer
	code := runTidy([]string{"-all-sessions"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 1 || got.Candidates[0].Status != statusUnknown || got.Candidates[0].Action != "skip" {
		t.Fatalf("missing delegation must be unknown and skipped: %+v", got.Candidates)
	}
	if got.Candidates[0].DelegationID != "" || got.Candidates[0].Reason != "delegation record not found for pane" || got.Closed != 0 {
		t.Fatalf("missing delegation outcome = %+v", got)
	}
}

func TestTidy_WindowWithAnyUnknownPaneIsSkipped(t *testing.T) {
	withStubTmux(t, func(args ...string) (string, error) {
		if args[0] == "list-panes" {
			return "@1\t100\ttest-session\t%1\tsh -c 'codex' '--profile' 'tcm-delegate' --\n" +
				"@1\t100\ttest-session\t%2\tsh -c 'codex' '--profile' 'tcm-delegate' --\n", nil
		}
		return "", nil
	})
	previous := listTidyDelegations
	listTidyDelegations = func() ([]delegation.Record, error) {
		return []delegation.Record{{DelegationID: "done-delegation", SessionName: "test-session", PaneID: "%1"}}, nil
	}
	t.Cleanup(func() { listTidyDelegations = previous })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": wire.StatusDone})
	}))
	defer srv.Close()

	var out bytes.Buffer
	code := runTidy([]string{"-all-sessions", "-port", strconv.Itoa(portOf(srv))}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	var got tidyOutcome
	_ = json.Unmarshal(out.Bytes(), &got)
	if len(got.Candidates) != 1 || got.Candidates[0].Status != statusUnknown || got.Candidates[0].Action != "skip" {
		t.Fatalf("window with an unknown pane must be skipped: %+v", got.Candidates)
	}
	if got.Candidates[0].PaneID != "%2" || got.Closed != 0 {
		t.Fatalf("unknown pane outcome = %+v", got)
	}
}

func TestClassifyDelegation(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		body       string
		wantStatus string
		wantReason string
		wantClose  bool
	}{
		{name: "done", httpStatus: http.StatusOK, body: `{"status":"done"}`, wantStatus: wire.StatusDone, wantClose: true},
		{name: "error", httpStatus: http.StatusOK, body: `{"status":"error"}`, wantStatus: wire.StatusError, wantClose: true},
		{name: "interrupted", httpStatus: http.StatusOK, body: `{"status":"interrupted"}`, wantStatus: wire.StatusInterrupted, wantClose: true},
		{name: "unbound", httpStatus: http.StatusNotFound, body: `{"error":"delegation not yet bound"}`, wantStatus: statusUnknown, wantReason: "delegation not yet bound"},
		{name: "non-terminal", httpStatus: http.StatusOK, body: `{"status":"running"}`, wantStatus: statusUnknown, wantReason: `status "running" is not terminal`},
		{name: "server error", httpStatus: http.StatusInternalServerError, body: `{"error":"could not read delegation"}`, wantStatus: statusUnknown, wantReason: "could not read delegation"},
		{name: "invalid response", httpStatus: http.StatusOK, body: `{`, wantStatus: statusUnknown, wantReason: "invalid result response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("delegation"); got != "delegation-id" {
					t.Fatalf("delegation query = %q", got)
				}
				w.WriteHeader(test.httpStatus)
				_, _ = w.Write([]byte(test.body))
			}))
			defer srv.Close()

			status, reason, closeable := classifyDelegation(portOf(srv), "delegation-id")
			if status != test.wantStatus || reason != test.wantReason || closeable != test.wantClose {
				t.Fatalf("classification = (%q, %q, %v), want (%q, %q, %v)", status, reason, closeable, test.wantStatus, test.wantReason, test.wantClose)
			}
		})
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
	for _, key := range []string{"windowId", "sessionName", "paneId", "delegationId", "status", "action"} {
		if _, ok := cand[key]; !ok {
			t.Errorf("missing key %q in candidate: %v", key, cand)
		}
	}
}
