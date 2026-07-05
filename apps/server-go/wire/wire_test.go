package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The golden fixture is a live GET /state capture from the bun server.
// Decoding it with DisallowUnknownFields proves every field the real
// server emits is modeled here — a field added on the TS side fails this
// test instead of silently vanishing in the Go port.
//
// Theme is the one deliberate hole: it is json.RawMessage (opaque
// pass-through to the client's resolveTheme), so unknown keys inside the
// palette are invisible by design.
func TestServerState_GoldenFixture_StrictDecode(t *testing.T) {
	data, err := os.ReadFile("testdata/state-live.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var st ServerState
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("live /state capture must decode strictly: %v", err)
	}

	if st.Type != TypeState {
		t.Errorf("type = %q, want %q", st.Type, TypeState)
	}
	if len(st.Sessions) == 0 {
		t.Fatal("fixture must contain sessions")
	}
	s := st.Sessions[0]
	if s.Name == "" || s.Dir == "" || s.Uptime == "" {
		t.Errorf("session identity fields must decode, got %+v", s)
	}
	if s.AgentState == nil || s.AgentState.Status == "" || s.AgentState.PID == 0 {
		t.Errorf("agentState must decode with status and pid, got %+v", s.AgentState)
	}
	if len(s.Agents) == 0 || s.Agents[0].ThreadID == "" {
		t.Errorf("agents must decode with threadId, got %+v", s.Agents)
	}
	if s.Agents[0].WindowIndex == nil || s.Agents[0].PaneIndex == nil {
		t.Error("window/pane index must decode as present (pointer non-nil)")
	}
	if len(s.EventTimestamps) == 0 {
		t.Error("eventTimestamps must decode")
	}
	if len(st.Theme) == 0 {
		t.Error("theme must be carried opaquely, got empty RawMessage")
	}
}

// Encode→decode round-trip must be lossless at the struct level: what the
// Go server would emit re-decodes to the identical value.
func TestServerState_GoldenFixture_RoundTrip(t *testing.T) {
	data, err := os.ReadFile("testdata/state-live.json")
	if err != nil {
		t.Fatal(err)
	}
	var st ServerState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	var again ServerState
	if err := dec.Decode(&again); err != nil {
		t.Fatalf("re-decode of own encoding: %v", err)
	}
	// Compare encodings, not structs: the opaque Theme RawMessage keeps the
	// bun server's pretty-printed bytes on first decode and compacts on
	// encode, so the stable invariant is that our own encoding re-encodes
	// byte-identically.
	encodedAgain, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, encodedAgain) {
		t.Error("round-trip changed the encoding")
	}
}

func TestDecodeServerMessage_Dispatch(t *testing.T) {
	cases := []struct {
		json string
		want any
	}{
		{`{"type":"focus","focusedSession":"a","currentSession":null}`,
			FocusUpdate{Type: TypeFocus, FocusedSession: strPtr("a")}},
		{`{"type":"resize","width":42}`, ResizeNotify{Type: TypeResize, Width: 42}},
		{`{"type":"quit"}`, QuitNotify{Type: TypeQuit}},
		{`{"type":"your-session","name":"proj","clientTty":"/dev/ttys001"}`,
			YourSession{Type: TypeYourSess, Name: "proj", ClientTTY: strPtr("/dev/ttys001")}},
		{`{"type":"re-identify"}`, ReIdentify{Type: TypeReIdentify}},
		{`{"type":"pane-focus","paneId":"%12"}`, PaneFocusUpdate{Type: TypePaneFocus, PaneID: "%12"}},
	}
	for _, c := range cases {
		got, err := DecodeServerMessage([]byte(c.json))
		if err != nil {
			t.Errorf("%s: %v", c.json, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.json, got, c.want)
		}
	}

	if _, err := DecodeServerMessage([]byte(`{"type":"martian"}`)); err == nil {
		t.Error("unknown discriminator must error")
	}
	if _, err := DecodeServerMessage([]byte(`not json`)); err == nil {
		t.Error("non-JSON must error")
	}
}

func TestClientCommand_Decode(t *testing.T) {
	cases := []struct {
		json string
		want ClientCommand
	}{
		{`{"type":"switch-session","name":"proj","clientTty":"/dev/ttys001"}`,
			ClientCommand{Type: CmdSwitchSession, Name: "proj", ClientTTY: "/dev/ttys001"}},
		{`{"type":"switch-index","index":0}`,
			ClientCommand{Type: CmdSwitchIndex, Index: intPtr(0)}},
		{`{"type":"reorder-session","name":"proj","delta":-1}`,
			ClientCommand{Type: CmdReorderSession, Name: "proj", Delta: -1}},
		{`{"type":"dismiss-agent","session":"proj","agent":"claude-code","threadId":"t1","paneId":"%3","pid":123}`,
			ClientCommand{Type: CmdDismissAgent, Session: "proj", Agent: "claude-code", ThreadID: "t1", PaneID: "%3", PID: intPtr(123)}},
		{`{"type":"equalize-width"}`, ClientCommand{Type: CmdEqualizeWidth}},
	}
	for _, c := range cases {
		var got ClientCommand
		dec := json.NewDecoder(bytes.NewReader([]byte(c.json)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&got); err != nil {
			t.Errorf("%s: %v", c.json, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %#v\nwant %#v", c.json, got, c.want)
		}
	}
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }
