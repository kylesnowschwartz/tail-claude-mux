// Port of packages/runtime/test/sidebar-width-sync.test.ts. Subtest names
// keep the TS test names verbatim; the expected values are the spec.
package widthsync_test

import (
	"math"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/widthsync"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

// unbounded stands in for the TS calls that omit windowWidth entirely.
const unbounded = 0

func expectClamp(t *testing.T, width, windowWidth, want int) {
	t.Helper()
	if got := widthsync.Clamp(width, windowWidth); got != want {
		t.Errorf("Clamp(%d, %d) = %d, want %d", width, windowWidth, got, want)
	}
}

// TestClamp ports the describe("sidebar width sync") block.
func TestClamp(t *testing.T) {
	t.Run("clampSidebarWidth enforces minimum", func(t *testing.T) {
		expectClamp(t, 10, unbounded, widthsync.AbsoluteMin)
		expectClamp(t, 5, unbounded, widthsync.AbsoluteMin)
		expectClamp(t, 0, unbounded, widthsync.AbsoluteMin)
	})

	t.Run("clampSidebarWidth passes through values above minimum", func(t *testing.T) {
		expectClamp(t, 50, unbounded, 50)
		expectClamp(t, widthsync.AbsoluteMin, unbounded, widthsync.AbsoluteMin)
		expectClamp(t, 100, unbounded, 100)
	})

	t.Run("with windowWidth, clamps to 40% max", func(t *testing.T) {
		// 40% of 200 = 80
		expectClamp(t, 90, 200, 80)
		expectClamp(t, 80, 200, 80)
		expectClamp(t, 50, 200, 50)
	})

	t.Run("with small windowWidth, max wins over large values", func(t *testing.T) {
		// 40% of 100 = 40
		expectClamp(t, 60, 100, 40)
		expectClamp(t, 40, 100, 40)
		expectClamp(t, 30, 100, 30)
	})

	t.Run("without windowWidth, no max enforced", func(t *testing.T) {
		expectClamp(t, 500, unbounded, 500)
		expectClamp(t, 1000, unbounded, 1000)
	})

	t.Run("min boundary passes through exactly", func(t *testing.T) {
		expectClamp(t, widthsync.AbsoluteMin, unbounded, widthsync.AbsoluteMin)
	})

	t.Run("computed max boundary passes through exactly", func(t *testing.T) {
		windowWidth := 200
		maxWidth := int(math.Floor(float64(windowWidth) * widthsync.MaxWidthPercent))
		expectClamp(t, maxWidth, windowWidth, maxWidth)
	})

	t.Run("max takes precedence when window is very small", func(t *testing.T) {
		// 40% of 30 = 12, which is below AbsoluteMin.
		// Max wins because a 20-col sidebar in a 30-col window is unusable.
		expectClamp(t, 15, 30, 12)
		expectClamp(t, 25, 30, 12)
	})
}

// makeSession mirrors the TS makeSession helper: the default dir's leaf
// matches the session name so tests that only set `name` do not
// accidentally trigger dirMismatch. Callers override fields on the result.
func makeSession(name string) wire.SessionData {
	return wire.SessionData{
		Name:            name,
		CreatedAt:       0,
		Dir:             "/tmp/" + name,
		Branch:          "",
		Dirty:           false,
		IsWorktree:      false,
		Unseen:          false,
		Panes:           1,
		Windows:         1,
		Uptime:          "0s",
		AgentState:      nil,
		Agents:          []wire.AgentEvent{},
		EventTimestamps: []int64{},
	}
}

func expectMin(t *testing.T, sessions []wire.SessionData, want int) {
	t.Helper()
	if got := widthsync.ComputeMin(sessions); got != want {
		t.Errorf("ComputeMin(...) = %d, want %d", got, want)
	}
}

// All content widths include +2 for the focused card's border box.
// TestComputeMin ports the describe("computeMinSidebarWidth") block.
func TestComputeMin(t *testing.T) {
	t.Run("returns absolute minimum for empty session list", func(t *testing.T) {
		expectMin(t, []wire.SessionData{}, widthsync.AbsoluteMin)
	})

	t.Run("fits short session name: padL(1) + name + status(2) + padR(1) + border(2)", func(t *testing.T) {
		// "test" = 4 → content 8 + border 2 = 10, but floor is 15
		expectMin(t, []wire.SessionData{makeSession("test")}, widthsync.AbsoluteMin)
	})

	t.Run("fits a longer session name", func(t *testing.T) {
		// "my-cool-project" = 15 → 1 + 15 + 2 + 1 + 2 = 21
		expectMin(t, []wire.SessionData{makeSession("my-cool-project")}, 21)
	})

	t.Run("truncates name at 18 chars", func(t *testing.T) {
		// 25-char name truncated to 18 → 1 + 18 + 2 + 1 + 2 = 24
		longName := "aaaaaaaaaaaaaaaaaaaaaaaaa" // "a".repeat(25)
		expectMin(t, []wire.SessionData{makeSession(longName)}, 24)
	})

	t.Run("branch row can drive the width", func(t *testing.T) {
		// name "ab" = 2 → name row = 1 + 2 + 2 + 1 = 6
		// branch "feature/long" = 12 → branch row = 1 + 2 + 12 + 1 = 16
		// widest content = 16 + border 2 = 18
		s := makeSession("ab")
		s.Branch = "feature/long"
		expectMin(t, []wire.SessionData{s}, 18)
	})

	t.Run("dir mismatch widens the branch row by the glyph cells", func(t *testing.T) {
		// name "ab", dir leaf "claude" (≠ "ab") → mismatch glyph adds 2 cols.
		// branch row = 1 + 2 + 12 + 2 + 1 = 18 → + border 2 = 20.
		s := makeSession("ab")
		s.Branch = "feature/long"
		s.Dir = "/Users/k/Code/dotfiles/claude"
		expectMin(t, []wire.SessionData{s}, 20)
	})

	t.Run("matching dir leaf does not add mismatch cols", func(t *testing.T) {
		// dir leaf "ab" === name "ab" → no glyph, branch row stays 16 → + 2 = 18.
		s := makeSession("ab")
		s.Branch = "feature/long"
		s.Dir = "/Users/k/Code/ab"
		expectMin(t, []wire.SessionData{s}, 18)
	})

	t.Run("agent badge adds to name row", func(t *testing.T) {
		// "test-session" = 12, 2 alive agents → badge " ●2" = 3
		// collapsed name row = 1 + 12 + 3 + 2 + 1 = 19
		// expanded agent row "claude" = 6 + 6 + 0 = 12 (name row is widest)
		// + border 2 = 21
		s := makeSession("test-session")
		s.Agents = []wire.AgentEvent{
			{Agent: "claude", Session: "x", Status: wire.StatusRunning, TS: 0, Liveness: wire.LivenessAlive},
			{Agent: "amp", Session: "x", Status: wire.StatusRunning, TS: 0, Liveness: wire.LivenessAlive},
		}
		expectMin(t, []wire.SessionData{s}, 21)
	})

	t.Run("expanded agent row drives width for long agent names", func(t *testing.T) {
		// "claude-code" = 11 → agent row = 6 + 11 + 0 = 17 + border 2 = 19
		s := makeSession("short")
		s.Agents = []wire.AgentEvent{
			{Agent: "claude-code", Session: "x", Status: wire.StatusRunning, TS: 0, Liveness: wire.LivenessAlive},
		}
		expectMin(t, []wire.SessionData{s}, 19)
	})

	t.Run("thread ID adds 6 cols to agent row", func(t *testing.T) {
		// "claude-code" = 11, threadId present → 6 + 11 + 6 + 0 = 23 + border 2 = 25
		s := makeSession("short")
		s.Agents = []wire.AgentEvent{
			{Agent: "claude-code", Session: "x", Status: wire.StatusRunning, TS: 0, Liveness: wire.LivenessAlive, ThreadID: "52e9abcd"},
		}
		expectMin(t, []wire.SessionData{s}, 25)
	})

	t.Run("unseen agent adds 2 cols to agent row", func(t *testing.T) {
		// "claude-code" = 11, unseen → 6 + 11 + 2 = 19 + border 2 = 21
		s := makeSession("short")
		s.Agents = []wire.AgentEvent{
			{Agent: "claude-code", Session: "x", Status: wire.StatusDone, TS: 0, Liveness: wire.LivenessExited, Unseen: true},
		}
		expectMin(t, []wire.SessionData{s}, 21)
	})

	t.Run("unseen session badge adds to collapsed name row", func(t *testing.T) {
		// "test-session" = 12, 1 alive agent → badge " ●" = 2, unseen → " ●" = 2
		// collapsed name row = 1 + 12 + 2 + 2 + 2 + 1 = 20 + border 2 = 22
		s := makeSession("test-session")
		s.Unseen = true
		s.Agents = []wire.AgentEvent{
			{Agent: "claude", Session: "x", Status: wire.StatusWaiting, TS: 0, Liveness: wire.LivenessAlive},
		}
		expectMin(t, []wire.SessionData{s}, 22)
	})

	t.Run("exited agents still contribute to expanded row width", func(t *testing.T) {
		// Even exited agents render in the list — "claude" = 6 → 6+6 = 12
		// badge: only "claude" is alive (1) → " ●" = 2
		// name row "test-session" = 12 → 1+12+2+2+1 = 18 (widest content)
		// + border 2 = 20
		s := makeSession("test-session")
		s.Agents = []wire.AgentEvent{
			{Agent: "claude", Session: "x", Status: wire.StatusRunning, TS: 0, Liveness: wire.LivenessAlive},
			{Agent: "amp", Session: "x", Status: wire.StatusDone, TS: 0, Liveness: wire.LivenessExited},
		}
		expectMin(t, []wire.SessionData{s}, 20)
	})

	t.Run("uses widest session across multiple", func(t *testing.T) {
		sessions := []wire.SessionData{
			makeSession("a"),
			makeSession("test-session"), // 1+12+2+1 = 16 + border 2 = 18
			makeSession("b"),
		}
		expectMin(t, sessions, 18)
	})
}
