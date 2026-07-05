// tcm-server is the Go tcm backend (work in progress — see
// .agent-history/SCOPING-go-backend.md at the repo root).
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
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/gitinfo"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/server"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/sessionorder"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/state"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux"
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

	srv := server.New(builder)
	go srv.Run(*refresh)

	addr := fmt.Sprintf("%s:%d", wire.ServerHost, *port)
	log.Printf("tcm server (go) listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
