package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const delegateContextPreamble = `[TCM delegate context] You are a delegate agent. An orchestrator in tmux session "%s" spawned you and is watching this delegation (ID %s). You can message it at any time:
  "$TCM_BIN" tell -message "<text>"   — deliver a note or result upstream (fire-and-forget)
  "$TCM_BIN" ask -message "<question>" — ask and wait for the orchestrator's answer
Both read TCM_DELEGATION_ID and TCM_PORT from your environment. Multiline text: use -message-file <path>. Never claim you have no upstream. Your task brief follows.
---
`

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

	delegationID, err := delegation.NewID()
	if err != nil {
		writeSpawnAgentError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.DelegationID = delegationID
	executable, err := os.Executable()
	if err != nil {
		writeSpawnAgentError(w, http.StatusInternalServerError, "could not resolve tcm executable")
		return
	}
	req.TCMBin = executable
	port := s.Port
	if port == 0 {
		port = wire.ServerPort
	}
	req.TCMPort = strconv.Itoa(port)
	req.Prompt = prependDelegateContext(req.Prompt, req.OwnerSession, delegationID)
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
	if s.Delegations == nil {
		log.Printf("delegation persistence failed after spawn: delegation store is unavailable")
	} else {
		record, err := s.Delegations.CreateWithID(delegationID, delegation.CreateParams{
			Agent:        req.Agent,
			Cwd:          req.Dir,
			OwnerSession: req.OwnerSession,
			SessionName:  result.SessionName,
			PaneID:       result.PaneID,
			WindowID:     result.WindowID,
		})
		if err != nil {
			log.Printf("delegation persistence failed after spawn: %v", err)
		} else {
			result.DelegationID = record.DelegationID
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func prependDelegateContext(prompt, ownerSession, delegationID string) string {
	return fmt.Sprintf(delegateContextPreamble, ownerSession, delegationID) + prompt
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
