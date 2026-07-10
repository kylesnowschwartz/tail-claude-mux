package tmux

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSpawnAgentValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	reservedDir := filepath.Join(dir, StashSession)
	if err := os.Mkdir(reservedDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		req  SpawnAgentRequest
		want string
	}{
		{name: "missing dir", req: SpawnAgentRequest{Agent: "codex", Prompt: "task"}, want: "dir is required"},
		{name: "relative dir", req: SpawnAgentRequest{Dir: "relative", Agent: "codex", Prompt: "task"}, want: "dir must be an absolute path"},
		{name: "missing path", req: SpawnAgentRequest{Dir: filepath.Join(dir, "missing"), Agent: "codex", Prompt: "task"}, want: "dir does not exist"},
		{name: "not a directory", req: SpawnAgentRequest{Dir: file, Agent: "codex", Prompt: "task"}, want: "dir must be a directory"},
		{name: "unknown agent", req: SpawnAgentRequest{Dir: dir, Agent: "other", Prompt: "task"}, want: "agent must be codex, claude, or pi"},
		{name: "empty prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex"}, want: "prompt is required"},
		{name: "blank prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: " \n\t"}, want: "prompt is required"},
		{name: "empty sanitized name", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task", Name: "\x00\n"}, want: "name must contain printable characters"},
		{name: "reserved requested name", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task", Name: "\x1b[31m_tcm_stash\x1b[0m"}, want: "name is reserved by tcm"},
		{name: "reserved derived name", req: SpawnAgentRequest{Dir: reservedDir, Agent: "codex", Prompt: "task"}, want: "name is reserved by tcm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			tm := &Tmux{Run: func(args ...string) (string, error) {
				calls++
				return "", errors.New("missing")
			}}
			_, err := tm.SpawnAgent(tt.req)
			var validationErr *SpawnAgentValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want validation error", err)
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
			if calls != 0 {
				t.Errorf("runner calls = %d, want 0", calls)
			}
		})
	}
}

func TestSpawnAgentNameResolution(t *testing.T) {
	tests := []struct {
		name        string
		requestName string
		existing    map[string]bool
		want        string
		wantProbes  []string
	}{
		{name: "derived from directory", want: "project", wantProbes: []string{"=project"}},
		{name: "sanitizes explicit name", requestName: "\x1b[31mfix-auth\x1b[0m\x00", want: "fix-auth", wantProbes: []string{"=fix-auth"}},
		{name: "replaces tmux target separators", requestName: "a.b:c", want: "a-b-c", wantProbes: []string{"=a-b-c"}},
		{name: "replaces one space", requestName: "a b", want: "a-b", wantProbes: []string{"=a-b"}},
		{name: "collapses and trims spaces", requestName: " a  b ", want: "a-b", wantProbes: []string{"=a-b"}},
		{name: "replaces tab", requestName: "a\tb", want: "a-b", wantProbes: []string{"=a-b"}},
		{
			name: "deduplicates", requestName: "fix-auth",
			existing: map[string]bool{"fix-auth": true, "fix-auth-2": true},
			want:     "fix-auth-3", wantProbes: []string{"=fix-auth", "=fix-auth-2", "=fix-auth-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "project")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			var calls [][]string
			var probes []string
			tm := &Tmux{Run: func(args ...string) (string, error) {
				calls = append(calls, append([]string(nil), args...))
				if args[0] == "has-session" {
					probes = append(probes, args[2])
					if tt.existing[strings.TrimPrefix(args[2], "=")] {
						return "", nil
					}
					return "", errors.New("session not found")
				}
				return tt.want + " %42 @7", nil
			}}
			got, err := tm.SpawnAgent(SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task", Name: tt.requestName})
			if err != nil {
				t.Fatal(err)
			}
			if got.SessionName != tt.want {
				t.Errorf("session name = %q, want %q", got.SessionName, tt.want)
			}
			newSession := calls[len(calls)-1]
			if newSession[3] != tt.want {
				t.Errorf("new-session name = %q, want %q", newSession[3], tt.want)
			}
			if !reflect.DeepEqual(probes, tt.wantProbes) {
				t.Errorf("has-session targets = %#v, want %#v", probes, tt.wantProbes)
			}
		})
	}
}

func TestSpawnAgentCommandQuoting(t *testing.T) {
	prompt := "fix 'single' and \"double\"\nthen print $HOME"
	tests := []struct {
		name    string
		agent   string
		command []string
		want    string
	}{
		{
			name:  "nested prompt quoting",
			agent: "codex",
			want:  "sh -c ''\\''codex'\\'' '\\''--'\\'' '\\''fix '\\''\\'\\'''\\''single'\\''\\'\\'''\\'' and \"double\"\nthen print $HOME'\\''; status=$?; printf \"\\n[tcm] agent exited with status %d\\n\" \"$status\"; exec \"${SHELL:-sh}\"'",
		},
		{
			name:    "command override",
			agent:   "claude",
			command: []string{"custom agent", "--mode", "it's-safe"},
			want:    "sh -c ''\\''custom agent'\\'' '\\''--mode'\\'' '\\''it'\\''\\'\\'''\\''s-safe'\\'' '\\''fix '\\''\\'\\'''\\''single'\\''\\'\\'''\\'' and \"double\"\nthen print $HOME'\\''; status=$?; printf \"\\n[tcm] agent exited with status %d\\n\" \"$status\"; exec \"${SHELL:-sh}\"'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSpawnAgentCommand(SpawnAgentRequest{Agent: tt.agent, Prompt: prompt, Command: tt.command})
			if !strings.HasPrefix(got, "sh -c '") {
				t.Errorf("command prefix = %q, want sh -c with a quoted wrapper", got)
			}
			if !strings.HasSuffix(got, `exec "${SHELL:-sh}"'`) {
				t.Errorf("command suffix = %q, want interactive shell fallback", got)
			}
			if got != tt.want {
				t.Errorf("command:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestSpawnAgentCommandEndOfOptions(t *testing.T) {
	tests := []struct {
		name    string
		agent   string
		prompt  string
		command []string
		want    string
	}{
		{
			name: "codex protects flag-like prompt", agent: "codex", prompt: "--help",
			want: "sh -c ''\\''codex'\\'' '\\''--'\\'' '\\''--help'\\''; status=$?; printf \"\\n[tcm] agent exited with status %d\\n\" \"$status\"; exec \"${SHELL:-sh}\"'",
		},
		{
			name: "claude protects subcommand-like prompt", agent: "claude", prompt: "resume",
			want: "sh -c ''\\''claude'\\'' '\\''--'\\'' '\\''resume'\\''; status=$?; printf \"\\n[tcm] agent exited with status %d\\n\" \"$status\"; exec \"${SHELL:-sh}\"'",
		},
		{
			name: "pi keeps bare prompt", agent: "pi", prompt: "--help",
			want: "sh -c ''\\''pi'\\'' '\\''--help'\\''; status=$?; printf \"\\n[tcm] agent exited with status %d\\n\" \"$status\"; exec \"${SHELL:-sh}\"'",
		},
		{
			name: "override keeps bare prompt", agent: "codex", prompt: "--help", command: []string{"custom"},
			want: "sh -c ''\\''custom'\\'' '\\''--help'\\''; status=$?; printf \"\\n[tcm] agent exited with status %d\\n\" \"$status\"; exec \"${SHELL:-sh}\"'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSpawnAgentCommand(SpawnAgentRequest{
				Agent: tt.agent, Prompt: tt.prompt, Command: tt.command,
			})
			if got != tt.want {
				t.Errorf("command:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestSpawnAgentTmuxArguments(t *testing.T) {
	dir := t.TempDir()
	var got []string
	tm := &Tmux{Run: func(args ...string) (string, error) {
		if args[0] == "has-session" {
			return "", errors.New("session not found")
		}
		got = append([]string(nil), args...)
		return "agent %42 @7", nil
	}}

	result, err := tm.SpawnAgent(SpawnAgentRequest{
		Dir: dir, Agent: "pi", Prompt: "line one\nline '$TWO'", Name: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"new-session", "-d", "-s", "agent", "-c", dir,
		"-P", "-F", spawnAgentFormat, "--", buildSpawnAgentCommand(SpawnAgentRequest{
			Agent: "pi", Prompt: "line one\nline '$TWO'",
		}),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}
	if result != (SpawnAgentResult{SessionName: "agent", PaneID: "%42", WindowID: "@7"}) {
		t.Errorf("result = %#v", result)
	}
}

func TestSpawnAgentTmuxErrorIncludesOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		err  error
		want string
	}{
		{name: "runner output", out: "duplicate session: agent", err: errors.New("exit status 1"), want: "duplicate session: agent"},
		{name: "process stderr", err: &exec.ExitError{Stderr: []byte("server refused the session")}, want: "server refused the session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tm := &Tmux{Run: func(args ...string) (string, error) {
				if args[0] == "has-session" {
					return "", errors.New("session not found")
				}
				return tt.out, tt.err
			}}

			_, err := tm.SpawnAgent(SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task", Name: "agent"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
