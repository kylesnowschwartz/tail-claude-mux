package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kylesnowschwartz/agent-ouija/codex/rollout"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

type resultResponse struct {
	Session        string               `json:"session"`
	Status         string               `json:"status"`
	HasFinal       bool                 `json:"hasFinal"`
	FinalMessage   string               `json:"finalMessage"`
	RolloutPath    string               `json:"rolloutPath"`
	ThreadID       string               `json:"threadId"`
	Cwd            string               `json:"cwd"`
	Identification string               `json:"identification"`
	Messages       []delegation.Message `json:"messages,omitempty"`
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	session := query.Get("session")
	threadID := query.Get("thread")
	paneID := query.Get("pane")
	delegationID := query.Get("delegation")

	if delegationID != "" {
		s.handleDelegationResult(w, delegationID, session, threadID, paneID)
		return
	}
	if threadID != "" {
		s.handleThreadResult(w, session, threadID, paneID)
		return
	}

	s.handleTrackedResult(w, session, paneID)
}

func (s *Server) handleTrackedResult(w http.ResponseWriter, session, paneID string) {
	if session == "" {
		writeResultError(w, http.StatusBadRequest, "session is required")
		return
	}

	state, resolveErr := s.followupState(session, "", paneID)
	if resolveErr != nil {
		writeResultError(w, resolveErr.status, resolveErr.message)
		return
	}
	if state == nil {
		writeResultError(w, http.StatusNotFound, "session not found")
		return
	}
	s.writeLiveResult(w, session, state, nil)
}

func (s *Server) handleDelegationResult(w http.ResponseWriter, delegationID, session, threadID, paneID string) {
	if s.Delegations == nil {
		writeResultError(w, http.StatusNotFound, "delegation not found")
		return
	}
	record, err := s.Delegations.Get(delegationID)
	if err != nil {
		if errors.Is(err, delegation.ErrNotFound) {
			writeResultError(w, http.StatusNotFound, "delegation not found")
			return
		}
		writeResultError(w, http.StatusInternalServerError, "could not read delegation")
		return
	}
	messages, err := s.Delegations.Messages(delegationID)
	if err != nil {
		writeResultError(w, http.StatusInternalServerError, "could not read delegation messages")
		return
	}
	if session != "" && session != record.SessionName {
		writeResultError(w, http.StatusBadRequest, "delegation and session identify different agents")
		return
	}
	if paneID != "" && paneID != record.PaneID {
		writeResultError(w, http.StatusBadRequest, "delegation and pane identify different agents")
		return
	}
	if record.ThreadID != "" {
		if threadID != "" && threadID != record.ThreadID {
			writeResultError(w, http.StatusBadRequest, "delegation and thread identify different agents")
			return
		}
		tracked, resolveErr := s.followupState(record.SessionName, record.ThreadID, "")
		if resolveErr == nil && tracked != nil {
			s.writeLiveResult(w, record.SessionName, tracked, messages)
			return
		}
		if resolveErr != nil && resolveErr.status != http.StatusNotFound {
			writeResultError(w, resolveErr.status, resolveErr.message)
			return
		}

		response := resultResponse{Session: record.SessionName, ThreadID: record.ThreadID, Cwd: record.Cwd, Messages: messages}
		found, inspectErr := s.populateDiskResult(&response, record.Agent)
		if inspectErr != nil {
			writeResultError(w, http.StatusInternalServerError, inspectErr.Error())
			return
		}
		if !found {
			writeResultError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeResultResponse(w, response)
		return
	}

	state, resolveErr := s.followupState(record.SessionName, "", record.PaneID)
	if resolveErr != nil && resolveErr.status != http.StatusNotFound {
		writeResultError(w, resolveErr.status, resolveErr.message)
		return
	}
	if state == nil || resolveErr != nil {
		writeResultError(w, http.StatusNotFound, "delegation not yet bound")
		return
	}
	if threadID != "" && state.ThreadID != threadID {
		writeResultError(w, http.StatusBadRequest, "delegation and thread identify different agents")
		return
	}
	s.writeLiveResult(w, record.SessionName, state, messages)
}

func (s *Server) handleThreadResult(w http.ResponseWriter, session, threadID, paneID string) {
	if session != "" {
		state, resolveErr := s.followupState(session, threadID, paneID)
		if resolveErr == nil && state != nil {
			s.writeLiveResult(w, session, state, nil)
			return
		}
		if resolveErr != nil && resolveErr.status != http.StatusNotFound {
			writeResultError(w, resolveErr.status, resolveErr.message)
			return
		}
	}
	response := resultResponse{Session: session, ThreadID: threadID}
	found, err := s.populateDiskResult(&response, "")
	if err != nil {
		writeResultError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeResultError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeResultResponse(w, response)
}

func (s *Server) writeLiveResult(w http.ResponseWriter, session string, state *wire.AgentEvent, messages []delegation.Message) {
	response := resultResponse{Session: session, Status: state.Status, ThreadID: state.ThreadID, Messages: messages}
	if state.Agent != "codex" || state.ThreadID == "" || s.CodexWatcher == nil {
		writeResultResponse(w, response)
		return
	}
	path, err := s.CodexWatcher.RolloutForThread(state.ThreadID)
	if err != nil {
		writeResultError(w, http.StatusInternalServerError, "could not inspect codex rollouts")
		return
	}
	response.RolloutPath = path
	if path != "" {
		if err := populateCodexResult(&response, path, false); err != nil {
			writeResultError(w, http.StatusInternalServerError, "could not read codex rollout")
			return
		}
	}
	writeResultResponse(w, response)
}

func (s *Server) populateDiskResult(response *resultResponse, agent string) (bool, error) {
	if (agent == "" || agent == "codex") && s.CodexWatcher != nil {
		path, err := s.CodexWatcher.RolloutForThread(response.ThreadID)
		if err != nil {
			return false, errors.New("could not inspect codex rollouts")
		}
		if path != "" {
			if err := populateCodexResult(response, path, true); err != nil {
				return false, errors.New("could not read codex rollout")
			}
			return true, nil
		}
	}
	if (agent == "" || agent == "claude" || agent == "claude-code") && s.Watcher != nil {
		snapshot, err := s.Watcher.ResultForThread(response.ThreadID)
		if err != nil {
			return false, errors.New("could not read claude transcript")
		}
		if snapshot.TranscriptPath != "" {
			response.RolloutPath = snapshot.TranscriptPath
			response.Status = snapshot.Status
			response.HasFinal = snapshot.HasFinal
			response.FinalMessage = snapshot.FinalMessage
			return true, nil
		}
	}
	return false, nil
}

func populateCodexResult(response *resultResponse, path string, useDiskStatus bool) error {
	snapshot, err := rollout.SessionSnapshot(path)
	if err != nil {
		return err
	}
	response.RolloutPath = path
	response.Cwd = snapshot.Cwd
	response.HasFinal = snapshot.HasFinal
	response.FinalMessage = snapshot.Final.Text
	response.Identification = snapshot.Final.Identification
	if useDiskStatus {
		response.Status = snapshot.Status.String()
	}
	return nil
}

func writeResultResponse(w http.ResponseWriter, response resultResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func writeResultError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
