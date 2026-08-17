package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// delegateOutcome is the JSON shape tcm delegate prints to stdout.
type delegateOutcome struct {
	Outcome      string `json:"outcome"`
	Detail       string `json:"detail,omitempty"`
	SessionName  string `json:"sessionName,omitempty"`
	PaneID       string `json:"paneId,omitempty"`
	WindowID     string `json:"windowId,omitempty"`
	OwnerSession string `json:"ownerSession,omitempty"`
	Alive        bool   `json:"alive,omitempty"`
}

// spawnAgentBody is the POST /spawn-agent request body tcm delegate sends.
type spawnAgentBody struct {
	Dir          string   `json:"dir"`
	Agent        string   `json:"agent"`
	Prompt       string   `json:"prompt"`
	Name         string   `json:"name"`
	OwnerSession string   `json:"ownerSession"`
	Command      []string `json:"command"`
}

// spawnAgentResponse is the subset of POST /spawn-agent's 200 response tcm
// delegate reads.
type spawnAgentResponse struct {
	SessionName string `json:"sessionName"`
	PaneID      string `json:"paneId"`
	WindowID    string `json:"windowId"`
}

// runDelegate starts a codex delegate in a new tmux window inside the
// owning tmux session and prints the outcome as one line of JSON. It
// returns the process exit code.
func runDelegate(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("delegate", flag.ContinueOnError)
	dir := fs.String("dir", "", "absolute working directory for the delegate")
	name := fs.String("name", "", "kebab-case session name for the delegate")
	brief := fs.String("brief", "", "task brief text (mutually exclusive with -brief-file)")
	briefFile := fs.String("brief-file", "", "path to a file containing the task brief")
	owner := fs.String("owner", "", "owning tmux session (auto-detected from $TMUX when omitted)")
	port := fs.Int("port", 7391, "tcm server port")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	promptText, err := resolveBrief(*brief, *briefFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ownerSession := *owner
	if ownerSession == "" {
		ownerSession = detectOwnerSession()
	}
	if ownerSession == "" {
		return emitDelegate(stdout, delegateOutcome{Outcome: "spawn-failed", Detail: "no-tmux-owner"})
	}

	body, err := json.Marshal(spawnAgentBody{
		Dir:          *dir,
		Agent:        "codex",
		Prompt:       promptText,
		Name:         *name,
		OwnerSession: ownerSession,
		Command:      []string{"codex", "--profile", "tcm-delegate", "-c", "mcp_servers.just.enabled=false", "--"},
	})
	if err != nil {
		return emitDelegate(stdout, delegateOutcome{Outcome: "spawn-failed", Detail: err.Error()})
	}

	status, respBody, err := postJSON(baseURL(*port)+"/spawn-agent", body)
	if err != nil {
		return emitDelegate(stdout, delegateOutcome{Outcome: "spawn-failed", Detail: err.Error()})
	}
	if status != 200 {
		return emitDelegate(stdout, delegateOutcome{Outcome: "spawn-failed", Detail: fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(respBody)))})
	}

	var spawned spawnAgentResponse
	if err := json.Unmarshal(respBody, &spawned); err != nil {
		return emitDelegate(stdout, delegateOutcome{Outcome: "spawn-failed", Detail: fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(respBody)))})
	}

	time.Sleep(2 * time.Second)
	alive := paneAlive(spawned.PaneID)

	return emitDelegate(stdout, delegateOutcome{
		Outcome:      "spawned",
		SessionName:  spawned.SessionName,
		PaneID:       spawned.PaneID,
		WindowID:     spawned.WindowID,
		OwnerSession: ownerSession,
		Alive:        alive,
	})
}

// resolveBrief reads the task brief from -brief or -brief-file. Exactly one
// must be set.
func resolveBrief(brief, briefFile string) (string, error) {
	if brief != "" && briefFile != "" {
		return "", fmt.Errorf("delegate: pass either -brief or -brief-file, not both")
	}
	if brief != "" {
		return brief, nil
	}
	if briefFile != "" {
		data, err := os.ReadFile(briefFile)
		if err != nil {
			return "", fmt.Errorf("delegate: reading -brief-file: %w", err)
		}
		return string(data), nil
	}
	return "", fmt.Errorf("delegate: -brief or -brief-file is required")
}

// detectOwnerSession auto-detects the owning tmux session from the current
// pane, but only inside a running tmux client — outside tmux (e.g. cron)
// there is no session to detect.
func detectOwnerSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := tmuxOutput("display-message", "-p", "#{session_name}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// paneAlive probes whether a pane is present. tmux display-message exits 0
// even for an unknown -t target (it renders an empty line), so liveness is
// the echoed pane id matching, not the exit status.
func paneAlive(paneID string) bool {
	if paneID == "" {
		return false
	}
	out, err := tmuxOutput("display-message", "-p", "-t", paneID, "#{pane_id}")
	return err == nil && strings.TrimSpace(out) == paneID
}

func emitDelegate(stdout io.Writer, outcome delegateOutcome) int {
	_ = writeJSON(stdout, outcome)
	if outcome.Outcome != "spawned" {
		return 1
	}
	return 0
}
