package tmux

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestValidateSpawnAgentRequest(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		req  SpawnAgentRequest
		want string
	}{
		{name: "missing dir", req: SpawnAgentRequest{Agent: "codex", Prompt: "task", OwnerSession: "owner"}, want: "dir is required"},
		{name: "missing owner session", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task"}, want: "ownerSession is required"},
		{name: "relative dir", req: SpawnAgentRequest{Dir: "relative", Agent: "codex", Prompt: "task", OwnerSession: "owner"}, want: "dir must be an absolute path"},
		{name: "missing path", req: SpawnAgentRequest{Dir: filepath.Join(dir, "missing"), Agent: "codex", Prompt: "task", OwnerSession: "owner"}, want: "dir does not exist"},
		{name: "not a directory", req: SpawnAgentRequest{Dir: file, Agent: "codex", Prompt: "task", OwnerSession: "owner"}, want: "dir must be a directory"},
		{name: "unknown agent", req: SpawnAgentRequest{Dir: dir, Agent: "other", Prompt: "task", OwnerSession: "owner"}, want: "agent must be codex or claude"},
		{name: "empty prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", OwnerSession: "owner"}, want: "prompt is required"},
		{name: "blank prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: " \n\t", OwnerSession: "owner"}, want: "prompt is required"},
		{name: "codex flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "--help", OwnerSession: "owner"}},
		{name: "claude flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "claude", Prompt: "--help", OwnerSession: "owner"}},
		{name: "override allows flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "--help", Command: []string{"custom"}, OwnerSession: "owner"}},
		{name: "valid", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task", OwnerSession: "owner"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSpawnAgentRequest(tt.req)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateSpawnAgentRequest() error = %v", err)
				}
				return
			}
			var validationErr *SpawnAgentValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error = %v, want validation error", err)
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveSpawnAgentName(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	reservedDir := filepath.Join(parent, StashSession)
	if err := os.Mkdir(reservedDir, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		dir         string
		requestName string
		existing    map[string]bool
		want        string
		wantErr     string
	}{
		{name: "derived from directory", dir: dir, want: "project"},
		{name: "sanitizes explicit name", requestName: "\x1b[31mfix-auth\x1b[0m\x00", want: "fix-auth"},
		{name: "replaces colons dots and spaces", requestName: "a:b.c d", want: "a-b-c-d"},
		{name: "collapses and trims spaces", requestName: " a  b ", want: "a-b"},
		{name: "replaces tab", requestName: "a\tb", want: "a-b"},
		{name: "uses exact matches", requestName: "fix-auth", existing: map[string]bool{"fix-auth-extra": true}, want: "fix-auth"},
		{name: "deduplicates first collision", requestName: "fix-auth", existing: map[string]bool{"fix-auth": true}, want: "fix-auth-2"},
		{name: "deduplicates repeated collisions", requestName: "fix-auth", existing: map[string]bool{"fix-auth": true, "fix-auth-2": true}, want: "fix-auth-3"},
		{name: "rejects empty sanitized name", requestName: "\x00\n", wantErr: "name must contain printable characters"},
		{name: "rejects reserved requested name", requestName: "\x1b[31m_tcm_stash\x1b[0m", wantErr: "name is reserved by tcm"},
		{name: "rejects reserved derived name", dir: reservedDir, wantErr: "name is reserved by tcm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestDir := tt.dir
			if requestDir == "" {
				requestDir = dir
			}
			tm := &Tmux{Run: func(args ...string) (string, error) {
				switch args[0] {
				case "list-sessions":
					return "$1\towner\t0\t0\t1\t/tmp\t0", nil
				case "list-windows":
					names := make([]string, 0, len(tt.existing))
					for name := range tt.existing {
						names = append(names, name)
					}
					sort.Strings(names)
					return strings.Join(names, "\n"), nil
				default:
					t.Fatalf("unexpected tmux command %q", args[0])
					return "", nil
				}
			}}
			got, err := tm.resolveSpawnAgentName(SpawnAgentRequest{Dir: requestDir, Name: tt.requestName, OwnerSession: "owner"})
			if tt.wantErr != "" {
				var validationErr *SpawnAgentValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("error = %v, want validation error", err)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("error = %q, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("resolveSpawnAgentName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveSpawnAgentWindowName(t *testing.T) {
	dir := t.TempDir()
	var commands [][]string
	tm := &Tmux{Run: func(args ...string) (string, error) {
		commands = append(commands, append([]string(nil), args...))
		switch args[0] {
		case "list-sessions":
			return "$1\towner\t0\t0\t2\t/tmp\t0\n$2\tother\t0\t0\t1\t/tmp\t0", nil
		case "list-windows":
			return "agent\nagent-2", nil
		default:
			t.Fatalf("unexpected tmux command %q", args[0])
			return "", nil
		}
	}}

	got, err := tm.resolveSpawnAgentName(SpawnAgentRequest{Dir: dir, Name: "agent", OwnerSession: "$1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent-3" {
		t.Errorf("name = %q, want agent-3", got)
	}
	wantListWindows := []string{"list-windows", "-t", "=$1:", "-F", "#{window_name}"}
	if len(commands) != 2 || !reflect.DeepEqual(commands[1], wantListWindows) {
		t.Errorf("commands = %#v, want list-sessions then %#v", commands, wantListWindows)
	}

	_, err = tm.resolveSpawnAgentName(SpawnAgentRequest{Dir: dir, Name: "agent", OwnerSession: "missing"})
	var validationErr *SpawnAgentValidationError
	if !errors.As(err, &validationErr) || err.Error() != "owner session does not exist" {
		t.Fatalf("missing owner error = %v, want validation error", err)
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
		want    []string
	}{
		{
			name: "codex protects flag-like prompt", agent: "codex", prompt: "--help",
			want: []string{"codex", "--", "--help"},
		},
		{
			name: "claude protects flag-like prompt", agent: "claude", prompt: "--help",
			want: []string{"claude", "--", "--help"},
		},
		{
			name: "override keeps bare prompt", agent: "codex", prompt: "--help", command: []string{"custom"},
			want: []string{"custom", "--help"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSpawnAgentArgv(SpawnAgentRequest{
				Agent: tt.agent, Prompt: tt.prompt, Command: tt.command,
			})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("argv = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSpawnAgentWindowTmuxArguments(t *testing.T) {
	dir := t.TempDir()
	var got []string
	tm := &Tmux{Run: func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "$1\towner\t0\t0\t1\t/tmp\t0", nil
		case "list-windows":
			return "shell", nil
		default:
			got = append([]string(nil), args...)
			return "owner %42 @7", nil
		}
	}}
	req := SpawnAgentRequest{
		Dir: dir, Agent: "codex", Prompt: "task", Name: "agent", OwnerSession: "owner",
		DelegationID: "123e4567-e89b-42d3-a456-426614174000",
	}
	result, err := tm.SpawnAgent(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"new-window", "-d", "-t", "=owner:", "-c", dir, "-n", "agent",
		"-e", "TCM_DELEGATION_ID=123e4567-e89b-42d3-a456-426614174000",
		"-P", "-F", spawnAgentFormat, "--", buildSpawnAgentCommand(req),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}
	if result != (SpawnAgentResult{SessionName: "owner", PaneID: "%42", WindowID: "@7"}) {
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
				switch args[0] {
				case "list-sessions":
					return "$1\towner\t0\t0\t1\t/tmp\t0", nil
				case "list-windows":
					return "", nil
				default:
					return tt.out, tt.err
				}
			}}

			_, err := tm.SpawnAgent(SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "task", Name: "agent", OwnerSession: "owner"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSpawnAgentListSessionsError(t *testing.T) {
	dir := t.TempDir()
	tm := &Tmux{Run: func(args ...string) (string, error) {
		return "", errors.New("tmux unavailable")
	}}

	_, err := tm.SpawnAgent(SpawnAgentRequest{
		Dir: dir, Agent: "codex", Prompt: "task", Name: "agent", OwnerSession: "owner",
	})
	if err == nil || !strings.Contains(err.Error(), "tmux could not list sessions: tmux unavailable") {
		t.Fatalf("error = %v, want list-sessions failure", err)
	}
}

func TestSpawnAgentRejectsUnexpectedTmuxResult(t *testing.T) {
	for _, output := range []string{"agent %42", "agent extra %42 @7"} {
		t.Run(output, func(t *testing.T) {
			dir := t.TempDir()
			tm := &Tmux{Run: func(args ...string) (string, error) {
				switch args[0] {
				case "list-sessions":
					return "$1\towner\t0\t0\t1\t/tmp\t0", nil
				case "list-windows":
					return "", nil
				default:
					return output, nil
				}
			}}

			_, err := tm.SpawnAgent(SpawnAgentRequest{
				Dir: dir, Agent: "codex", Prompt: "task", Name: "agent", OwnerSession: "owner",
			})
			if err == nil || err.Error() != "tmux returned an unexpected result" {
				t.Fatalf("error = %v, want unexpected result", err)
			}
		})
	}
}
