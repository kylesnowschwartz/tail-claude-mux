package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

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

func (s *Server) handleWait(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	paneID := r.URL.Query().Get("pane")
	threadID := r.URL.Query().Get("thread")
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
