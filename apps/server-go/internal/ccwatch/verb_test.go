package ccwatch

import "testing"

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
		if got := ToolVerb(c.tool); got != c.want {
			t.Errorf("ToolVerb(%q) = %q, want %q", c.tool, got, c.want)
		}
	}
}
