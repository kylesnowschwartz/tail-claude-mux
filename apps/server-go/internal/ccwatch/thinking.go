package ccwatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// maxThinkingScanBytes bounds one incremental transcript read. A larger gap
// (a huge tool result, or a cursor far behind) is skipped rather than read
// under the server lock; it loses at most that gap's thinking blocks.
const maxThinkingScanBytes = 8 << 20

// thinkingCursor follows one thread's transcript so each assistant thinking
// block is counted once. offset is the byte just past the last complete line
// read; a trailing partial line is read again on the next call.
type thinkingCursor struct {
	path   string
	offset int64
}

// trackTranscript records the thread's transcript path from a hook payload
// and starts the cursor at the file's current end: thinking written before
// tcm saw the thread is history. A file that does not exist yet starts at 0.
// The path must be absolute and name the thread's own <session_id>.jsonl;
// anything else is ignored. The first accepted path is kept.
func (c *thinkingCursor) trackTranscript(path, threadID string) {
	if c.path != "" || path == "" || !filepath.IsAbs(path) || filepath.Base(path) != threadID+".jsonl" {
		return
	}
	c.path = filepath.Clean(path)
	if info, err := os.Stat(c.path); err == nil {
		c.offset = info.Size()
	}
}

// newThinkingBlocks returns the number of assistant thinking blocks written
// to the transcript since the previous call, and advances past them.
func (c *thinkingCursor) newThinkingBlocks() int {
	if c.path == "" {
		return 0
	}
	f, err := os.Open(c.path)
	if err != nil {
		return 0
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0
	}
	size := info.Size()
	if size < c.offset || size-c.offset > maxThinkingScanBytes {
		c.offset = size
		return 0
	}
	if size == c.offset {
		return 0
	}
	buf := make([]byte, size-c.offset)
	n, err := f.ReadAt(buf, c.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0
	}
	buf = buf[:n]
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		return 0
	}
	c.offset += int64(end + 1)
	return countThinkingBlocks(buf[:end+1])
}

// thinkingEntry is the slice of a transcript line thinking detection reads.
type thinkingEntry struct {
	IsSidechain bool `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// countThinkingBlocks counts thinking and redacted_thinking content blocks in
// the main-chain assistant entries of complete JSONL lines.
func countThinkingBlocks(lines []byte) int {
	n := 0
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`thinking"`)) {
			continue
		}
		var e thinkingEntry
		if json.Unmarshal(line, &e) != nil || e.IsSidechain || e.Message.Role != "assistant" {
			continue
		}
		for _, c := range contentItems(e.Message.Content) {
			if c.Type == "thinking" || c.Type == "redacted_thinking" {
				n++
			}
		}
	}
	return n
}
