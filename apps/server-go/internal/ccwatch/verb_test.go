package ccwatch

import (
	"encoding/json"
	"testing"
)

func TestToolVerb(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		// Claude Code
		{"Read", "read"},
		{"Edit", "edit"},
		{"Write", "edit"},
		{"NotebookEdit", "edit"},
		{"Bash", "run"},
		{"Glob", "search"},
		{"Grep", "search"},
		{"Agent", "task"},
		{"Task", "task"},
		{"SendMessage", "task"},
		{"SubagentHandback", "task"},
		{"ListAgents", "task"},
		{"TaskStop", "task"},
		{"WebFetch", "web"},
		{"WebSearch", "web"},
		{"Skill", "skill"},
		{"mcp__plugin_figma_figma__whoami", "tool"},
		{"mcp__claude_ai_Slack__slack_read_thread", "tool"},
		{"ToolSearch", ""}, // unknown tools stay untagged — TUI classifier decides
		{"AskUserQuestion", ""},
		{"", ""},

		// Codex hook payload names
		{"apply_patch", "edit"},
		{"mcp__memory__create_entities", "tool"},
		{"spawn_agent", "task"},
		{"wait_agent", "task"},
		{"multi_agent_v1send_input", "task"},
		{"collaborationspawn_agent", "task"},
		{"update_plan", ""},
	}
	for _, c := range cases {
		if got := ToolVerb(c.tool, nil); got != c.want {
			t.Errorf("ToolVerb(%q) = %q, want %q", c.tool, got, c.want)
		}
	}
}

func TestToolVerbClassifiesShellCommands(t *testing.T) {
	cmd := func(s string) map[string]json.RawMessage {
		raw, _ := json.Marshal(s)
		return map[string]json.RawMessage{"command": raw}
	}
	cases := []struct {
		tool  string
		input map[string]json.RawMessage
		want  string
	}{
		{"Bash", cmd("grep -rn ToolVerb apps/server-go"), "search"},
		{"Bash", cmd("ls -la"), "list"},
		{"Bash", cmd("git status --short"), "run"},
		{"Bash", nil, "run"},
		{"Bash", map[string]json.RawMessage{"command": json.RawMessage(`["ls"]`)}, "run"},
		// Codex reports every shell call as Bash with the command string.
		{"Bash", cmd("rg -n 'func Render' internal/waterfall"), "search"},
		{"apply_patch", cmd("*** Begin Patch\n*** Update File: a.go"), "edit"},
	}
	for _, c := range cases {
		if got := ToolVerb(c.tool, c.input); got != c.want {
			t.Errorf("ToolVerb(%q, %s) = %q, want %q", c.tool, c.input["command"], got, c.want)
		}
	}
}

func TestShellVerb(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		// search
		{`grep -rn "ToolVerb" apps/server-go/internal`, "search"},
		{`rg -n --type go 'func determineStatus'`, "search"},
		{`find ~/.claude -path '*plugin-authoring*' -type f 2>/dev/null | head -50`, "search"},
		{`fd -e go waterfall`, "search"},
		{`/usr/bin/grep -c foo bar.txt`, "search"},

		// list
		{`ls -la apps/server-go/internal/waterfall/`, "list"},
		{`tree -L 2 apps`, "list"},

		// read
		{`cat /Users/kyle/.claude/projects/memory/MEMORY.md; echo ----; tmux -V`, "read"},
		{`head -40 apps/tui/src/vocab.ts`, "read"},
		{`tail -f /tmp/tcm-debug.log`, "read"},
		{`wc -l *.go`, "read"},
		{`less README.md`, "read"},
		{`jq '.hooks | keys' ~/.codex/hooks.json`, "read"},
		{`sed -n 855,960p internal/ccwatch/ccwatch.go`, "read"},
		{`sed -n -e '1,20p' go.mod`, "read"},
		{`sed -ne '/^func /p' verb.go`, "read"},
		{`sed --quiet 5p go.mod`, "read"},
		{`grep -n foo bar.go 2>&1`, "search"},
		{`cat go.mod > /dev/null`, "read"},

		// leading cd steps, assignments, and comments are skipped
		{`cd /Users/kyle/Code/my-projects/tail-claude-mux-waterfall && git status --short`, "run"},
		{`cd apps/server-go && grep -rn ToolVerb .`, "search"},
		{`cd apps/tui; ls src`, "list"},
		{`cd a && cd b && sed -n 1,5p x.go`, "read"},
		{`CODEGRAPH_TELEMETRY=0 rg foo`, "search"},
		{`FOO=1 BAR_2=x ls`, "list"},
		{"# list the waterfall package\nls internal/waterfall", "list"},
		{`cd /tmp || exit 1`, "run"},

		// run
		{`git log -3 --format='%h %s'`, "run"},
		{`gh pr view 42 --json title`, "run"},
		{`python3 - <<'EOF'`, "run"},
		{`curl -s https://example.com | jq .`, "run"},
		{`go test ./...`, "run"},
		{`sed -i '' 's/a/b/' file.go`, "run"},
		{`sed -i.bak -n 's/a/b/p' file.go`, "run"},
		{`sed -ni 's/a/b/p' file.go`, "run"},
		{`sed --in-place -n 's/a/b/p' file.go`, "run"},
		{`sed 's/a/b/' file.go`, "run"},
		{`cat > notes.txt <<'EOF'`, "run"},
		{`cat a.txt >> b.txt`, "run"},
		{`head -5 a.txt >out.txt`, "run"},
		{`=oops ls`, "run"},
		{`1FOO=x ls`, "run"},
		{``, "run"},
		{`   `, "run"},
		{`cd /tmp`, "run"},
	}
	for _, c := range cases {
		if got := ShellVerb(c.command); got != c.want {
			t.Errorf("ShellVerb(%q) = %q, want %q", c.command, got, c.want)
		}
	}
}
