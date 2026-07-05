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
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/server"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const pidFile = "/tmp/tcm.pid" // shared.ts PID_FILE

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

	srv := server.New(builder, tracker.New(), watcher, panescan.New())
	srv.Restart = restartInPlace
	srv.Quit = func() {
		_ = os.Remove(pidFile)
		os.Exit(0)
	}

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
	go srv.Run(*refresh)

	log.Printf("tcm server (go) listening on %s", addr)
	if err := http.Serve(ln, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

// writePidFile records this process for the launcher/stop tooling. Best
// effort — the tooling also checks the port.
func writePidFile() {
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
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
		_ = os.Remove(pidFile)
		os.Exit(0)
	}()
}

// restartInPlace re-execs the current binary with the same argv — the
// POST /restart contract. Same pid (pid file stays valid); Go sockets are
// CLOEXEC, so the listener closes at exec and the fresh image binds anew.
func restartInPlace() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("restart: %v", err)
		return
	}
	log.Printf("restart: re-exec %s", exe)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("restart: exec failed: %v", err)
	}
}
