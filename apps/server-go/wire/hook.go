// Hook ingress contract: a port of
// packages/runtime/src/contracts/parse-hook-payload.ts with identical
// bounds and drop-on-malformed semantics.
package wire

import "encoding/json"

// Field bounds, mirroring parse-hook-payload.ts.
const (
	maxEventLen            = 128
	maxSessionIDLen        = 256
	maxAgentLen            = 64
	maxToolNameLen         = 128
	maxNotificationTypeLen = 64
	maxSessionNameLen      = 256
	maxStringLen           = 64 * 1024
	maxToolInputKeys       = 256
	maxProcessSnapshotLen  = 256 * 1024
)

var validStopReasons = map[string]bool{
	"stop": true, "length": true, "toolUse": true, "error": true, "aborted": true,
}

var validShutdownReasons = map[string]bool{
	"quit": true, "reload": true, "new": true, "resume": true, "fork": true,
}

// HookPayload is the validated POST /hook body (contracts/agent-watcher.ts
// HookPayload). Required fields are Event, SessionID, and Cwd; everything
// else is optional and zero-valued when absent or malformed.
type HookPayload struct {
	Event            string                     `json:"event"`
	SessionID        string                     `json:"session_id"`
	Cwd              string                     `json:"cwd"`
	Agent            string                     `json:"agent,omitempty"`
	ToolName         string                     `json:"tool_name,omitempty"`
	ToolInput        map[string]json.RawMessage `json:"tool_input,omitempty"`
	NotificationType string                     `json:"notification_type,omitempty"`
	SessionName      string                     `json:"session_name,omitempty"`
	ToolIsError      *bool                      `json:"tool_is_error,omitempty"`
	StopReason       string                     `json:"stop_reason,omitempty"`
	ErrorMessage     string                     `json:"error_message,omitempty"`
	ShutdownReason   string                     `json:"shutdown_reason,omitempty"`
	PID              int                        `json:"pid,omitempty"`
	ProcessSnapshot  string                     `json:"process_snapshot,omitempty"`
}

// ParseHookPayload validates one POST /hook body. It returns (payload,
// true) when the input matches the contract and (zero, false) otherwise.
// Semantics match parse-hook-payload.ts exactly:
//
//   - Required fields (event, session_id, cwd) must be non-empty strings
//     within bounds — any failure rejects the whole event. The caller
//     still answers 200: hook failures must never block the agent, so a
//     rejected payload is dropped, not 4xx'd.
//   - Optional fields are checked individually; a malformed optional
//     field is dropped (zero value) rather than rejecting the event, so a
//     future Claude Code release adding fields can't break ingestion.
//   - Event names are NOT allow-listed here; watchers ignore events they
//     don't map.
func ParseHookPayload(body []byte) (HookPayload, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return HookPayload{}, false
	}

	event, ok := requiredString(raw["event"], maxEventLen)
	if !ok {
		return HookPayload{}, false
	}
	sessionID, ok := requiredString(raw["session_id"], maxSessionIDLen)
	if !ok {
		return HookPayload{}, false
	}
	// Empty cwd is malformed: downstream session resolution would fail
	// anyway, and accepting it asymmetrically with event/session_id
	// invites confusion.
	cwd, ok := requiredString(raw["cwd"], maxStringLen)
	if !ok {
		return HookPayload{}, false
	}

	p := HookPayload{
		Event:            event,
		SessionID:        sessionID,
		Cwd:              cwd,
		Agent:            optBoundedString(raw["agent"], maxAgentLen),
		ToolName:         optBoundedString(raw["tool_name"], maxToolNameLen),
		ToolInput:        optPlainObject(raw["tool_input"], maxToolInputKeys),
		NotificationType: optBoundedString(raw["notification_type"], maxNotificationTypeLen),
		SessionName:      optBoundedString(raw["session_name"], maxSessionNameLen),
		ToolIsError:      optBool(raw["tool_is_error"]),
		StopReason:       optEnumString(raw["stop_reason"], validStopReasons),
		ErrorMessage:     optBoundedString(raw["error_message"], maxStringLen),
		ShutdownReason:   optEnumString(raw["shutdown_reason"], validShutdownReasons),
		PID:              optPositiveInt(raw["pid"]),
		ProcessSnapshot:  optBoundedString(raw["process_snapshot"], maxProcessSnapshotLen),
	}
	return p, true
}

func requiredString(data json.RawMessage, maxLen int) (string, bool) {
	var s string
	if data == nil || json.Unmarshal(data, &s) != nil {
		return "", false
	}
	if len(s) == 0 || len(s) > maxLen {
		return "", false
	}
	return s, true
}

// optBoundedString returns a bounded non-empty string, or "" when absent
// or malformed.
func optBoundedString(data json.RawMessage, maxLen int) string {
	var s string
	if data == nil || json.Unmarshal(data, &s) != nil {
		return ""
	}
	if len(s) == 0 || len(s) > maxLen {
		return ""
	}
	return s
}

// optPlainObject returns a JSON object with at most maxKeys keys, or nil.
// Arrays and scalars are malformed (a JSON object is the only shape that
// decodes into the map).
func optPlainObject(data json.RawMessage, maxKeys int) map[string]json.RawMessage {
	if data == nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil || m == nil || len(m) > maxKeys {
		return nil
	}
	return m
}

func optBool(data json.RawMessage) *bool {
	var b bool
	if data == nil || json.Unmarshal(data, &b) != nil {
		return nil
	}
	return &b
}

// optEnumString returns the string when it belongs to the allow-set, "" otherwise.
func optEnumString(data json.RawMessage, allowed map[string]bool) string {
	var s string
	if data == nil || json.Unmarshal(data, &s) != nil || !allowed[s] {
		return ""
	}
	return s
}

// optPositiveInt returns an integer pid > 1 (0/1 are kernel/init and never
// an agent process), or 0 when absent or malformed. Matches the TS
// validator's Number.isInteger check: a fractional number is malformed.
func optPositiveInt(data json.RawMessage) int {
	var f float64
	if data == nil || json.Unmarshal(data, &f) != nil {
		return 0
	}
	n := int(f)
	if float64(n) != f || n <= 1 {
		return 0
	}
	return n
}
