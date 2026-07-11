package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/textutil"
)

const (
	spawnAgentDedupeLimit  = 100
	spawnAgentFormat       = "#{session_name} #{pane_id} #{window_id}"
	spawnAgentShellPrefix  = "sh -c "
	spawnAgentShellWrapper = "%s; status=$?; printf \"\\n[tcm] agent exited with status %%d\\n\" \"$status\"; exec \"${SHELL:-sh}\""
	tmuxSessionSeparator   = ":"
	tmuxWindowSeparator    = "."
	tmuxSafeNameSeparator  = "-"
)

var spawnAgents = map[string]struct {
	binary       string
	endOfOptions string
}{
	"codex":  {binary: "codex", endOfOptions: "--"},
	"claude": {binary: "claude", endOfOptions: "--"},
	"pi":     {binary: "pi"},
}

// SpawnAgentRequest is the POST /spawn-agent request.
type SpawnAgentRequest struct {
	Dir          string `json:"dir"`
	Agent        string `json:"agent"`
	Prompt       string `json:"prompt"`
	Name         string `json:"name,omitempty"`
	OwnerSession string `json:"ownerSession,omitempty"`
	// Command replaces the default agent argv. Its prompt is appended bare;
	// callers supplying an override own any end-of-options handling it needs.
	Command []string `json:"command,omitempty"`
}

// SpawnAgentResult identifies the tmux session, pane, and window that were created.
type SpawnAgentResult struct {
	SessionName string `json:"sessionName"`
	PaneID      string `json:"paneId"`
	WindowID    string `json:"windowId"`
}

// SpawnAgentValidationError reports a request the caller can correct.
type SpawnAgentValidationError struct {
	message string
}

// Error implements error.
func (e *SpawnAgentValidationError) Error() string { return e.message }

// SpawnAgent validates and starts an agent in a new tmux session or owner window.
func (t *Tmux) SpawnAgent(req SpawnAgentRequest) (SpawnAgentResult, error) {
	if err := validateSpawnAgentRequest(req); err != nil {
		return SpawnAgentResult{}, err
	}

	name, err := t.resolveSpawnAgentName(req)
	if err != nil {
		return SpawnAgentResult{}, err
	}
	command := buildSpawnAgentCommand(req)
	args := []string{"new-session", "-d", "-s", name, "-c", req.Dir}
	if req.OwnerSession != "" {
		args = []string{"new-window", "-t", req.OwnerSession, "-c", req.Dir, "-n", name}
	}
	args = append(args, "-P", "-F", spawnAgentFormat, "--", command)
	out, err := t.Run(args...)
	if err != nil {
		return SpawnAgentResult{}, fmt.Errorf("tmux could not start the agent: %s", tmuxErrorDetail(out, err))
	}

	fields := strings.Fields(out)
	if len(fields) != 3 {
		return SpawnAgentResult{}, fmt.Errorf("tmux returned an unexpected result")
	}
	return SpawnAgentResult{
		SessionName: fields[0],
		PaneID:      fields[1],
		WindowID:    fields[2],
	}, nil
}

func validateSpawnAgentRequest(req SpawnAgentRequest) error {
	if req.Dir == "" {
		return &SpawnAgentValidationError{message: "dir is required"}
	}
	if !filepath.IsAbs(req.Dir) {
		return &SpawnAgentValidationError{message: "dir must be an absolute path"}
	}
	info, err := os.Stat(req.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &SpawnAgentValidationError{message: "dir does not exist"}
		}
		return &SpawnAgentValidationError{message: "dir cannot be accessed"}
	}
	if !info.IsDir() {
		return &SpawnAgentValidationError{message: "dir must be a directory"}
	}
	agent, ok := spawnAgents[req.Agent]
	if !ok {
		return &SpawnAgentValidationError{message: "agent must be codex, claude, or pi"}
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return &SpawnAgentValidationError{message: "prompt is required"}
	}
	if len(req.Command) == 0 && agent.endOfOptions == "" && strings.HasPrefix(req.Prompt, "-") {
		return &SpawnAgentValidationError{message: "this agent cannot accept a prompt that begins with '-'"}
	}
	return nil
}

func (t *Tmux) resolveSpawnAgentName(req SpawnAgentRequest) (string, error) {
	name := req.Name
	if name == "" {
		name = filepath.Base(req.Dir)
	}
	name = strings.Join(strings.Fields(name), tmuxSafeNameSeparator)
	name = textutil.SanitizeSessionName(name)
	name = strings.NewReplacer(
		tmuxSessionSeparator, tmuxSafeNameSeparator,
		tmuxWindowSeparator, tmuxSafeNameSeparator,
	).Replace(name)
	if name == "" {
		return "", &SpawnAgentValidationError{message: "name must contain printable characters"}
	}
	if name == StashSession {
		return "", &SpawnAgentValidationError{message: "name is reserved by tcm"}
	}
	sessions, err := t.listSessions()
	if err != nil {
		return "", fmt.Errorf("tmux could not list sessions: %w", err)
	}
	existing := make(map[string]struct{}, len(sessions))
	if req.OwnerSession == "" {
		for _, session := range sessions {
			existing[session.Name] = struct{}{}
		}
	} else {
		foundOwner := false
		for _, session := range sessions {
			if session.Name == req.OwnerSession || session.ID == req.OwnerSession {
				foundOwner = true
				break
			}
		}
		if !foundOwner {
			return "", &SpawnAgentValidationError{message: "owner session does not exist"}
		}
		out, err := t.Run("list-windows", "-t", req.OwnerSession, "-F", "#{window_name}")
		if err != nil {
			return "", fmt.Errorf("tmux could not list owner session windows: %w", err)
		}
		for name := range strings.SplitSeq(out, "\n") {
			if name != "" {
				existing[name] = struct{}{}
			}
		}
	}
	for suffix := 1; suffix <= spawnAgentDedupeLimit; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", name, suffix)
		}
		if _, found := existing[candidate]; !found {
			return candidate, nil
		}
	}
	if req.OwnerSession != "" {
		return "", fmt.Errorf("could not find an available tmux window name")
	}
	return "", fmt.Errorf("could not find an available tmux session name")
}

func buildSpawnAgentCommand(req SpawnAgentRequest) string {
	argv := buildSpawnAgentArgv(req)
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	innerCommand := strings.Join(quoted, " ")
	wrapper := fmt.Sprintf(spawnAgentShellWrapper, innerCommand)
	return spawnAgentShellPrefix + shellQuote(wrapper)
}

func buildSpawnAgentArgv(req SpawnAgentRequest) []string {
	argv := append([]string(nil), req.Command...)
	if len(argv) == 0 {
		agent := spawnAgents[req.Agent]
		argv = append(argv, agent.binary)
		if agent.endOfOptions != "" {
			argv = append(argv, agent.endOfOptions)
		}
	}
	return append(argv, req.Prompt)
}

func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func tmuxErrorDetail(out string, err error) string {
	if detail := strings.TrimSpace(out); detail != "" {
		return detail
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if detail := strings.TrimSpace(string(exitErr.Stderr)); detail != "" {
			return detail
		}
	}
	return err.Error()
}
