package tmux

import (
	"errors"
	"fmt"
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
		{name: "missing dir", req: SpawnAgentRequest{Agent: "codex", Prompt: "task"}, want: "dir is required"},
		{name: "relative dir", req: SpawnAgentRequest{Dir: "relative", Agent: "codex", Prompt: "task"}, want: "dir must be an absolute path"},
		{name: "missing path", req: SpawnAgentRequest{Dir: filepath.Join(dir, "missing"), Agent: "codex", Prompt: "task"}, want: "dir does not exist"},
		{name: "not a directory", req: SpawnAgentRequest{Dir: file, Agent: "codex", Prompt: "task"}, want: "dir must be a directory"},
		{name: "unknown agent", req: SpawnAgentRequest{Dir: dir, Agent: "other", Prompt: "task"}, want: "agent must be codex, claude, or pi"},
		{name: "empty prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex"}, want: "prompt is required"},
		{name: "blank prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: " \n\t"}, want: "prompt is required"},
		{name: "pi flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "pi", Prompt: "--help"}, want: "this agent cannot accept a prompt that begins with '-'"},
		{name: "codex flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "codex", Prompt: "--help"}},
		{name: "claude flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "claude", Prompt: "--help"}},
		{name: "override allows flag-like prompt", req: SpawnAgentRequest{Dir: dir, Agent: "pi", Prompt: "--help", Command: []string{"custom"}}},
		{name: "valid", req: SpawnAgentRequest{Dir: dir, Agent: "pi", Prompt: "task"}},
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
			listCalls := 0
			tm := &Tmux{Run: func(args ...string) (string, error) {
				if args[0] != "list-sessions" {
					t.Fatalf("tmux command = %q, want list-sessions", args[0])
				}
				listCalls++
				names := make([]string, 0, len(tt.existing))
				for name := range tt.existing {
					names = append(names, name)
				}
				sort.Strings(names)
				rows := make([]string, 0, len(names))
				for i, name := range names {
					rows = append(rows, fmt.Sprintf("$%d\t%s\t0\t0\t1\t/tmp\t0", i, name))
				}
				return strings.Join(rows, "\n"), nil
			}}
			got, err := tm.resolveSpawnAgentName(SpawnAgentRequest{Dir: requestDir, Name: tt.requestName})
			if tt.wantErr != "" {
				var validationErr *SpawnAgentValidationError
				if !errors.As(err, &validationErr) {
					t.Fatalf("error = %v, want validation error", err)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("error = %q, want %q", err, tt.wantErr)
				}
				if listCalls != 0 {
					t.Errorf("list-sessions calls = %d, want 0", listCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("resolveSpawnAgentName() = %q, want %q", got, tt.want)
			}
			if listCalls != 1 {
				t.Errorf("list-sessions calls = %d, want 1", listCalls)
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
			name: "pi keeps normal prompt bare", agent: "pi", prompt: "task",
			want: []string{"pi", "task"},
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

func TestSpawnAgentTmuxArguments(t *testing.T) {
	dir := t.TempDir()
	var got []string
	tm := &Tmux{Run: func(args ...string) (string, error) {
		if args[0] == "list-sessions" {
			return "", nil
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
		Dir: dir, Agent: "pi", Prompt: "task", Name: "agent", OwnerSession: "owner",
	}
	result, err := tm.SpawnAgent(req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"new-window", "-d", "-t", "=owner:", "-c", dir, "-n", "agent",
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
				if args[0] == "list-sessions" {
					return "", nil
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

func TestSpawnAgentListSessionsError(t *testing.T) {
	dir := t.TempDir()
	tm := &Tmux{Run: func(args ...string) (string, error) {
		return "", errors.New("tmux unavailable")
	}}

	_, err := tm.SpawnAgent(SpawnAgentRequest{
		Dir: dir, Agent: "codex", Prompt: "task", Name: "agent",
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
				if args[0] == "list-sessions" {
					return "", nil
				}
				return output, nil
			}}

			_, err := tm.SpawnAgent(SpawnAgentRequest{
				Dir: dir, Agent: "codex", Prompt: "task", Name: "agent",
			})
			if err == nil || err.Error() != "tmux returned an unexpected result" {
				t.Fatalf("error = %v, want unexpected result", err)
			}
		})
	}
}
