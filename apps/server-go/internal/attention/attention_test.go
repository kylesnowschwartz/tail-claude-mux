package attention

import "testing"

func ev(hook, tool, notif string) Event {
	return Event{Hook: hook, ToolName: tool, NotificationType: notif}
}

func TestReasonStartsAndEnds(t *testing.T) {
	cases := []struct {
		name  string
		setup []Event // applied before start
		start Event
		want  string
		end   Event
	}{
		{"permission", nil, ev("PermissionRequest", "Bash", ""), Permission, ev("PostToolUse", "Bash", "")},
		{"permission ends on denial", nil, ev("PermissionRequest", "Bash", ""), Permission, ev("PermissionDenied", "Bash", "")},
		{"permission ends on tool failure", nil, ev("PermissionRequest", "Bash", ""), Permission, ev("PostToolUseFailure", "Bash", "")},
		{"question", nil, ev("PreToolUse", "AskUserQuestion", ""), Question, ev("PostToolUse", "AskUserQuestion", "")},
		{"input", []Event{ev("Stop", "", "")}, ev("Notification", "", "idle_prompt"), Input, ev("UserPromptSubmit", "", "")},
		{"error", nil, ev("StopFailure", "", ""), Error, ev("UserPromptSubmit", "", "")},
		{"finished", nil, ev("Stop", "", ""), Finished, ev("UserPromptSubmit", "", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewStore()
			for _, e := range c.setup {
				s.Apply("t", e, 1, nil)
			}
			if !s.Apply("t", c.start, 10, nil) {
				t.Fatal("start reported no change")
			}
			if got := s.Get("t"); got != (Reason{c.want, 10}) {
				t.Fatalf("after start = %+v", got)
			}
			if !s.Apply("t", c.end, 20, nil) {
				t.Fatal("end reported no change")
			}
			if got := s.Get("t"); got != (Reason{}) {
				t.Fatalf("after end = %+v", got)
			}
		})
	}
}

func TestNewReasonReplacesOldWithNewStart(t *testing.T) {
	s := NewStore()
	s.Apply("t", ev("Stop", "", ""), 10, nil)
	s.Apply("t", ev("PermissionRequest", "Bash", ""), 20, nil)
	if got := s.Get("t"); got != (Reason{Permission, 20}) {
		t.Fatalf("got %+v", got)
	}
	s.Apply("t", ev("Stop", "", ""), 30, nil)
	if got := s.Get("t"); got != (Reason{Finished, 30}) {
		t.Fatalf("got %+v", got)
	}
}

func TestQuestionPermissionRequestKeepsQuestionAndStart(t *testing.T) {
	s := NewStore()
	s.Apply("t", ev("PreToolUse", "AskUserQuestion", ""), 10, nil)
	if s.Apply("t", ev("PermissionRequest", "AskUserQuestion", ""), 130, nil) {
		t.Fatal("PermissionRequest(AskUserQuestion) changed the question")
	}
	if got := s.Get("t"); got != (Reason{Question, 10}) {
		t.Fatalf("got %+v", got)
	}
	// Without a prior PreToolUse it starts the question.
	s2 := NewStore()
	s2.Apply("t", ev("PermissionRequest", "AskUserQuestion", ""), 50, nil)
	if got := s2.Get("t"); got != (Reason{Question, 50}) {
		t.Fatalf("got %+v", got)
	}
}

func TestIdlePromptOnlyReplacesFinished(t *testing.T) {
	s := NewStore()
	if s.Apply("t", ev("Notification", "", "idle_prompt"), 10, nil) || s.Get("t") != (Reason{}) {
		t.Fatalf("idle_prompt with no reason changed it: %+v", s.Get("t"))
	}
	s.Apply("t", ev("StopFailure", "", ""), 20, nil)
	if s.Apply("t", ev("Notification", "", "idle_prompt"), 30, nil) || s.Get("t") != (Reason{Error, 20}) {
		t.Fatalf("idle_prompt over error changed it: %+v", s.Get("t"))
	}
}

func TestToolResultsLeaveOtherReasons(t *testing.T) {
	s := NewStore()
	s.Apply("t", ev("PreToolUse", "AskUserQuestion", ""), 10, nil)
	if s.Apply("t", ev("PostToolUse", "Bash", ""), 20, nil) {
		t.Fatal("another tool's result ended the question")
	}
	s.Apply("t", ev("Stop", "", ""), 30, nil)
	if s.Apply("t", ev("PostToolUse", "Bash", ""), 40, nil) || s.Get("t").Name != Finished {
		t.Fatal("tool result ended finished")
	}
	if s.Apply("t", ev("PreToolUse", "Bash", ""), 50, nil) {
		t.Fatal("PreToolUse of another tool changed the reason")
	}
}

func TestFocusEndsOnlyFinishedInputError(t *testing.T) {
	for _, start := range []Event{ev("Stop", "", ""), ev("StopFailure", "", "")} {
		s := NewStore()
		s.Apply("t", start, 10, nil)
		if !s.EndOnFocus("t") || s.Get("t") != (Reason{}) {
			t.Fatalf("%s: focus did not end it", start.Hook)
		}
	}
	s := NewStore()
	s.Apply("t", ev("Stop", "", ""), 10, nil)
	s.Apply("t", ev("Notification", "", "idle_prompt"), 20, nil)
	if !s.EndOnFocus("t") {
		t.Fatal("focus did not end input")
	}
	for _, start := range []Event{ev("PermissionRequest", "Bash", ""), ev("PreToolUse", "AskUserQuestion", "")} {
		s := NewStore()
		s.Apply("t", start, 10, nil)
		if s.EndOnFocus("t") || s.Get("t").Name == "" {
			t.Fatalf("%s: focus ended it", start.Hook)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := NewStore()
	s.Apply("t", ev("Stop", "", ""), 10, nil)
	s.Apply("t", ev("SessionStart", "", ""), 20, nil)
	if s.Get("t") != (Reason{}) {
		t.Fatal("SessionStart kept the reason")
	}
	s.Apply("t", ev("Stop", "", ""), 30, nil)
	if !s.Forget("t") || s.Get("t") != (Reason{}) {
		t.Fatal("Forget kept the reason")
	}
}

func TestRestoreFromRebuiltStatus(t *testing.T) {
	s := NewStore()
	if !s.Restore("w", "waiting", 5) || s.Get("w") != (Reason{Permission, 5}) {
		t.Fatalf("waiting: %+v", s.Get("w"))
	}
	if !s.Restore("e", "error", 6) || s.Get("e") != (Reason{Error, 6}) {
		t.Fatalf("error: %+v", s.Get("e"))
	}
	for _, st := range []string{"done", "running", "idle", "interrupted"} {
		if s.Restore("x", st, 7) {
			t.Fatalf("%s restored a reason", st)
		}
	}
	s.Apply("h", ev("Stop", "", ""), 1, nil)
	if s.Restore("h", "error", 9) || s.Get("h") != (Reason{Finished, 1}) {
		t.Fatalf("held reason overwritten: %+v", s.Get("h"))
	}
}
