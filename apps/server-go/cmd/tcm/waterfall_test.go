package main

import (
	"os"
	"strings"
	"testing"
)

func TestDecodeWaterfallStateLiveFixture(t *testing.T) {
	body, err := os.ReadFile("../../wire/testdata/state-live.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := decodeWaterfallState(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Logs) == 0 {
		t.Error("no logs decoded")
	}
	if snap.Palette["blue"] != "#3ba5ff" {
		t.Errorf("palette blue = %q", snap.Palette["blue"])
	}
	if snap.Waiting != 0 {
		t.Errorf("waiting = %d, want 0", snap.Waiting)
	}
}

func TestDecodeWaterfallStateWaitingAgent(t *testing.T) {
	body := `{"sessions":[
	 {"name":"a","agents":[{"status":"waiting"},{"status":"done"}],"metadata":{"logs":[{"message":"x","verb":"run","ts":5}]}},
	 {"name":"b","agentState":{"status":"waiting"},"metadata":{"logs":[{"message":"y","verb":"edit","ts":6}]}},
	 {"name":"c","agents":[]}],
	 "theme":{"palette":{"overlay0":"#6c7086"}}}`
	snap, err := decodeWaterfallState([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Waiting != 2 || len(snap.Logs) != 2 || snap.Palette["overlay0"] != "#6c7086" {
		t.Errorf("snap = %+v", snap)
	}
}

func TestDecodeWaterfallStateStringTheme(t *testing.T) {
	snap, err := decodeWaterfallState([]byte(`{"sessions":[],"theme":"dark"}`))
	if err != nil || snap.Palette != nil {
		t.Errorf("snap=%+v err=%v", snap, err)
	}
	if _, err := decodeWaterfallState([]byte("nope")); err == nil {
		t.Error("expected error for bad JSON")
	}
}

func TestWaterfallFrame(t *testing.T) {
	got := waterfallFrame([]string{"a", "b"})
	if got != "\x1b[Ha\x1b[K\r\nb\x1b[K\x1b[J" {
		t.Errorf("frame = %q", got)
	}
}

func TestOfflineLines(t *testing.T) {
	if got := offlineLines(nil); len(got) != 1 || got[0] != "tcm offline" {
		t.Errorf("plain = %q", got)
	}
	got := offlineLines(map[string]string{"overlay0": "#6c7086"})
	if !strings.HasPrefix(got[0], "\x1b[38;2;108;112;134m") || !strings.Contains(got[0], "tcm offline") {
		t.Errorf("colored = %q", got)
	}
}
