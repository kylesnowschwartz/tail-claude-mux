package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

type followupRequest struct {
	Session string `json:"session"`
	Message string `json:"message"`
	Pane    string `json:"pane,omitempty"`
	Thread  string `json:"thread,omitempty"`
}

type followupResponse struct {
	UUID        string `json:"uuid"`
	RolloutPath string `json:"rolloutPath"`
	MessageFile string `json:"messageFile"`
	PaneID      string `json:"paneId"`
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
	if strings.TrimSpace(req.Session) == "" || strings.TrimSpace(req.Message) == "" {
		writeFollowupError(w, http.StatusBadRequest, "session and message are required")
		return
	}
	s.followupMu.Lock()
	defer s.followupMu.Unlock()

	state, resolveErr := s.followupState(req.Session, req.Thread, req.Pane)
	if resolveErr != nil {
		writeFollowupError(w, resolveErr.status, resolveErr.message)
		return
	}
	if state == nil {
		writeFollowupError(w, http.StatusNotFound, "session not found")
		return
	}
	if state.Agent != "codex" {
		writeFollowupError(w, http.StatusUnprocessableEntity, "follow-up is only supported for codex sessions")
		return
	}
	if message := followupStatusConflict(state.Status); message != "" {
		writeFollowupError(w, http.StatusConflict, message)
		return
	}
	dir := s.followupDir(req.Session, state.PaneID)
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
	messageFile, err := writeFollowupMessage(req.Session, req.Message)
	if err != nil {
		writeFollowupError(w, http.StatusInternalServerError, "could not write follow-up message")
		return
	}
	current, resolveErr := s.followupState(req.Session, req.Thread, req.Pane)
	if resolveErr != nil {
		_ = os.Remove(messageFile)
		writeFollowupError(w, resolveErr.status, resolveErr.message)
		return
	}
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
	if currentDir := s.followupDir(req.Session, current.PaneID); currentDir != dir {
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
	_ = json.NewEncoder(w).Encode(followupResponse{UUID: uuid, RolloutPath: rolloutPath, MessageFile: messageFile, PaneID: state.PaneID})
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
