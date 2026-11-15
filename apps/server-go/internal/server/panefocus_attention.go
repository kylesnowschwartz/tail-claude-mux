package server

// endAttentionOnFocusLocked ends the focus-ended attention reasons of the
// Claude Code conversations the tracker has in the pane, and reports
// whether it ended any. Runs with s.mu held.
func (s *Server) endAttentionOnFocusLocked(paneID string) bool {
	if s.Tracker == nil || s.Watcher == nil {
		return false
	}
	ended := false
	for _, threadID := range s.Tracker.ThreadIDsInPane("claude-code", paneID) {
		if s.Watcher.Attention.EndOnFocus(threadID) {
			ended = true
		}
	}
	return ended
}

// paneFocusedLocked reports whether an attached tmux client's current pane
// is the pane the tracker holds for the Claude Code thread. An unknown
// pane or a tmux error reads as not focused. Runs with s.mu held.
func (s *Server) paneFocusedLocked(session, threadID string) bool {
	if s.Tracker == nil || s.Builder == nil || s.Builder.Tmux == nil {
		return false
	}
	ev := s.Tracker.GetEvent(session, "claude-code", threadID, "")
	if ev == nil || ev.PaneID == "" {
		return false
	}
	panes, ok := s.Builder.Tmux.FocusedPaneIDs()
	return ok && panes[ev.PaneID]
}
