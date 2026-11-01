package ccwatch

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/kylesnowschwartz/agent-ouija/claude/tools"
)

// ToolVerb maps a tool call to its shared.ts MetadataVerb. The watcher knows
// the tool name and input; tagging here spares renderers from regex-guessing
// the verb back out of the message (the TUI's classify.ts remains the
// fallback for untagged producers). Unknown/custom tools return "".
//
// agent-ouija's tool taxonomy is the base map, so Claude Code, Codex, and the
// other agents it knows share one classification; tcmVerbOverrides holds the
// tcm-specific exceptions to it. A shell call is classified by its command
// (see ShellVerb), since agents read and search through the shell.
func ToolVerb(toolName string, toolInput map[string]json.RawMessage) string {
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
		return ShellVerb(rawStr(toolInput, "command"))
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

// shellReadVerbs are commands that only look at the filesystem, by the verb
// they read as. Any command not listed is "run".
var shellReadVerbs = map[string]string{
	"grep": "search",
	"rg":   "search",
	"find": "search",
	"fd":   "search",
	"ls":   "list",
	"tree": "list",
	"cat":  "read",
	"head": "read",
	"tail": "read",
	"wc":   "read",
	"less": "read",
	"jq":   "read",
}

// ShellVerb classifies a shell command line by its first real command:
// leading `cd ...` steps (joined by &&, ||, ; or newlines), comment lines,
// and VAR=value assignments are skipped. Read-only commands map to search,
// list, or read; sed is read only when it prints with -n and does not edit
// in place. A command that redirects its output into a file (`cat > f`) is
// a write. Everything else, and an empty command, is "run".
func ShellVerb(command string) string {
	words := firstCommand(command)
	if len(words) == 0 || writesFile(words[1:]) {
		return "run"
	}
	name := path.Base(words[0])
	if name == "sed" {
		if sedReadOnly(words[1:]) {
			return "read"
		}
		return "run"
	}
	if verb, ok := shellReadVerbs[name]; ok {
		return verb
	}
	return "run"
}

// firstCommand returns the words of the first command in a command line that
// is not a directory change, with leading VAR=value assignments removed.
// Splitting is by whitespace; quoting is not interpreted, which is enough to
// find the command word.
func firstCommand(command string) []string {
	steps := strings.FieldsFunc(command, func(r rune) bool { return r == '\n' || r == ';' })
	for _, step := range steps {
		for _, part := range splitAll(step, "&&", "||") {
			words := strings.Fields(part)
			for len(words) > 0 && isAssignment(words[0]) {
				words = words[1:]
			}
			if len(words) == 0 || strings.HasPrefix(words[0], "#") || words[0] == "cd" {
				continue
			}
			return words
		}
	}
	return nil
}

// splitAll splits s at every occurrence of any separator.
func splitAll(s string, seps ...string) []string {
	parts := []string{s}
	for _, sep := range seps {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
		}
		parts = next
	}
	return parts
}

// isAssignment reports whether word is a VAR=value environment prefix: a
// name of letters, digits, and underscores, not starting with a digit.
func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" || (name[0] >= '0' && name[0] <= '9') {
		return false
	}
	for _, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// writesFile reports whether args redirect standard output (>, >>, 1>, &>)
// into a file other than /dev/null. Standard error redirects (2>) do not
// count.
func writesFile(args []string) bool {
	for i, a := range args {
		a = strings.TrimPrefix(strings.TrimPrefix(a, "1"), "&")
		if !strings.HasPrefix(a, ">") {
			continue
		}
		target := strings.TrimLeft(a, ">")
		if target == "" && i+1 < len(args) {
			target = args[i+1]
		}
		if target != "/dev/null" && !strings.HasPrefix(target, "&") {
			return true
		}
	}
	return false
}

// sedReadOnly reports whether sed's arguments print selected lines (-n,
// --quiet, --silent) without editing a file in place (-i, --in-place).
func sedReadOnly(args []string) bool {
	quiet := false
	for _, a := range args {
		switch {
		case a == "--quiet" || a == "--silent":
			quiet = true
		case strings.HasPrefix(a, "--in-place"):
			return false
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// A short-flag cluster: -n, -ne, -i, -i.bak, -ni.
			if strings.ContainsRune(a, 'i') {
				return false
			}
			if strings.ContainsRune(a, 'n') {
				quiet = true
			}
		}
	}
	return quiet
}

// rawStr reads a string field from tool input without display sanitizing;
// "" if missing or non-string.
func rawStr(input map[string]json.RawMessage, key string) string {
	var s string
	if json.Unmarshal(input[key], &s) != nil {
		return ""
	}
	return s
}
