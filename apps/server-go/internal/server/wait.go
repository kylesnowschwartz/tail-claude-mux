package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const (
	defaultWaitTimeout = 120 * time.Second
	maxWaitTimeout     = 600 * time.Second
)

type waitResponse struct {
	Session        string `json:"session"`
	Status         string `json:"status"`
	Terminal       bool   `json:"terminal"`
	TimedOut       bool   `json:"timedOut"`
	ElapsedSeconds int64  `json:"elapsedSeconds"`
}

type delegationWaitState struct {
	session string
	status  string
	bound   bool
}

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	paneID := r.URL.Query().Get("pane")
	threadID := r.URL.Query().Get("thread")
	delegationID := r.URL.Query().Get("delegation")
	if delegationID != "" {
		s.handleDelegationWait(w, r, delegationID, session, threadID, paneID)
		return
	}
	if session == "" {
		http.Error(w, "session is required", http.StatusBadRequest)
		return
	}
	timeout, err := parseWaitTimeout(r.URL.Query().Get("timeout"))
	if err != nil {
		http.Error(w, "timeout must be a number of seconds", http.StatusBadRequest)
		return
	}
	status, resolveErr := s.waitStatus(session, threadID, paneID)
	if resolveErr != nil {
		writeAgentResolutionError(w, resolveErr)
		return
	}
	if status == "" {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	started := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(s.waitInterval())
	defer poll.Stop()

	for {
		status, resolveErr := s.waitStatus(session, threadID, paneID)
		if resolveErr != nil {
			writeAgentResolutionError(w, resolveErr)
			return
		}
		if status == "" {
			s.writeWaitResponse(w, session, "gone", started, false)
			return
		}
		if wire.IsTerminalStatus(status) || status == wire.StatusWaiting {
			s.writeWaitResponse(w, session, status, started, false)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			status, resolveErr = s.waitStatus(session, threadID, paneID)
			if resolveErr != nil {
				writeAgentResolutionError(w, resolveErr)
				return
			}
			if status == "" {
				status = "gone"
			}
			s.writeWaitResponse(w, session, status, started, true)
			return
		case <-poll.C:
		}
	}
}

func (s *Server) handleDelegationWait(w http.ResponseWriter, r *http.Request, delegationID, session, threadID, paneID string) {
	timeout, err := parseWaitTimeout(r.URL.Query().Get("timeout"))
	if err != nil {
		http.Error(w, "timeout must be a number of seconds", http.StatusBadRequest)
		return
	}
	state, resolveErr := s.resolveDelegationWait(delegationID, session, threadID, paneID)
	if resolveErr != nil {
		writeAgentResolutionError(w, resolveErr)
		return
	}
	if state.bound && state.status == "" {
		s.writeWaitResponse(w, state.session, wire.StatusGone, time.Now(), false)
		return
	}

	started := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(s.waitInterval())
	defer poll.Stop()

	for {
		state, resolveErr = s.resolveDelegationWait(delegationID, session, threadID, paneID)
		if resolveErr != nil {
			writeAgentResolutionError(w, resolveErr)
			return
		}
		if state.bound && state.status == "" {
			s.writeWaitResponse(w, state.session, wire.StatusGone, started, false)
			return
		}
		if wire.IsTerminalStatus(state.status) || state.status == wire.StatusWaiting {
			s.writeWaitResponse(w, state.session, state.status, started, false)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			state, resolveErr = s.resolveDelegationWait(delegationID, session, threadID, paneID)
			if resolveErr != nil {
				writeAgentResolutionError(w, resolveErr)
				return
			}
			if state.status == "" {
				state.status = wire.StatusGone
			}
			s.writeWaitResponse(w, state.session, state.status, started, true)
			return
		case <-poll.C:
		}
	}
}

func (s *Server) resolveDelegationWait(delegationID, session, threadID, paneID string) (delegationWaitState, *agentResolutionError) {
	if s.Delegations == nil {
		return delegationWaitState{}, &agentResolutionError{status: http.StatusNotFound, message: "delegation not found"}
	}
	record, err := s.Delegations.Get(delegationID)
	if err != nil {
		if errors.Is(err, delegation.ErrNotFound) {
			return delegationWaitState{}, &agentResolutionError{status: http.StatusNotFound, message: "delegation not found"}
		}
		return delegationWaitState{}, &agentResolutionError{status: http.StatusInternalServerError, message: "could not read delegation"}
	}
	state := delegationWaitState{session: record.SessionName, bound: record.ThreadID != ""}
	if session != "" && session != record.SessionName {
		return state, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and session identify different agents"}
	}
	if paneID != "" && paneID != record.PaneID {
		return state, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and pane identify different agents"}
	}

	if record.ThreadID == "" {
		tracked, resolveErr := s.resolveTrackedEvent(record.SessionName, "", record.PaneID)
		if resolveErr != nil {
			if resolveErr.status == http.StatusNotFound {
				return state, nil
			}
			return state, resolveErr
		}
		if threadID != "" && tracked.ThreadID != threadID {
			return state, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and thread identify different agents"}
		}
		state.status = tracked.Status
		return state, nil
	}

	if threadID != "" && threadID != record.ThreadID {
		return state, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and thread identify different agents"}
	}
	tracked, resolveErr := s.resolveTrackedEvent(record.SessionName, record.ThreadID, "")
	if resolveErr == nil {
		state.status = tracked.Status
		return state, nil
	}
	if resolveErr.status != http.StatusNotFound {
		return state, resolveErr
	}
	response := resultResponse{Session: record.SessionName, ThreadID: record.ThreadID, Cwd: record.Cwd}
	found, inspectErr := s.populateDiskResult(&response, record.Agent)
	if inspectErr != nil {
		return state, &agentResolutionError{status: http.StatusInternalServerError, message: inspectErr.Error()}
	}
	if found {
		state.status = response.Status
	}
	return state, nil
}

func parseWaitTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultWaitTimeout, nil
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	timeout := time.Duration(seconds * float64(time.Second))
	if timeout < time.Second {
		timeout = time.Second
	}
	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}
	return timeout, nil
}

func (s *Server) waitStatus(session, threadID, paneID string) (string, *agentResolutionError) {
	state, err := s.resolveTrackedEvent(session, threadID, paneID)
	if state == nil {
		return "", err
	}
	return state.Status, err
}

func (s *Server) waitInterval() time.Duration {
	if s.waitPollInterval > 0 {
		return s.waitPollInterval
	}
	return time.Second
}

func (s *Server) writeWaitResponse(w http.ResponseWriter, session, status string, started time.Time, timedOut bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(waitResponse{
		Session: session, Status: status, Terminal: wire.IsTerminalStatus(status), TimedOut: timedOut,
		ElapsedSeconds: int64(time.Since(started) / time.Second),
	})
}
