// tcm-server is the Go tcm backend (see .agent-history/SCOPING-go-backend.md
// at the repo root).
//
// Cutover contract with the bun tooling: the launcher and restart.sh treat
// this binary as a drop-in for `bun run apps/server/src/main.ts` — it binds
// the same port, writes the same /tmp/tcm.pid after a successful bind, and
// answers POST /restart by re-exec'ing itself in place (same pid, so the
// pid file stays true; sockets are CLOEXEC so the port frees for the fresh
// image).
//
// A/B usage against the live bun server:
//
//	go run ./cmd/tcm-server -port 7392
//	curl -s localhost:7392/state | jq .sessions[].name
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kylesnowschwartz/agent-ouija/claude/claudedir"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/panescan"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/piwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/server"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/theming"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

func main() {
	port := flag.Int("port", wire.ServerPort, "listen port (use a non-default port to A/B against the bun server)")
	refresh := flag.Duration("refresh", 2*time.Second, "state refresh interval")
	flag.Parse()

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("home dir: %v", err)
	}
	configDir := filepath.Join(home, ".config", "tcm")

	builder := &state.Builder{
		Tmux:         tmux.New(),
		Git:          gitinfo.NewCache(),
		Order:        sessionorder.Load(filepath.Join(configDir, "session-order.json")),
		ConfigDir:    configDir,
		SidebarWidth: state.LoadSidebarWidth(configDir),
	}

	var watcher *ccwatch.Adapter
	if root, err := claudedir.DefaultRoot(); err == nil {
		watcher = ccwatch.New(root.ProjectsDir(), root.SessionsDir())
	} else {
		log.Printf("claude root unavailable, hook watcher disabled: %v", err)
	}
	piWatcher := piwatch.New(filepath.Join(home, ".pi", "agent", "sessions"))

	srv := server.New(builder, tracker.New(), watcher, piWatcher, panescan.New())
	srv.Restart = restartInPlace
	srv.Quit = func() {
		_ = os.Remove(wire.PIDFile)
		os.Exit(0)
	}
	srv.ScriptsDir = resolveScriptsDir()
	srv.SidebarPosition = state.LoadSidebarPosition(configDir)

	themeLog := func(msg string, data map[string]any) { log.Printf("theming: %s %v", msg, data) }
	srv.Palette = theming.NewPaletteWriter(configDir, builder.Tmux, themeLog)
	srv.Header = theming.NewHeaderSync(builder.Tmux, theming.IsClawdInstalled(home), themeLog)
	srv.HeaderEnabled = theming.ReadHeaderEnabled(builder.Tmux)
	srv.Palette.Apply("server-boot")

	// TCM_RELOAD_TUI travels through the /restart self-exec: the previous
	// incarnation sets it so THIS one cycles the sidebar TUIs onto new
	// code. Consume it immediately — it must not survive into the next
	// restart by default.
	reloadTUI := os.Getenv("TCM_RELOAD_TUI") == "1"
	_ = os.Unsetenv("TCM_RELOAD_TUI")

	// Bind before the pid file: a failed bind (stale server still holding
	// the port) must not clobber the live server's pid record.
	addr := fmt.Sprintf("%s:%d", wire.ServerHost, *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	if *port == wire.ServerPort {
		writePidFile()
		cleanupOnSignal()
	}

	srv.StartWatchers()
	if *port == wire.ServerPort {
		go srv.BootstrapSidebars(reloadTUI)
	}
	go srv.Run(*refresh)

	log.Printf("tcm server (go) listening on %s", addr)
	if err := http.Serve(ln, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// writePidFile records this process for the launcher/stop tooling. Best
// effort — the tooling also checks the port.
func writePidFile() {
	if err := os.WriteFile(wire.PIDFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		log.Printf("pid file: %v", err)
	}
}

// cleanupOnSignal removes the pid file on SIGINT/SIGTERM, matching the bun
// server's cleanup path.
func cleanupOnSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		_ = os.Remove(wire.PIDFile)
		os.Exit(0)
	}()
}

// restartInPlace re-execs the current binary with the same argv — the
// POST /restart contract. Same pid (pid file stays valid); Go sockets are
// CLOEXEC, so the listener closes at exec and the fresh image binds anew.
// reloadTUI rides the environment into the next incarnation.
func restartInPlace(reloadTUI bool) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("restart: %v", err)
		return
	}
	if reloadTUI {
		_ = os.Setenv("TCM_RELOAD_TUI", "1")
	}
	log.Printf("restart: re-exec %s (reload-tui=%v)", exe, reloadTUI)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("restart: exec failed: %v", err)
	}
}

// resolveScriptsDir locates apps/tui/scripts (the sidebar TUI launcher):
// TCM_DIR when it actually holds start.sh, else relative to this binary
// (apps/server-go/bin/tcm-server → repo root is three levels up). The
// exe-relative fallback also covers a stale TCM_DIR — a dangling tpm
// symlink after a repo move burned us once. Empty disables sidebar
// bootstrap rather than spawning panes with a broken command.
func resolveScriptsDir() string {
	var roots []string
	if env := os.Getenv("TCM_DIR"); env != "" {
		roots = append(roots, env)
	}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(exe), "..", "..", ".."))
	}
	for _, root := range roots {
		dir := filepath.Join(root, "apps", "tui", "scripts")
		if _, err := os.Stat(filepath.Join(dir, "start.sh")); err == nil {
			return dir
		}
	}
	log.Printf("sidebar: start.sh not found under %v — bootstrap disabled", roots)
	return ""
}
