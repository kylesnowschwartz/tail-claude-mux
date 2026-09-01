package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/delegation"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

type followupRequest struct {
	Delegation string `json:"delegation,omitempty"`
	Session    string `json:"session"`
	Message    string `json:"message"`
	Pane       string `json:"pane,omitempty"`
	Thread     string `json:"thread,omitempty"`
}

type followupTarget struct {
	Session string
	State   *wire.AgentEvent
	Bound   bool
}

type followupResponse struct {
	UUID        string `json:"uuid"`
	RolloutPath string `json:"rolloutPath"`
	MessageFile string `json:"messageFile"`
	PaneID      string `json:"paneId"`
	Mode        string `json:"mode"`
	AskID       string `json:"askId,omitempty"`
}

func (s *Server) handleFollowup(w http.ResponseWriter, r *http.Request) {
	var req followupRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiBodyLimit))
	if err := decoder.Decode(&req); err != nil {
		writeFollowupError(w, http.StatusBadRequest, "request body must be valid JSON")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeFollowupError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		if strings.TrimSpace(req.Delegation) != "" {
			writeFollowupError(w, http.StatusBadRequest, "message is required")
			return
		}
		writeFollowupError(w, http.StatusBadRequest, "session and message are required")
		return
	}
	if strings.TrimSpace(req.Delegation) == "" && strings.TrimSpace(req.Session) == "" {
		writeFollowupError(w, http.StatusBadRequest, "session and message are required")
		return
	}
	s.followupMu.Lock()
	defer s.followupMu.Unlock()
	if req.Delegation != "" {
		delegationID, addressErr := s.resolveFollowupDelegation(req)
		if addressErr != nil {
			writeFollowupError(w, addressErr.status, addressErr.message)
			return
		}
		if s.routeFollowupToAsk(w, delegationID, req.Message) {
			return
		}
	}

	target, resolveErr := s.resolveFollowupTarget(req)
	if resolveErr != nil {
		writeFollowupError(w, resolveErr.status, resolveErr.message)
		return
	}
	if target.State == nil {
		writeFollowupError(w, http.StatusNotFound, "session not found")
		return
	}
	state := target.State
	if state.Agent != "codex" {
		writeFollowupError(w, http.StatusUnprocessableEntity, "follow-up is only supported for codex sessions")
		return
	}
	if message := followupStatusConflict(state.Status); message != "" {
		writeFollowupError(w, http.StatusConflict, message)
		return
	}
	dir := s.followupDir(target.Session, state.PaneID)
	if target.Bound && state.PaneID != "" && dir == "" {
		writeFollowupError(w, http.StatusConflict, fmt.Sprintf("delegate pane %s is gone; cannot deliver follow-up", state.PaneID))
		return
	}
	if state.PaneID == "" || dir == "" || s.CodexWatcher == nil {
		writeFollowupError(w, http.StatusInternalServerError, "session is missing follow-up metadata")
		return
	}
	rolloutPath, uuid, err := s.CodexWatcher.RolloutForFollowup(dir, state.ThreadID)
	if err != nil {
		writeFollowupError(w, http.StatusInternalServerError, "could not inspect codex rollouts")
		return
	}
	if uuid == "" {
		writeFollowupError(w, http.StatusNotFound, "no codex rollout found for session directory")
		return
	}
	messageFile, err := writeFollowupMessage(target.Session, req.Message)
	if err != nil {
		writeFollowupError(w, http.StatusInternalServerError, "could not write follow-up message")
		return
	}
	currentTarget, resolveErr := s.resolveFollowupTarget(req)
	if resolveErr != nil {
		_ = os.Remove(messageFile)
		writeFollowupError(w, resolveErr.status, resolveErr.message)
		return
	}
	current := currentTarget.State
	if current == nil || current.ThreadID != state.ThreadID || current.PaneID != state.PaneID {
		_ = os.Remove(messageFile)
		writeFollowupError(w, http.StatusConflict, "session changed before follow-up delivery; refusing to interrupt it")
		return
	}
	if message := followupStatusConflict(current.Status); message != "" {
		_ = os.Remove(messageFile)
		writeFollowupError(w, http.StatusConflict, message)
		return
	}
	currentDir := s.followupDir(currentTarget.Session, current.PaneID)
	if currentTarget.Bound && currentDir == "" {
		_ = os.Remove(messageFile)
		writeFollowupError(w, http.StatusConflict, fmt.Sprintf("delegate pane %s is gone; cannot deliver follow-up", current.PaneID))
		return
	}
	if currentDir != dir {
		_ = os.Remove(messageFile)
		writeFollowupError(w, http.StatusConflict, "session pane changed before follow-up delivery; refusing to interrupt it")
		return
	}
	// --profile keeps the resumed thread on the same posture as the spawn leg
	// (tcm-delegate profile in ~/.codex/config.toml); do not rely on codex
	// resume inheriting it implicitly.
	command := fmt.Sprintf(`codex --profile tcm-delegate -c mcp_servers.just.enabled=false resume %s "Read %s and address it"`, uuid, messageFile)
	if _, err := s.Builder.Tmux.Run("respawn-pane", "-k", "-t", state.PaneID, command); err != nil {
		_ = os.Remove(messageFile)
		writeFollowupError(w, http.StatusInternalServerError, "tmux could not deliver the follow-up")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(followupResponse{UUID: uuid, RolloutPath: rolloutPath, MessageFile: messageFile, PaneID: state.PaneID, Mode: "resume"})
}

func (s *Server) routeFollowupToAsk(w http.ResponseWriter, delegationID, text string) bool {
	if s.Delegations == nil || delegationID == "" {
		return false
	}
	s.messageMu.Lock()
	defer s.messageMu.Unlock()
	asks, err := s.Delegations.UnansweredAsks(delegationID)
	if err != nil {
		writeDelegationStoreError(w, err)
		return true
	}
	if len(asks) == 0 {
		return false
	}
	answerable := make([]delegation.Message, 0, len(asks))
	for _, ask := range asks {
		if ask.DeliveredAt != nil && s.hasAnswerWaiter(delegationID, ask.ID) {
			answerable = append(answerable, ask)
		}
	}
	if len(answerable) == 0 {
		return false
	}
	if len(answerable) > 1 {
		writeFollowupError(w, http.StatusConflict, "ambiguous-ask: multiple unanswered asks; use tcm answer -ask <id>")
		return true
	}
	if _, failure := s.appendDelegationAnswer(delegationID, answerable[0].ID, text); failure != nil {
		writeFollowupError(w, failure.status, failure.message)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(followupResponse{Mode: "answer", AskID: answerable[0].ID})
	return true
}

func (s *Server) resolveFollowupDelegation(req followupRequest) (string, *agentResolutionError) {
	if s.Delegations == nil {
		return "", &agentResolutionError{status: http.StatusNotFound, message: "delegation not found"}
	}
	record, err := s.Delegations.Get(req.Delegation)
	if err != nil {
		if errors.Is(err, delegation.ErrNotFound) {
			return "", &agentResolutionError{status: http.StatusNotFound, message: "delegation not found"}
		}
		return "", &agentResolutionError{status: http.StatusInternalServerError, message: "could not read delegation"}
	}
	if req.Session != "" && req.Session != record.SessionName {
		return "", &agentResolutionError{status: http.StatusBadRequest, message: "delegation and session identify different agents"}
	}
	if req.Pane != "" && req.Pane != record.PaneID {
		return "", &agentResolutionError{status: http.StatusBadRequest, message: "delegation and pane identify different agents"}
	}
	if record.ThreadID != "" && req.Thread != "" && req.Thread != record.ThreadID {
		return "", &agentResolutionError{status: http.StatusBadRequest, message: "delegation and thread identify different agents"}
	}
	return record.DelegationID, nil
}

func followupStatusConflict(status string) string {
	switch status {
	case wire.StatusRunning:
		return "session is running; refusing to interrupt it"
	case wire.StatusWaiting:
		return "session is waiting for input; refusing to interrupt it"
	default:
		return ""
	}
}

func (s *Server) followupState(session, threadID, paneID string) (*wire.AgentEvent, *agentResolutionError) {
	return s.resolveTrackedEvent(session, threadID, paneID)
}

func (s *Server) resolveFollowupTarget(req followupRequest) (followupTarget, *agentResolutionError) {
	if req.Delegation == "" {
		state, resolveErr := s.followupState(req.Session, req.Thread, req.Pane)
		return followupTarget{Session: req.Session, State: state}, resolveErr
	}
	if s.Delegations == nil {
		return followupTarget{}, &agentResolutionError{status: http.StatusNotFound, message: "delegation not found"}
	}
	record, err := s.Delegations.Get(req.Delegation)
	if err != nil {
		if errors.Is(err, delegation.ErrNotFound) {
			return followupTarget{}, &agentResolutionError{status: http.StatusNotFound, message: "delegation not found"}
		}
		return followupTarget{}, &agentResolutionError{status: http.StatusInternalServerError, message: "could not read delegation"}
	}
	target := followupTarget{Session: record.SessionName, Bound: record.ThreadID != ""}
	if req.Session != "" && req.Session != record.SessionName {
		return target, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and session identify different agents"}
	}
	if req.Pane != "" && req.Pane != record.PaneID {
		return target, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and pane identify different agents"}
	}
	if record.ThreadID == "" {
		state, resolveErr := s.followupState(record.SessionName, "", record.PaneID)
		if resolveErr != nil {
			return target, resolveErr
		}
		if state == nil {
			return target, nil
		}
		if req.Thread != "" && state.ThreadID != req.Thread {
			return target, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and thread identify different agents"}
		}
		target.State = state
		return target, nil
	}
	if req.Thread != "" && req.Thread != record.ThreadID {
		return target, &agentResolutionError{status: http.StatusBadRequest, message: "delegation and thread identify different agents"}
	}
	target.State = &wire.AgentEvent{
		Session:  record.SessionName,
		Agent:    record.Agent,
		ThreadID: record.ThreadID,
		PaneID:   record.PaneID,
	}
	tracked, resolveErr := s.followupState(record.SessionName, record.ThreadID, "")
	if resolveErr == nil && tracked != nil {
		if tracked.Status == "" {
			return target, &agentResolutionError{status: http.StatusConflict, message: "cannot verify delegate is idle; refusing to interrupt"}
		}
		target.State.Status = tracked.Status
		return target, nil
	}
	if resolveErr != nil && resolveErr.status != http.StatusNotFound {
		return target, resolveErr
	}
	response := resultResponse{Session: record.SessionName, ThreadID: record.ThreadID, Cwd: record.Cwd}
	found, inspectErr := s.populateDiskResult(&response, record.Agent)
	if inspectErr != nil || !found || response.Status == "" {
		return target, &agentResolutionError{status: http.StatusConflict, message: "cannot verify delegate is idle; refusing to interrupt"}
	}
	target.State.Status = response.Status
	return target, nil
}

func (s *Server) followupDir(session, paneID string) string {
	if s.Builder == nil || s.Builder.Tmux == nil || paneID == "" {
		return ""
	}
	for _, pane := range s.Builder.Tmux.ListAllPanes() {
		if pane.ID == paneID && pane.Session == session {
			return pane.Dir
		}
	}
	return ""
}

func writeFollowupMessage(session, message string) (string, error) {
	cleanupOldFollowupMessages(os.TempDir(), time.Now())
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, session)
	file, err := os.CreateTemp("", "tcm-followup-"+name+"-*.md")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err = file.WriteString(message); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func cleanupOldFollowupMessages(dir string, now time.Time) {
	matches, _ := filepath.Glob(filepath.Join(dir, "tcm-followup-*"))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(path)
		}
	}
}

func writeFollowupError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
