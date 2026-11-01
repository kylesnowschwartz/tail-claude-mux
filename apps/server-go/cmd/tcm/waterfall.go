package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/waterfall"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const (
	waterfallPollInterval  = time.Second
	waterfallFetchTimeout  = 500 * time.Millisecond
	waterfallFallbackWidth = 37
	waterfallFallbackRows  = 10
	// waterfallOfflineAfter is how many polls in a row must fail before the
	// view shows "tcm offline"; fewer failures keep the last good frame.
	waterfallOfflineAfter = 3
)

// waterfallSnapshot is what the waterfall view needs from GET /state.
type waterfallSnapshot struct {
	Logs    []wire.MetadataLogEntry
	Waiting int
	Palette waterfall.Palette
}

// decodeWaterfallState extracts activity logs from every session, the number
// of agents in the waiting status, and the theme palette from a /state body.
func decodeWaterfallState(body []byte) (waterfallSnapshot, error) {
	// Theme may be a plain string or an object; only the object has a palette.
	var raw struct {
		Sessions []wire.SessionData `json:"sessions"`
		Theme    json.RawMessage    `json:"theme"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return waterfallSnapshot{}, err
	}
	var theme struct {
		Palette map[string]string `json:"palette"`
	}
	if len(raw.Theme) > 0 && raw.Theme[0] == '{' {
		_ = json.Unmarshal(raw.Theme, &theme)
	}
	snap := waterfallSnapshot{Palette: theme.Palette}
	for _, s := range raw.Sessions {
		if s.Metadata != nil {
			snap.Logs = append(snap.Logs, s.Metadata.Logs...)
		}
		agents := s.Agents
		if len(agents) == 0 && s.AgentState != nil {
			agents = []wire.AgentEvent{*s.AgentState}
		}
		for _, a := range agents {
			if a.Status == wire.StatusWaiting {
				snap.Waiting++
			}
		}
	}
	return snap, nil
}

// waterfallFrame assembles lines into one redraw: cursor home, each line
// followed by erase-to-end-of-line, then erase below.
func waterfallFrame(lines []string) string {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
		b.WriteString("\x1b[K")
	}
	b.WriteString("\x1b[J")
	return b.String()
}

// offlineLines is the single-line view shown while the server is unreachable.
func offlineLines(pal waterfall.Palette) []string {
	if c, ok := pal["overlay0"]; ok && len(c) == 7 && c[0] == '#' {
		var r, g, b int
		if _, err := fmt.Sscanf(c[1:], "%02x%02x%02x", &r, &g, &b); err == nil {
			return []string{fmt.Sprintf("\x1b[38;2;%d;%d;%dmtcm offline\x1b[0m", r, g, b)}
		}
	}
	return []string{"tcm offline"}
}

// terminalSize returns the size of the terminal on fd via TIOCGWINSZ.
func terminalSize(fd uintptr) (cols, rows int, ok bool) {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.Col == 0 || ws.Row == 0 {
		return 0, 0, false
	}
	return int(ws.Col), int(ws.Row), true
}

func fetchWaterfallState(client *http.Client, url string) (waterfallSnapshot, error) {
	resp, err := client.Get(url)
	if err != nil {
		return waterfallSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return waterfallSnapshot{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return waterfallSnapshot{}, err
	}
	return decodeWaterfallState(body)
}

// runWaterfall draws the verb waterfall in the current terminal until
// interrupted.
func runWaterfall(args []string) int {
	fs := flag.NewFlagSet("waterfall", flag.ContinueOnError)
	port := fs.Int("port", defaultServerPort(), "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	url := baseURL(*port) + "/state"
	client := &http.Client{Timeout: waterfallFetchTimeout}

	out := os.Stdout
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
	restore := func() { fmt.Fprint(out, "\x1b[?25h\x1b[?1049l") }
	defer restore()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	defer signal.Stop(sigs)

	var snap waterfallSnapshot
	var haveState bool
	var failures int
	var lastFrame string
	draw := func() {
		w, h, ok := terminalSize(out.Fd())
		if !ok {
			w, h = waterfallFallbackWidth, waterfallFallbackRows+1
		}
		var lines []string
		if haveState {
			lines = waterfall.Render(snap.Logs, time.Now().UnixMilli(), w, h, snap.Waiting, snap.Palette)
		} else {
			lines = offlineLines(snap.Palette)
		}
		frame := waterfallFrame(lines)
		if frame == lastFrame {
			return
		}
		lastFrame = frame
		fmt.Fprint(out, frame)
	}
	poll := func() {
		s, err := fetchWaterfallState(client, url)
		if err != nil {
			failures++
			if failures >= waterfallOfflineAfter {
				haveState = false
			}
			return
		}
		snap, haveState, failures = s, true, 0
	}

	poll()
	draw()
	ticker := time.NewTicker(waterfallPollInterval)
	defer ticker.Stop()
	for {
		select {
		case sig := <-sigs:
			if sig == syscall.SIGWINCH {
				lastFrame = ""
				draw()
				continue
			}
			return 0
		case <-ticker.C:
			poll()
			draw()
		}
	}
}
