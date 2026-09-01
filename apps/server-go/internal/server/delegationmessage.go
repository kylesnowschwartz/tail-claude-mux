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

type delegationMessageFailure struct {
	status  int
	message string
}

type answerWaiterKey struct {
	delegationID string
	askID        string
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
		stored, failure := s.appendDelegationAnswer(req.Delegation, req.ReplyTo, req.Text)
		if failure != nil {
			writeDelegationMessageError(w, failure.status, failure.message)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stored)
		return
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

// appendDelegationAnswer validates and stores an answer while messageMu is held.
func (s *Server) appendDelegationAnswer(delegationID, askID, text string) (delegation.Message, *delegationMessageFailure) {
	if askID == "" {
		return delegation.Message{}, &delegationMessageFailure{status: http.StatusBadRequest, message: "answer replyTo is required"}
	}
	messages, err := s.Delegations.Messages(delegationID)
	if err != nil {
		return delegation.Message{}, delegationStoreFailure(err)
	}
	if !containsAsk(messages, askID) {
		return delegation.Message{}, &delegationMessageFailure{status: http.StatusBadRequest, message: "replyTo must name an existing ask"}
	}
	if _, answered, err := s.Delegations.AnswerFor(delegationID, askID); err != nil {
		return delegation.Message{}, delegationStoreFailure(err)
	} else if answered {
		return delegation.Message{}, &delegationMessageFailure{status: http.StatusConflict, message: "ask is already answered"}
	}
	stored, err := s.Delegations.AppendMessage(delegationID, delegation.Message{
		From: "orchestrator", Kind: "answer", Text: text, ReplyTo: askID,
	})
	if err != nil {
		if errors.Is(err, delegation.ErrNotFound) {
			return delegation.Message{}, &delegationMessageFailure{status: http.StatusNotFound, message: "delegation not found"}
		}
		return delegation.Message{}, &delegationMessageFailure{status: http.StatusBadRequest, message: err.Error()}
	}
	return stored, nil
}

func delegationStoreFailure(err error) *delegationMessageFailure {
	if errors.Is(err, delegation.ErrNotFound) {
		return &delegationMessageFailure{status: http.StatusNotFound, message: "delegation not found"}
	}
	return &delegationMessageFailure{status: http.StatusInternalServerError, message: "could not read delegation messages"}
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

func (s *Server) handleDelegation(w http.ResponseWriter, r *http.Request) {
	delegationID := r.URL.Query().Get("delegation")
	if delegationID == "" {
		writeDelegationMessageError(w, http.StatusBadRequest, "delegation is required")
		return
	}
	if s.Delegations == nil {
		writeDelegationMessageError(w, http.StatusNotFound, "delegation not found")
		return
	}
	record, err := s.Delegations.Get(delegationID)
	if err != nil {
		writeDelegationStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(record)
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
	unregister := s.registerAnswerWaiter(delegationID, askID)
	defer unregister()
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

func (s *Server) registerAnswerWaiter(delegationID, askID string) func() {
	key := answerWaiterKey{delegationID: delegationID, askID: askID}
	s.answerWaiterMu.Lock()
	if s.answerWaiters == nil {
		s.answerWaiters = make(map[answerWaiterKey]int)
	}
	s.answerWaiters[key]++
	s.answerWaiterMu.Unlock()

	return func() {
		s.answerWaiterMu.Lock()
		defer s.answerWaiterMu.Unlock()
		if s.answerWaiters[key] <= 1 {
			delete(s.answerWaiters, key)
			return
		}
		s.answerWaiters[key]--
	}
}

func (s *Server) hasAnswerWaiter(delegationID, askID string) bool {
	s.answerWaiterMu.Lock()
	defer s.answerWaiterMu.Unlock()
	return s.answerWaiters[answerWaiterKey{delegationID: delegationID, askID: askID}] > 0
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
