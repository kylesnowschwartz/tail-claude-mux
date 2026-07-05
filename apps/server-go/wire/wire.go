// Package wire freezes tcm's client↔server JSON contract for the Go
// backend. It is a field-for-field port of packages/runtime/src/shared.ts
// and contracts/agent.ts — the opentui TUI, scripts/hook.sh, and the tmux
// plugin scripts all speak these shapes, and the Go server must emit and
// accept them byte-compatibly.
//
// The TypeScript source is the contract of record while both servers
// exist; changes land there first and are mirrored here. The golden
// fixture under testdata/ was captured from the live bun server
// (GET /state) and the tests decode it with DisallowUnknownFields, so a
// field added on the TS side fails these tests instead of drifting
// silently.
package wire

import (
	"encoding/json"
	"fmt"
)

// Server address constants, mirroring shared.ts.
const (
	ServerPort = 7391
	ServerHost = "127.0.0.1"
	PIDFile    = "/tmp/tcm.pid"
)

// AgentStatus values (shared.ts AgentStatus). Terminal states are
// StatusDone, StatusError, and StatusInterrupted.
const (
	StatusIdle        = "idle"
	StatusRunning     = "running"
	StatusDone        = "done"
	StatusError       = "error"
	StatusWaiting     = "waiting"
	StatusInterrupted = "interrupted"
)

// IsTerminalStatus reports whether a status is terminal (contracts/agent.ts
// TERMINAL_STATUSES): the turn is over and the row is a candidate for the
// prune tiers.
func IsTerminalStatus(status string) bool {
	return status == StatusDone || status == StatusError || status == StatusInterrupted
}

// AgentLiveness values (contracts/agent.ts AgentLiveness).
const (
	LivenessAlive   = "alive"
	LivenessExited  = "exited"
	LivenessUnknown = "unknown"
)

// ServerMessage type discriminators (shared.ts ServerMessage union).
const (
	TypeState      = "state"
	TypeFocus      = "focus"
	TypeResize     = "resize"
	TypeQuit       = "quit"
	TypeYourSess   = "your-session"
	TypeReIdentify = "re-identify"
	TypePaneFocus  = "pane-focus"
)

// AgentEvent is contracts/agent.ts AgentEvent: one agent instance's state
// as tracked by the server and serialized to the TUI. Numeric fields where
// 0 is a valid value (window/pane index) are pointers so absence survives
// a round-trip; the rest use omitempty because 0/""/false and absence are
// equivalent to consumers.
type AgentEvent struct {
	Agent           string `json:"agent"`
	Session         string `json:"session"`
	Status          string `json:"status"`
	TS              int64  `json:"ts"`
	FirstSeenTS     int64  `json:"firstSeenTs,omitempty"`
	ThreadID        string `json:"threadId,omitempty"`
	ThreadName      string `json:"threadName,omitempty"`
	Unseen          bool   `json:"unseen,omitempty"`
	PaneID          string `json:"paneId,omitempty"`
	WindowIndex     *int   `json:"windowIndex,omitempty"`
	PaneIndex       *int   `json:"paneIndex,omitempty"`
	Liveness        string `json:"liveness,omitempty"`
	PID             int    `json:"pid,omitempty"`
	ToolDescription string `json:"toolDescription,omitempty"`
	// ToolVerb is the structured verb for ToolDescription (shared.ts
	// MetadataVerb), derived from the tool name by the watcher so renderers
	// don't regex-guess it back out of the message. Same lifecycle as
	// ToolDescription.
	ToolVerb string `json:"toolVerb,omitempty"`
	// ToolInvoked marks this event as the START of a new tool call, set by
	// watchers on their tool-start signal (ccwatch: PreToolUse/
	// PermissionRequest; piwatch: tool_execution_start). The activity log
	// appends a tool entry only when it is set, so repeated identical calls
	// each count while echoes of one call (PermissionRequest → approved
	// PreToolUse, PostToolUse keeping the description) don't double-log.
	ToolInvoked bool   `json:"toolInvoked,omitempty"`
	PaneTitle   string `json:"paneTitle,omitempty"`
	Subagent    string `json:"subagent,omitempty"`
	Ended       bool   `json:"ended,omitempty"`
}

// MetadataStatus is shared.ts MetadataStatus (programmatic API).
type MetadataStatus struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	TS   int64  `json:"ts"`
}

// MetadataProgress is shared.ts MetadataProgress. All three numeric fields
// are optional on the TS side; pointers preserve absent-vs-zero.
type MetadataProgress struct {
	Current *float64 `json:"current,omitempty"`
	Total   *float64 `json:"total,omitempty"`
	Percent *float64 `json:"percent,omitempty"`
	Label   string   `json:"label,omitempty"`
	TS      int64    `json:"ts"`
}

// MetadataLogEntry is shared.ts MetadataLogEntry. Verb is the shared.ts
// MetadataVerb union (read/list/search/edit/run/web/task/skill/thinking/
// error) — producer-tagged so the TUI doesn't regex-guess the verb back
// out of Message; empty when the producer only has free text.
type MetadataLogEntry struct {
	Message string `json:"message"`
	Tone    string `json:"tone,omitempty"`
	Source  string `json:"source,omitempty"`
	Verb    string `json:"verb,omitempty"`
	TS      int64  `json:"ts"`
}

// SessionMetadata is shared.ts SessionMetadata.
type SessionMetadata struct {
	Status   *MetadataStatus    `json:"status"`
	Progress *MetadataProgress  `json:"progress"`
	Logs     []MetadataLogEntry `json:"logs"`
}

// SessionData is shared.ts SessionData: one mux session row.
type SessionData struct {
	Name            string           `json:"name"`
	CreatedAt       int64            `json:"createdAt"`
	Dir             string           `json:"dir"`
	Branch          string           `json:"branch"`
	Dirty           bool             `json:"dirty"`
	IsWorktree      bool             `json:"isWorktree"`
	Unseen          bool             `json:"unseen"`
	Panes           int              `json:"panes"`
	Windows         int              `json:"windows"`
	Uptime          string           `json:"uptime"`
	AgentState      *AgentEvent      `json:"agentState"`
	Agents          []AgentEvent     `json:"agents"`
	EventTimestamps []int64          `json:"eventTimestamps"`
	Metadata        *SessionMetadata `json:"metadata,omitempty"`
}

// ServerState is the full-state broadcast (shared.ts ServerState). Theme is
// deliberately opaque: the TS side types it string | PartialTheme |
// undefined and the client passes it straight to resolveTheme(), so the Go
// server forwards whatever it loaded without modeling the palette.
type ServerState struct {
	Type           string          `json:"type"`
	Sessions       []SessionData   `json:"sessions"`
	FocusedSession *string         `json:"focusedSession"`
	CurrentSession *string         `json:"currentSession"`
	Theme          json.RawMessage `json:"theme,omitempty"`
	SidebarWidth   int             `json:"sidebarWidth"`
	TS             int64           `json:"ts"`
}

// FocusUpdate is shared.ts FocusUpdate.
type FocusUpdate struct {
	Type           string  `json:"type"`
	FocusedSession *string `json:"focusedSession"`
	CurrentSession *string `json:"currentSession"`
}

// ResizeNotify is shared.ts ResizeNotify.
type ResizeNotify struct {
	Type  string `json:"type"`
	Width int    `json:"width"`
}

// QuitNotify is shared.ts QuitNotify.
type QuitNotify struct {
	Type string `json:"type"`
}

// YourSession is shared.ts YourSession.
type YourSession struct {
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	ClientTTY *string `json:"clientTty"`
}

// ReIdentify is shared.ts ReIdentify.
type ReIdentify struct {
	Type string `json:"type"`
}

// PaneFocusUpdate is shared.ts PaneFocusUpdate.
type PaneFocusUpdate struct {
	Type   string `json:"type"`
	PaneID string `json:"paneId"`
}

// DecodeServerMessage decodes one WebSocket text frame from the server
// into its concrete message type, dispatching on the "type" discriminator.
// Unknown discriminators return an error — the caller decides whether that
// is fatal (contract tests) or skippable (a tolerant client).
func DecodeServerMessage(data []byte) (any, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, err
	}
	switch head.Type {
	case TypeState:
		return decodeAs[ServerState](data)
	case TypeFocus:
		return decodeAs[FocusUpdate](data)
	case TypeResize:
		return decodeAs[ResizeNotify](data)
	case TypeQuit:
		return decodeAs[QuitNotify](data)
	case TypeYourSess:
		return decodeAs[YourSession](data)
	case TypeReIdentify:
		return decodeAs[ReIdentify](data)
	case TypePaneFocus:
		return decodeAs[PaneFocusUpdate](data)
	default:
		return nil, fmt.Errorf("wire: unknown server message type %q", head.Type)
	}
}

func decodeAs[T any](data []byte) (T, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

// ClientCommand type discriminators (shared.ts ClientCommand union).
const (
	CmdSwitchSession  = "switch-session"
	CmdSwitchIndex    = "switch-index"
	CmdNewSession     = "new-session"
	CmdHideSession    = "hide-session"
	CmdShowAll        = "show-all-sessions"
	CmdKillSession    = "kill-session"
	CmdReorderSession = "reorder-session"
	CmdRefresh        = "refresh"
	CmdMoveFocus      = "move-focus"
	CmdFocusSession   = "focus-session"
	CmdMarkSeen       = "mark-seen"
	CmdDismissAgent   = "dismiss-agent"
	CmdSetTheme       = "set-theme"
	CmdIdentify       = "identify"
	CmdQuit           = "quit"
	CmdIdentifyPane   = "identify-pane"
	CmdFocusAgentPane = "focus-agent-pane"
	CmdKillAgentPane  = "kill-agent-pane"
	CmdReportWidth    = "report-width"
	CmdEqualizeWidth  = "equalize-width"
)

// ClientCommand is the flattened union of shared.ts ClientCommand: every
// variant's fields, discriminated by Type. Which fields are meaningful for
// which Type follows the TS union; a Go handler switches on Type and reads
// only that variant's fields.
type ClientCommand struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	ClientTTY   string `json:"clientTty,omitempty"`
	Index       *int   `json:"index,omitempty"`
	Delta       int    `json:"delta,omitempty"`
	Session     string `json:"session,omitempty"`
	Agent       string `json:"agent,omitempty"`
	ThreadID    string `json:"threadId,omitempty"`
	ThreadName  string `json:"threadName,omitempty"`
	PaneID      string `json:"paneId,omitempty"`
	PID         *int   `json:"pid,omitempty"`
	Theme       string `json:"theme,omitempty"`
	SessionName string `json:"sessionName,omitempty"`
	Width       int    `json:"width,omitempty"`
}
