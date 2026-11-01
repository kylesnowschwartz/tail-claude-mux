package ccwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const (
	lineUser          = `{"type":"user","message":{"role":"user","content":"why is the build thinking so long"}}`
	lineThinking      = `{"type":"assistant","message":{"role":"assistant","id":"m1","content":[{"type":"thinking","thinking":"private","signature":"sig"}]}}`
	lineRedacted      = `{"type":"assistant","message":{"role":"assistant","id":"m2","content":[{"type":"redacted_thinking","data":"xyz"}]}}`
	lineThinkingTools = `{"type":"assistant","message":{"role":"assistant","id":"m3","content":[{"type":"thinking","thinking":""},{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Read"}]}}`
	lineToolUse       = `{"type":"assistant","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","name":"Bash"}]}}`
	lineSidechain     = `{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"x"}]}}`
	lineToolResult    = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"{\"type\":\"thinking\"}"}]}}`
)

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestCountThinkingBlocks(t *testing.T) {
	lines := strings.Join([]string{
		lineUser, lineThinking, lineToolUse, lineRedacted, lineThinkingTools, lineSidechain, lineToolResult, "not json thinking\"",
	}, "\n") + "\n"
	if got := countThinkingBlocks([]byte(lines)); got != 3 {
		t.Fatalf("countThinkingBlocks = %d, want 3 (thinking, redacted_thinking, thinking before tools)", got)
	}
}

func TestThinkingCursor(t *testing.T) {
	t.Run("history before tracking is skipped, new blocks count once", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sess-1.jsonl")
		appendLines(t, path, lineUser, lineThinking, lineToolUse)
		var c thinkingCursor
		c.trackTranscript(path, "sess-1")
		if got := c.newThinkingBlocks(); got != 0 {
			t.Fatalf("first read = %d, want 0 (history)", got)
		}
		appendLines(t, path, lineThinking, lineToolUse, lineThinkingTools)
		if got := c.newThinkingBlocks(); got != 2 {
			t.Fatalf("second read = %d, want 2", got)
		}
		if got := c.newThinkingBlocks(); got != 0 {
			t.Fatalf("re-read = %d, want 0 (no double count)", got)
		}
	})

	t.Run("a file created after tracking is read from the start", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sess-1.jsonl")
		var c thinkingCursor
		c.trackTranscript(path, "sess-1")
		appendLines(t, path, lineUser, lineThinking)
		if got := c.newThinkingBlocks(); got != 1 {
			t.Fatalf("read = %d, want 1", got)
		}
	})

	t.Run("a partial trailing line waits for its newline", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sess-1.jsonl")
		var c thinkingCursor
		c.trackTranscript(path, "sess-1")
		half := len(lineThinking) / 2
		appendRaw(t, path, lineThinking[:half])
		if got := c.newThinkingBlocks(); got != 0 {
			t.Fatalf("partial read = %d, want 0", got)
		}
		appendRaw(t, path, lineThinking[half:]+"\n")
		if got := c.newThinkingBlocks(); got != 1 {
			t.Fatalf("completed read = %d, want 1", got)
		}
	})

	t.Run("a truncated file restarts at its end", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sess-1.jsonl")
		var c thinkingCursor
		c.trackTranscript(path, "sess-1")
		appendLines(t, path, lineThinking, lineThinking)
		c.newThinkingBlocks()
		if err := os.WriteFile(path, []byte(lineUser+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := c.newThinkingBlocks(); got != 0 {
			t.Fatalf("after truncation = %d, want 0", got)
		}
		appendLines(t, path, lineThinking)
		if got := c.newThinkingBlocks(); got != 1 {
			t.Fatalf("after truncation and append = %d, want 1", got)
		}
	})

	t.Run("paths that are not the thread's transcript are ignored", func(t *testing.T) {
		dir := t.TempDir()
		for _, p := range []string{
			"sess-1.jsonl",                          // relative
			filepath.Join(dir, "other.jsonl"),       // another thread's file
			filepath.Join(dir, "sess-1.jsonl.bak"),  // not the transcript
			filepath.Join(dir, "sess-1", "x.jsonl"), // inside the thread dir
		} {
			var c thinkingCursor
			c.trackTranscript(p, "sess-1")
			if c.path != "" {
				t.Errorf("trackTranscript(%q) kept the path", p)
			}
		}
	})
}

// The adapter reports each thinking block once, on the next emitted event,
// and an incremental re-read after more hooks does not report it again.
func TestAdapterEmitsThinkingBlocks(t *testing.T) {
	tc := makeCtx(map[string]string{"/tmp/myproject": "myproject"}, nil)
	a := newStartedAdapter(t, tc)
	path := filepath.Join(t.TempDir(), "sess-1.jsonl")
	appendLines(t, path, lineThinking) // before tcm saw the thread

	withPath := func(p wire.HookPayload) wire.HookPayload {
		p.TranscriptPath = path
		return p
	}
	readInput := map[string]json.RawMessage{"file_path": json.RawMessage(`"/tmp/foo.ts"`)}

	a.HandleHook(withPath(hook("UserPromptSubmit", "sess-1", "/tmp/myproject")))
	appendLines(t, path, lineThinking, lineToolUse)
	a.HandleHook(withPath(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read", readInput)))
	a.HandleHook(withPath(hook("PostToolUse", "sess-1", "/tmp/myproject"))) // deduped, no emit
	appendLines(t, path, lineThinking, lineRedacted)
	a.HandleHook(withPath(toolHook("PreToolUse", "sess-1", "/tmp/myproject", "Read", readInput)))
	a.HandleHook(withPath(hook("Stop", "sess-1", "/tmp/myproject")))

	wantLen(t, tc.events, 4)
	want := []int{0, 1, 2, 0}
	for i, w := range want {
		if got := tc.events[i].ThinkingBlocks; got != w {
			t.Errorf("events[%d].ThinkingBlocks = %d, want %d", i, got, w)
		}
	}
}
