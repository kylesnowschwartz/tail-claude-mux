# opensessions development recipes

set shell := ["zsh", "-cu"]

root := justfile_directory()
bun := env("BUN_PATH", `command -v bun 2>/dev/null || echo "$HOME/.bun/bin/bun"`)
port := env("OPENSESSIONS_PORT", "7391")
host := env("OPENSESSIONS_HOST", "127.0.0.1")

# Full dev restart: kill everything, rebuild, relaunch server + all sidebars
restart:
    #!/usr/bin/env zsh
    set -euo pipefail
    echo "==> Killing TUI panes..."
    # Find and kill all tmux panes running the TUI (bun run src/index.tsx)
    tmux list-panes -a -F '#{pane_id} #{pane_pid}' 2>/dev/null | while read pane_id pane_pid; do
        cmd=$(ps -p "$pane_pid" -o command= 2>/dev/null || true)
        if [[ "$cmd" == *"src/index.tsx"* ]]; then
            tmux kill-pane -t "$pane_id" 2>/dev/null || true
        fi
    done
    echo "==> Killing server..."
    # Kill via PID file
    if [[ -f /tmp/opensessions.pid ]]; then
        kill "$(cat /tmp/opensessions.pid)" 2>/dev/null || true
        rm -f /tmp/opensessions.pid
    fi
    # Belt and suspenders: kill any remaining bun server processes
    pkill -f "bun.*apps/server/src/main.ts" 2>/dev/null || true
    # Wait for port to free
    for i in {1..20}; do
        if ! lsof -iTCP:{{port}} -sTCP:LISTEN -t >/dev/null 2>&1; then
            break
        fi
        sleep 0.1
    done
    echo "==> Building TUI..."
    cd "{{root}}/apps/tui" && "{{bun}}" run build
    echo "==> Starting server..."
    "{{bun}}" run "{{root}}/apps/server/src/main.ts" >/dev/null 2>&1 &
    # Wait for server to come up
    for i in {1..30}; do
        if curl -s -o /dev/null -m 0.2 "http://{{host}}:{{port}}/" 2>/dev/null; then
            break
        fi
        sleep 0.1
    done
    echo "==> Reopening sidebars..."
    # Re-run the tmux plugin to rebind keys, then ensure sidebars for all sessions
    bash "{{root}}/opensessions.tmux" 2>/dev/null || true
    # Ensure sidebar for every session
    tmux list-sessions -F '#{session_name}' 2>/dev/null | while read sess; do
        curl -s -o /dev/null -X POST "http://{{host}}:{{port}}/ensure-sidebar" \
            -d "|${sess}|" 2>/dev/null || true
    done
    echo "==> Done. All sidebars restarted."

# Kill everything without restarting
stop:
    #!/usr/bin/env zsh
    set -euo pipefail
    echo "==> Killing TUI panes..."
    tmux list-panes -a -F '#{pane_id} #{pane_pid}' 2>/dev/null | while read pane_id pane_pid; do
        cmd=$(ps -p "$pane_pid" -o command= 2>/dev/null || true)
        if [[ "$cmd" == *"src/index.tsx"* ]]; then
            tmux kill-pane -t "$pane_id" 2>/dev/null || true
        fi
    done
    echo "==> Killing server..."
    if [[ -f /tmp/opensessions.pid ]]; then
        kill "$(cat /tmp/opensessions.pid)" 2>/dev/null || true
        rm -f /tmp/opensessions.pid
    fi
    pkill -f "bun.*apps/server/src/main.ts" 2>/dev/null || true
    echo "==> Stopped."

# Run runtime tests
test:
    cd "{{root}}/packages/runtime" && "{{bun}}" test

# Build the TUI
build:
    cd "{{root}}/apps/tui" && "{{bun}}" run build

# Tail server debug log
log:
    tail -f /tmp/opensessions-debug.log
