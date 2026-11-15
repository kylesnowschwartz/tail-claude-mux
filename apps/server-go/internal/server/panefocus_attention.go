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
