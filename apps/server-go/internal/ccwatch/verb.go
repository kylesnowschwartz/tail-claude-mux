package ccwatch

import (
	"strings"

	"github.com/kylesnowschwartz/agent-ouija/claude/tools"
)

// ToolVerb maps a tool name to its shared.ts MetadataVerb. The watcher knows
// the tool name; tagging here spares renderers from regex-guessing the verb
// back out of the message (the TUI's classify.ts remains the fallback for
// untagged producers). Unknown/custom tools return "".
//
// agent-ouija's tool taxonomy is the base map, so Claude Code, Codex, and the
// other agents it knows share one classification; tcmVerbOverrides holds the
// tcm-specific exceptions to it.
func ToolVerb(toolName string) string {
	if verb, ok := tcmVerbOverrides[toolName]; ok {
		return verb
	}
	if verb, ok := codexCoordinationVerb(toolName); ok {
		return verb
	}
	switch tools.CategorizeToolName(toolName) {
	case tools.CategoryRead:
		return "read"
	case tools.CategoryEdit, tools.CategoryWrite:
		return "edit"
	case tools.CategoryBash:
		return "run"
	case tools.CategoryGrep, tools.CategoryGlob:
		return "search"
	case tools.CategoryTask:
		return "task"
	case tools.CategoryWeb:
		return "web"
	case tools.CategoryTool:
		return "tool" // MCP server tools
	default:
		return ""
	}
}

// tcmVerbOverrides are tools whose tcm verb differs from their agent-ouija
// category. Skill is a Task in the taxonomy; the sidebar gives it its own
// verb.
var tcmVerbOverrides = map[string]string{
	"Skill": "skill",
}

// codexAgentTools are Codex's sub-agent tools. Codex's hook payload names
// them bare (spawn_agent, and the other tools in the default namespace) or
// with the tool namespace glued on: multi_agent_v1send_input,
// collaborationspawn_agent.
var codexAgentTools = map[string]bool{
	"spawn_agent":     true,
	"send_input":      true,
	"send_message":    true,
	"followup_task":   true,
	"wait_agent":      true,
	"resume_agent":    true,
	"close_agent":     true,
	"interrupt_agent": true,
	"list_agents":     true,
}

var codexAgentNamespaces = []string{"", "multi_agent_v1", "collaboration"}

// codexCoordinationVerb reports "task" for a Codex sub-agent tool.
func codexCoordinationVerb(toolName string) (string, bool) {
	for _, ns := range codexAgentNamespaces {
		if name, ok := strings.CutPrefix(toolName, ns); ok && codexAgentTools[name] {
			return "task", true
		}
	}
	return "", false
}
