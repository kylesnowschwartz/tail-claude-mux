package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
)

// handleSpawnAgent starts an agent in a tmux session or owner window.
func (s *Server) handleSpawnAgent(w http.ResponseWriter, r *http.Request) {
	var req tmux.SpawnAgentRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit))
	if err := decoder.Decode(&req); err != nil {
		writeSpawnAgentDecodeError(w, err)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeSpawnAgentDecodeError(w, err)
		return
	}

	result, err := s.Builder.Tmux.SpawnAgent(req)
	if err != nil {
		var validationErr *tmux.SpawnAgentValidationError
		if errors.As(err, &validationErr) {
			writeSpawnAgentError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeSpawnAgentError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func writeSpawnAgentDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeSpawnAgentError(w, http.StatusBadRequest, "request body is too large")
		return
	}
	writeSpawnAgentError(w, http.StatusBadRequest, "request body must be valid JSON")
}

func writeSpawnAgentError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
