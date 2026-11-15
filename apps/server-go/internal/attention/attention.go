// Package attention derives, for each Claude Code conversation, the one
// reason it needs the engineer and when that reason started. A reason
// starts at one hook event and ends at a later hook event or when the
// engineer focuses the conversation's pane. No timer ends a reason.
package attention

// Reason names.
const (
	Permission = "permission" // a permission question is open
	Question   = "question"   // the AskUserQuestion tool is open
	Input      = "input"      // Claude Code reports the conversation waiting at its prompt
	Error      = "error"      // the turn ended with an error
	Finished   = "finished"   // a turn ended and the engineer has not acted on it
)

// askUserQuestion is Claude Code's question tool.
const askUserQuestion = "AskUserQuestion"

// Reason is a conversation's current attention reason. The zero value
// means no reason.
type Reason struct {
	Name    string
	SinceMS int64 // epoch milliseconds the reason started
}

// Event is the part of a hook payload the transition reads.
type Event struct {
	Hook             string // hook event name, such as "Stop"
	ToolName         string
	NotificationType string
}

// Next returns the reason that follows cur after the hook event at nowMS.
// A reason that starts takes nowMS as its start time; a kept reason keeps
// its start time. SessionEnd is the store's concern (Forget) and yields no
// reason here.
func Next(cur Reason, ev Event, nowMS int64) Reason {
	start := func(name string) Reason { return Reason{Name: name, SinceMS: nowMS} }
	switch ev.Hook {
	case "PermissionRequest":
		if ev.ToolName == askUserQuestion {
			// Claude Code posts PreToolUse and then PermissionRequest for the
			// same question; the question keeps its start time.
			if cur.Name == Question {
				return cur
			}
			return start(Question)
		}
		return start(Permission)
	case "PreToolUse":
		if ev.ToolName == askUserQuestion {
			return start(Question)
		}
		return cur
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		if cur.Name == Permission || cur.Name == Question && ev.ToolName == askUserQuestion {
			return Reason{}
		}
		return cur
	case "Stop":
		return start(Finished)
	case "StopFailure":
		return start(Error)
	case "Notification":
		if ev.NotificationType == "idle_prompt" && cur.Name == Finished {
			return start(Input)
		}
		return cur
	case "UserPromptSubmit", "SessionStart", "SessionEnd":
		return Reason{}
	}
	return cur
}

// WhileFocused is the reason after an event that would start finished,
// input, or error while the engineer looks at the conversation's pane: the
// end of a turn leaves no reason, and an idle_prompt notification keeps the
// current one.
func WhileFocused(cur Reason, ev Event) Reason {
	if ev.Hook == "Notification" {
		return cur
	}
	return Reason{}
}

// endsOnFocus reports whether focusing the conversation's pane ends the
// reason. An open permission question or question tool stays open.
func endsOnFocus(name string) bool {
	return name == Finished || name == Input || name == Error
}

// Store holds the current reason per conversation, keyed by thread ID.
// It is not safe for concurrent use; callers hold the server lock.
type Store struct {
	reasons map[string]Reason
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{reasons: map[string]Reason{}}
}

// Apply runs the transition for one hook event and reports whether the
// reason or its start time changed. focused, when not nil, reports whether
// the engineer is looking at the conversation's pane; it is asked only when
// the event would start finished, input, or error, and a focused pane
// starts none of them (see WhileFocused).
func (s *Store) Apply(threadID string, ev Event, nowMS int64, focused func() bool) bool {
	if threadID == "" {
		return false
	}
	cur := s.reasons[threadID]
	next := Next(cur, ev, nowMS)
	if next != cur && endsOnFocus(next.Name) && focused != nil && focused() {
		next = WhileFocused(cur, ev)
	}
	if next == cur {
		return false
	}
	if next.Name == "" {
		delete(s.reasons, threadID)
	} else {
		s.reasons[threadID] = next
	}
	return true
}

// EndOnFocus ends the thread's reason when focus ends it, and reports
// whether it did.
func (s *Store) EndOnFocus(threadID string) bool {
	cur, ok := s.reasons[threadID]
	if !ok || !endsOnFocus(cur.Name) {
		return false
	}
	delete(s.reasons, threadID)
	return true
}

// Get returns the thread's current reason; the zero Reason when it has none.
func (s *Store) Get(threadID string) Reason {
	return s.reasons[threadID]
}

// Forget drops the thread and reports whether it had a reason.
func (s *Store) Forget(threadID string) bool {
	_, ok := s.reasons[threadID]
	delete(s.reasons, threadID)
	return ok
}

// Restore rebuilds a reason from a status that state rebuilt from disk:
// "waiting" becomes permission and "error" becomes error, each starting at
// sinceMS. A thread that holds a reason keeps it. Reports whether it set one.
func (s *Store) Restore(threadID, status string, sinceMS int64) bool {
	if threadID == "" {
		return false
	}
	if _, held := s.reasons[threadID]; held {
		return false
	}
	var name string
	switch status {
	case "waiting":
		name = Permission
	case "error":
		name = Error
	default:
		return false
	}
	s.reasons[threadID] = Reason{Name: name, SinceMS: sinceMS}
	return true
}
