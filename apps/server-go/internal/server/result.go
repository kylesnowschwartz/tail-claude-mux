package server

import (
	"encoding/json"
	"net/http"

	"github.com/kylesnowschwartz/agent-ouija/codex/rollout"
)

type resultResponse struct {
	Session        string `json:"session"`
	Status         string `json:"status"`
	HasFinal       bool   `json:"hasFinal"`
	FinalMessage   string `json:"finalMessage"`
	RolloutPath    string `json:"rolloutPath"`
	ThreadID       string `json:"threadId"`
	Cwd            string `json:"cwd"`
	Identification string `json:"identification"`
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	if session == "" {
		writeResultError(w, http.StatusBadRequest, "session is required")
		return
	}

	state, resolveErr := s.followupState(session, r.URL.Query().Get("thread"), r.URL.Query().Get("pane"))
	if resolveErr != nil {
		writeAgentResolutionError(w, resolveErr)
		return
	}
	if state == nil {
		writeResultError(w, http.StatusNotFound, "session not found")
		return
	}
	response := resultResponse{Session: session, Status: state.Status, ThreadID: state.ThreadID}
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
	if path == "" {
		writeResultResponse(w, response)
		return
	}

	snapshot, err := rollout.SessionSnapshot(path)
	if err != nil {
		writeResultError(w, http.StatusInternalServerError, "could not read codex rollout")
		return
	}
	response.Cwd = snapshot.Cwd
	response.HasFinal = snapshot.HasFinal
	response.FinalMessage = snapshot.Final.Text
	response.Identification = snapshot.Final.Identification
	writeResultResponse(w, response)
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
