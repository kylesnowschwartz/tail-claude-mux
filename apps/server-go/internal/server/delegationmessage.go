package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
)

const delegationMessageBodyLimit = 128 << 10

type delegationMessageRequest struct {
	Delegation string `json:"delegation"`
	From       string `json:"from"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	ReplyTo    string `json:"replyTo"`
}

type delegationMessagesResponse struct {
	Messages []delegation.Message `json:"messages"`
}

type delegationAnswerResponse struct {
	Answered bool                `json:"answered"`
	TimedOut bool                `json:"timedOut,omitempty"`
	Message  *delegation.Message `json:"message,omitempty"`
}

func (s *Server) handleDelegationMessage(w http.ResponseWriter, r *http.Request) {
	var req delegationMessageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, delegationMessageBodyLimit))
	if err := decoder.Decode(&req); err != nil {
		writeDelegationMessageError(w, http.StatusBadRequest, "request body must be valid JSON")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeDelegationMessageError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	if req.Delegation == "" {
		writeDelegationMessageError(w, http.StatusBadRequest, "delegation is required")
		return
	}
	if s.Delegations == nil {
		writeDelegationMessageError(w, http.StatusNotFound, "delegation not found")
		return
	}

	s.messageMu.Lock()
	defer s.messageMu.Unlock()
	if _, err := s.Delegations.Get(req.Delegation); err != nil {
		writeDelegationStoreError(w, err)
		return
	}
	if req.From == "orchestrator" && req.Kind == "answer" {
		if req.ReplyTo == "" {
			writeDelegationMessageError(w, http.StatusBadRequest, "answer replyTo is required")
			return
		}
		messages, err := s.Delegations.Messages(req.Delegation)
		if err != nil {
			writeDelegationStoreError(w, err)
			return
		}
		if !containsAsk(messages, req.ReplyTo) {
			writeDelegationMessageError(w, http.StatusBadRequest, "replyTo must name an existing ask")
			return
		}
		if _, answered, err := s.Delegations.AnswerFor(req.Delegation, req.ReplyTo); err != nil {
			writeDelegationStoreError(w, err)
			return
		} else if answered {
			writeDelegationMessageError(w, http.StatusConflict, "ask is already answered")
			return
		}
	}
	stored, err := s.Delegations.AppendMessage(req.Delegation, delegation.Message{
		From: req.From, Kind: req.Kind, Text: req.Text, ReplyTo: req.ReplyTo,
	})
	if err != nil {
		if errors.Is(err, delegation.ErrNotFound) {
			writeDelegationMessageError(w, http.StatusNotFound, "delegation not found")
			return
		}
		writeDelegationMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stored)
}

func (s *Server) handleDelegationMessages(w http.ResponseWriter, r *http.Request) {
	delegationID := r.URL.Query().Get("delegation")
	if delegationID == "" {
		writeDelegationMessageError(w, http.StatusBadRequest, "delegation is required")
		return
	}
	if s.Delegations == nil {
		writeDelegationMessageError(w, http.StatusNotFound, "delegation not found")
		return
	}
	if _, err := s.Delegations.Get(delegationID); err != nil {
		writeDelegationStoreError(w, err)
		return
	}
	messages, err := s.Delegations.Messages(delegationID)
	if err != nil {
		writeDelegationStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(delegationMessagesResponse{Messages: messages})
}

func (s *Server) handleDelegationAnswer(w http.ResponseWriter, r *http.Request) {
	delegationID := r.URL.Query().Get("delegation")
	askID := r.URL.Query().Get("ask")
	if delegationID == "" || askID == "" {
		writeDelegationMessageError(w, http.StatusBadRequest, "delegation and ask are required")
		return
	}
	if s.Delegations == nil {
		writeDelegationMessageError(w, http.StatusNotFound, "delegation not found")
		return
	}
	if _, err := s.Delegations.Get(delegationID); err != nil {
		writeDelegationStoreError(w, err)
		return
	}
	timeout, err := parseWaitTimeout(r.URL.Query().Get("timeout"))
	if err != nil {
		writeDelegationMessageError(w, http.StatusBadRequest, "timeout must be a number of seconds")
		return
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(s.waitInterval())
	defer poll.Stop()

	for {
		answer, answered, err := s.Delegations.AnswerFor(delegationID, askID)
		if err != nil {
			writeDelegationStoreError(w, err)
			return
		}
		if answered {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(delegationAnswerResponse{Answered: true, Message: &answer})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			answer, answered, err = s.Delegations.AnswerFor(delegationID, askID)
			if err != nil {
				writeDelegationStoreError(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if answered {
				_ = json.NewEncoder(w).Encode(delegationAnswerResponse{Answered: true, Message: &answer})
				return
			}
			_ = json.NewEncoder(w).Encode(delegationAnswerResponse{TimedOut: true})
			return
		case <-poll.C:
		}
	}
}

func containsAsk(messages []delegation.Message, askID string) bool {
	for _, msg := range messages {
		if msg.ID == askID && msg.From == "delegate" && msg.Kind == "ask" {
			return true
		}
	}
	return false
}

func writeDelegationStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, delegation.ErrNotFound) {
		writeDelegationMessageError(w, http.StatusNotFound, "delegation not found")
		return
	}
	writeDelegationMessageError(w, http.StatusInternalServerError, "could not read delegation messages")
}

func writeDelegationMessageError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
