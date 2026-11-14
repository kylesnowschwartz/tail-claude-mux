package ccwatch

import (
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/kylesnowschwartz/agent-ouija/claude/tools"
)

// ToolVerb maps a tool call to its shared.ts MetadataVerb. The watcher knows
// the tool name and input; tagging here spares renderers from regex-guessing
// the verb back out of the message. Unknown/custom tools return "".
//
// agent-ouija's tool taxonomy is the base map, so Claude Code, Codex, and the
// other agents it knows share one classification; tcmVerbOverrides holds the
// tcm-specific exceptions to it. A shell call is classified by its first
// real command, looked through wrappers such as sudo or timeout (see
// ShellVerb): "web" when it reaches the network, search, list, or read when
// it only looks at files, and "run" otherwise, since agents read, search,
// and fetch through the shell.
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
// and VAR=value assignments are skipped. Wrappers such as sudo, env,
// timeout, time, nice and xargs are looked through to the command they run;
// a wrapper that runs nothing (`command -v`, a bare `sudo`) is "run".
// A command that reaches the network (curl, gh, ssh, `git push`,
// `npm install`, `go mod download`, ...) is "web". Otherwise, a command that
// redirects its output into a file (`cat > f`) is a write ("run");
// read-only commands map to search, list, or read; sed is read only when it
// prints with -n and does not edit in place. Everything else, and an empty
// command, is "run".
func ShellVerb(command string) string {
	words := unwrap(firstCommand(command))
	if len(words) == 0 {
		return "run"
	}
	if reachesNetwork(words) {
		return "web"
	}
	if writesFile(words[1:]) {
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

// shellWrapper describes a command that runs another command. takes lists
// its flags that consume the next word; skip is how many words of its own
// follow the flags; a flag in asks means it runs nothing.
type shellWrapper struct {
	takes []string
	skip  int
	asks  []string
}

var shellWrappers = map[string]shellWrapper{
	"sudo":       {takes: []string{"-u", "-g", "-p", "-h", "-C", "-D", "-R", "-T", "-U", "-r", "-t"}},
	"doas":       {takes: []string{"-u", "-C"}},
	"env":        {takes: []string{"-u", "-C"}},
	"time":       {},
	"timeout":    {takes: []string{"-s", "-k"}, skip: 1},
	"nohup":      {},
	"nice":       {takes: []string{"-n"}},
	"ionice":     {takes: []string{"-c", "-n"}},
	"stdbuf":     {takes: []string{"-i", "-o", "-e"}},
	"caffeinate": {takes: []string{"-t", "-w"}},
	"command":    {asks: []string{"-v", "-V"}},
	"exec":       {takes: []string{"-a"}},
	"xargs":      {takes: []string{"-n", "-I", "-P", "-L", "-d", "-E", "-s", "-a", "-J", "-R"}},
}

// maxUnwrapDepth bounds how many nested wrappers unwrap looks through.
const maxUnwrapDepth = 6

// unwrap returns the words of the command a chain of wrappers runs, with
// VAR=value assignments after a wrapper removed. Words that do not start
// with a wrapper are returned as they are; a wrapper that runs nothing is
// returned as itself.
func unwrap(words []string) []string {
	for range maxUnwrapDepth {
		if len(words) == 0 {
			return words
		}
		w, ok := shellWrappers[path.Base(words[0])]
		if !ok || (len(words) > 1 && slices.Contains(w.asks, words[1])) {
			return words
		}
		words = pastFlags(words[1:], w.takes)
		for len(words) > 0 && isAssignment(words[0]) {
			words = words[1:]
		}
		words = words[min(w.skip, len(words)):]
	}
	return words
}

// pastFlags returns the words after the leading flags; a flag in takes
// consumes the word after it.
func pastFlags(words, takes []string) []string {
	i := 0
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		if slices.Contains(takes, words[i]) {
			i += 2
		} else {
			i++
		}
	}
	return words[min(i, len(words)):]
}

// netCommands reach the network whatever their arguments.
var netCommands = map[string]bool{
	"curl": true, "wget": true, "gh": true, "ssh": true, "scp": true, "sftp": true,
	"ftp": true, "rsync": true, "nc": true, "ncat": true, "telnet": true,
	"ping": true, "dig": true, "nslookup": true,
}

// netSubcommands are commands that reach the network only with some
// subcommands, one or two words long.
var netSubcommands = map[string][]string{
	"git":            {"push", "pull", "fetch", "clone", "ls-remote"},
	"npm":            {"install", "i", "ci", "add", "update", "publish", "view", "audit"},
	"pnpm":           {"install", "i", "add", "update", "publish", "dlx"},
	"yarn":           {"install", "add", "upgrade", "publish", "dlx"},
	"bun":            {"install", "add", "update", "publish"},
	"pip":            {"install", "download"},
	"pip3":           {"install", "download"},
	"pipx":           {"install", "upgrade", "upgrade-all", "run"},
	"uv":             {"add", "sync", "lock", "publish", "tool install"},
	"poetry":         {"install", "add", "update", "lock", "publish"},
	"docker":         {"pull", "push", "login"},
	"docker compose": {"pull", "push"},
	"brew":           {"install", "update", "upgrade", "fetch"},
	"cargo":          {"install", "fetch", "publish", "update"},
	"go":             {"get", "install", "mod download"},
	"apt":            {"install", "update", "upgrade", "full-upgrade", "dist-upgrade"},
	"apt-get":        {"install", "update", "upgrade", "dist-upgrade"},
}

// pythonModules are programs `python -m` runs that reach the network the
// same way their own command does.
var pythonModules = map[string]bool{"pip": true, "uv": true, "pipx": true, "poetry": true}

// gitGlobalTakes and dockerGlobalTakes are the flags before the subcommand
// that consume the next word.
var (
	gitGlobalTakes    = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env"}
	dockerGlobalTakes = []string{"-c", "--context", "-H", "--host", "--config", "-l", "--log-level", "-f", "--file", "-p", "--project-name", "--profile", "--env-file", "--project-directory"}
)

// reachesNetwork reports whether a command reaches the network, by its name
// or by its name and subcommand. npx counts only when told to install
// (-y, --yes).
func reachesNetwork(words []string) bool {
	head, rest := path.Base(words[0]), words[1:]
	if netCommands[head] {
		return true
	}
	if isPython(head) && len(rest) > 1 && rest[0] == "-m" && pythonModules[rest[1]] {
		head, rest = rest[1], rest[2:]
	}
	if head == "uv" && len(rest) > 0 && rest[0] == "pip" {
		head, rest = "pip", rest[1:]
	}
	switch head {
	case "git":
		rest = pastFlags(rest, gitGlobalTakes)
	case "docker":
		rest = pastFlags(rest, dockerGlobalTakes)
		if len(rest) > 0 && rest[0] == "compose" {
			head, rest = "docker compose", pastFlags(rest[1:], dockerGlobalTakes)
		}
	case "npx":
		return slices.Contains(rest, "-y") || slices.Contains(rest, "--yes")
	}
	subs, ok := netSubcommands[head]
	if !ok || len(rest) == 0 {
		return false
	}
	if slices.Contains(subs, rest[0]) {
		return true
	}
	return len(rest) > 1 && slices.Contains(subs, rest[0]+" "+rest[1])
}

// isPython reports whether name is a Python interpreter: python, python3,
// python3.12, or py.
func isPython(name string) bool {
	if name == "py" {
		return true
	}
	v, ok := strings.CutPrefix(name, "python")
	return ok && strings.Trim(v, "0123456789.") == ""
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

// FleetStepVerb maps a worker step kind to an activity verb.
func FleetStepVerb(kind string) string {
	switch kind {
	case "read", "search":
		return kind
	case "edit", "delete", "move":
		return "edit"
	case "execute":
		return "run"
	case "think":
		return "thinking"
	case "fetch":
		return "web"
	default:
		return "tool"
	}
}
