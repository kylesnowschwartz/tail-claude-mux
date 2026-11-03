package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// countingTmux fakes tmux and counts state builds: every Builder.Build
// lists sessions exactly once.
type countingTmux struct {
	listings atomic.Int32
	sessions atomic.Value // string: list-sessions output
	delay    time.Duration
}

func (c *countingTmux) run(args ...string) (string, error) {
	if args[0] == "list-sessions" {
		c.listings.Add(1)
		time.Sleep(c.delay)
		return c.sessions.Load().(string), nil
	}
	return "", nil
}

func newStateTestServer(t *testing.T, delay time.Duration) (*Server, *countingTmux) {
	t.Helper()
	fake := &countingTmux{delay: delay}
	fake.sessions.Store("$1\tproj\t100\t1\t1\t/p\t0")
	tm := &tmux.Tmux{Run: fake.run}
	s := New(&state.Builder{Tmux: tm, Git: gitinfo.NewCache(), Order: sessionorder.Load("")}, nil, nil, nil, nil)
	return s, fake
}

func fetchState(s *Server) (wire.ServerState, error) {
	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/state", nil))
	if resp.Code != http.StatusOK {
		return wire.ServerState{}, fmt.Errorf("GET /state status = %d: %s", resp.Code, resp.Body.String())
	}
	var st wire.ServerState
	if err := json.Unmarshal(resp.Body.Bytes(), &st); err != nil {
		return wire.ServerState{}, fmt.Errorf("decode /state: %w", err)
	}
	return st, nil
}

func getState(t *testing.T, s *Server) wire.ServerState {
	t.Helper()
	st, err := fetchState(s)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func sessionNames(st wire.ServerState) []string {
	names := make([]string, 0, len(st.Sessions))
	for _, sess := range st.Sessions {
		names = append(names, sess.Name)
	}
	return names
}

func TestStateConcurrentRequestsShareOneBuild(t *testing.T) {
	s, fake := newStateTestServer(t, 20*time.Millisecond)

	var wg sync.WaitGroup
	results := make([]wire.ServerState, 32)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = fetchState(s)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if n := fake.listings.Load(); n < 1 || n > 2 {
		t.Fatalf("builds = %d for %d concurrent requests, want 1 or 2", n, len(results))
	}
	for i, st := range results {
		if st.Type != wire.TypeState || st.TS == 0 || len(st.Sessions) != 1 || st.Sessions[0].Name != "proj" {
			t.Fatalf("response %d = type %q ts %d sessions %v, want state with ts and [proj]", i, st.Type, st.TS, sessionNames(st))
		}
	}

	getState(t, s)
	if n := fake.listings.Load(); n > 2 {
		t.Fatalf("builds = %d after a back-to-back request, want the snapshot reused", n)
	}
}

func TestStateShowsChangeAfterFreshnessWindow(t *testing.T) {
	s, fake := newStateTestServer(t, 0)
	s.stateMaxAge = 30 * time.Millisecond

	if got := sessionNames(getState(t, s)); len(got) != 1 {
		t.Fatalf("sessions = %v, want [proj]", got)
	}
	fake.sessions.Store("$1\tproj\t100\t1\t1\t/p\t0\n$2\tother\t150\t0\t1\t/p\t0")

	time.Sleep(s.stateMaxAge + 10*time.Millisecond)
	if got := sessionNames(getState(t, s)); len(got) != 2 || got[1] != "other" {
		t.Fatalf("sessions after freshness window = %v, want [proj other]", got)
	}
}

func TestStateBroadcastRefreshesSnapshot(t *testing.T) {
	s, fake := newStateTestServer(t, 0)

	getState(t, s)
	fake.sessions.Store("$1\tproj\t100\t1\t1\t/p\t0\n$2\tother\t150\t0\t1\t/p\t0")
	s.broadcast()

	if got := sessionNames(getState(t, s)); len(got) != 2 {
		t.Fatalf("sessions after broadcast = %v, want [proj other]", got)
	}
	if n := fake.listings.Load(); n != 2 {
		t.Fatalf("builds = %d, want 2 (one per request-driven or broadcast build)", n)
	}
}

func TestStateCancelledRequestDoesNotBuild(t *testing.T) {
	s, fake := newStateTestServer(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp := httptest.NewRecorder()
	s.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/state", nil).WithContext(ctx))

	if n := fake.listings.Load(); n != 0 {
		t.Fatalf("builds = %d for a cancelled request, want 0", n)
	}
	if resp.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty for a cancelled request", resp.Body.String())
	}
}
