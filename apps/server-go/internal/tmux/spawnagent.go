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
	spawnAgentNameMaxWidth = 80
	spawnAgentDedupeLimit  = 100
	spawnAgentFormat       = "#{session_name} #{pane_id} #{window_id}"
	spawnAgentShellPrefix  = "sh -c "
	spawnAgentShellWrapper = "%s; status=$?; printf \"\\n[tcm] agent exited with status %%d\\n\" \"$status\"; exec \"${SHELL:-sh}\""
	defaultCodexCommand    = "codex"
	defaultClaudeCommand   = "claude"
	defaultPiCommand       = "pi"
	tmuxExactTargetPrefix  = "="
	tmuxSessionSeparator   = ":"
	tmuxWindowSeparator    = "."
	tmuxSafeNameSeparator  = "-"
)

// SpawnAgentRequest is the POST /spawn-agent request.
type SpawnAgentRequest struct {
	Dir     string   `json:"dir"`
	Agent   string   `json:"agent"`
	Prompt  string   `json:"prompt"`
	Name    string   `json:"name,omitempty"`
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

// SpawnAgent validates and starts an agent in a new detached tmux session.
func (t *Tmux) SpawnAgent(req SpawnAgentRequest) (SpawnAgentResult, error) {
	if err := validateSpawnAgentRequest(req); err != nil {
		return SpawnAgentResult{}, err
	}

	name, err := t.resolveSpawnAgentName(req)
	if err != nil {
		return SpawnAgentResult{}, err
	}
	command := buildSpawnAgentCommand(req)
	out, err := t.Run("new-session", "-d", "-s", name, "-c", req.Dir,
		"-P", "-F", spawnAgentFormat, "--", command)
	if err != nil {
		return SpawnAgentResult{}, fmt.Errorf("tmux could not start the agent: %s", tmuxErrorDetail(out, err))
	}

	fields := strings.Fields(out)
	if len(fields) < 3 {
		return SpawnAgentResult{}, fmt.Errorf("tmux returned an unexpected result")
	}
	return SpawnAgentResult{
		SessionName: strings.Join(fields[:len(fields)-2], " "),
		PaneID:      fields[len(fields)-2],
		WindowID:    fields[len(fields)-1],
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
	if _, ok := defaultSpawnAgentCommand(req.Agent); !ok {
		return &SpawnAgentValidationError{message: "agent must be codex, claude, or pi"}
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return &SpawnAgentValidationError{message: "prompt is required"}
	}
	return nil
}

func (t *Tmux) resolveSpawnAgentName(req SpawnAgentRequest) (string, error) {
	name := req.Name
	if name == "" {
		name = filepath.Base(req.Dir)
	}
	name = textutil.TruncateToWidth(textutil.SanitizeForDisplay(name), spawnAgentNameMaxWidth)
	name = strings.NewReplacer(
		tmuxSessionSeparator, tmuxSafeNameSeparator,
		tmuxWindowSeparator, tmuxSafeNameSeparator,
	).Replace(name)
	if name == "" {
		return "", &SpawnAgentValidationError{message: "name must contain printable characters"}
	}
	for suffix := 1; suffix <= spawnAgentDedupeLimit; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", name, suffix)
		}
		if _, err := t.Run("has-session", "-t", tmuxExactTargetPrefix+candidate); err != nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find an available tmux session name")
}

func buildSpawnAgentCommand(req SpawnAgentRequest) string {
	argv := req.Command
	if len(argv) == 0 {
		command, _ := defaultSpawnAgentCommand(req.Agent)
		argv = []string{command}
	}
	argv = append(append([]string(nil), argv...), req.Prompt)
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	innerCommand := strings.Join(quoted, " ")
	wrapper := fmt.Sprintf(spawnAgentShellWrapper, innerCommand)
	return spawnAgentShellPrefix + shellQuote(wrapper)
}

func defaultSpawnAgentCommand(agent string) (string, bool) {
	switch agent {
	case defaultCodexCommand:
		return defaultCodexCommand, true
	case defaultClaudeCommand:
		return defaultClaudeCommand, true
	case defaultPiCommand:
		return defaultPiCommand, true
	default:
		return "", false
	}
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
