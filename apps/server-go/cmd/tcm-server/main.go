// tcm-server is the Go tcm backend (see .agent-history/SCOPING-go-backend.md
// at the repo root).
//
// Tooling contract: the TUI launcher, restart.sh, and the tmux plugin all
// spawn this binary directly — it binds the tcm port, writes /tmp/tcm.pid
// after a successful bind, and answers POST /restart by re-exec'ing itself
// in place (same pid, so the pid file stays true; sockets are CLOEXEC so
// the port frees for the fresh image).
//
// A/B usage against a live instance:
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
	"github.com/kylesnowschwartz/agent-ouija/claude/settings"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/ccwatch"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexhooks"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/codexwatch"
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

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	port := flag.Int("port", wire.ServerPort, "listen port (use a non-default port to A/B against a live instance)")
	refresh := flag.Duration("refresh", 2*time.Second, "state refresh interval")
	doRegisterHooks := flag.Bool("register-hooks", false, "register tcm's Claude Code and Codex lifecycle hooks and exit")
	showVersion := flag.Bool("version", false, "print version and build commit and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("tcm-server %s\n", buildInfo())
		return
	}

	if *doRegisterHooks {
		registerHooks()
		return
	}

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
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	codexWatcher := codexwatch.New(filepath.Join(codexHome, "sessions"), filepath.Join(codexHome, "session_index.jsonl"))

	srv := server.New(builder, tracker.New(), watcher, piWatcher, codexWatcher, panescan.New())
	srv.BuildInfo = buildInfo()
	srv.Restart = restartInPlace
	srv.Quit = func() {
		_ = os.Remove(wire.PIDFile)
		os.Exit(0)
	}
	srv.ScriptsDir = resolveScriptsDir()
	srv.SidebarPosition = state.LoadSidebarPosition(configDir)
	srv.CompanionPane = state.LoadCompanionPane(configDir)

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

func buildInfo() string {
	return fmt.Sprintf("%s (commit %s)", version, commit)
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

// hookEvents are the Claude Code lifecycle events tcm listens to.
// StopFailure fires when a turn ends on an API error (rate limit, server
// error, max output tokens) instead of a normal Stop — without it a turn
// that dies mid-flight leaves the agent stuck showing "running".
var hookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PreToolUse",
	"PermissionRequest",
	"PostToolUse",
	"Stop",
	"StopFailure",
	"Notification",
	"SessionEnd",
}

// registerClaudeHooks registers scripts/hook.sh for every lifecycle event
// in Claude Code's settings.json (the -register-hooks one-shot; replaces
// the retired bun setup-hooks.ts). Idempotent against entries already
// present in either shell-string or exec form; hooks register async so
// they never block the agent.
func registerClaudeHooks() {
	hookScript := resolveHookScript()
	if hookScript == "" {
		log.Fatalf("register-hooks: scripts/hook.sh not found (set TCM_DIR or run from a tcm checkout)")
	}
	root, err := claudedir.DefaultRoot()
	if err != nil {
		log.Fatalf("register-hooks: claude root: %v", err)
	}

	cmds := make([]settings.HookCommand, 0, len(hookEvents))
	for _, event := range hookEvents {
		cmds = append(cmds, settings.HookCommand{Event: event, Command: hookScript, Args: []string{event}, Async: true})
	}

	added, err := settings.RegisterHooks(root.SettingsPath(), cmds)
	if err != nil {
		log.Fatalf("register-hooks: %v", err)
	}
	if len(added) == 0 {
		fmt.Println("All hooks already registered.")
		return
	}
	fmt.Printf("Registered hooks for: %v\n", added)
}

func registerHooks() {
	hookScript := resolveHookScript()
	if hookScript == "" {
		log.Fatalf("register-hooks: scripts/hook.sh not found (set TCM_DIR or run from a tcm checkout)")
	}
	registerClaudeHooks()
	registerCodexHooks(hookScript)
}

func registerCodexHooks(hookScript string) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("register-hooks: home dir: %v", err)
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	path := filepath.Join(codexHome, "hooks.json")
	added, err := codexhooks.Register(path, hookScript)
	if err != nil {
		log.Fatalf("register-hooks: codex: %v", err)
	}
	if len(added) == 0 {
		fmt.Println("All Codex hooks already registered.")
	} else {
		fmt.Printf("Registered Codex hooks for: %v\n", added)
	}
	fmt.Println("On the next Codex launch, choose Hooks need review, then Trust all and continue.")
}

// resolveHookScript locates scripts/hook.sh at the repo root, trying the
// same roots as resolveScriptsDir (TCM_DIR, then exe-relative). The result
// is symlink-resolved: TCM_DIR is usually the tpm plugin symlink, and
// registering the symlink path alongside an existing real-path entry would
// duplicate every hook.
func resolveHookScript() string {
	var roots []string
	if env := os.Getenv("TCM_DIR"); env != "" {
		roots = append(roots, env)
	}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(exe), "..", "..", ".."))
	}
	for _, root := range roots {
		script := filepath.Join(root, "scripts", "hook.sh")
		if resolved, err := filepath.EvalSymlinks(script); err == nil {
			return resolved
		}
	}
	return ""
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
