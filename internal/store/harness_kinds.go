package store

// HerdrKinds are the agent kinds herdr 0.9 can start (herdr agent start
// --help); a custom kind must be one of them.
var HerdrKinds = []string{"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp",
	"mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo",
	"qodercli", "qwen", "letta", "maki", "muse"}

// executables are the kinds whose command is not named after the kind.
var executables = map[string]string{"cursor": "cursor-agent", "kiro": "kiro-cli"}

// Executable is the command herdr runs for kind.
func Executable(kind string) string {
	if e, ok := executables[kind]; ok {
		return e
	}
	return kind
}

// HarnessLabels are the labels of the kinds yardr has a harness file for,
// as their files write them. The store prints them on secret asks and their
// answers; the files are the catalogue's, and a test in agents pins this
// table to them, so a file that renames a label fails until this follows.
// Read-only: the catalogue is the writer.
var HarnessLabels = map[string]string{
	"claude":   "Claude Code",
	"codex":    "Codex",
	"grok":     "Grok",
	"kimi":     "Kimi",
	"kiro":     "Kiro",
	"muse":     "Muse Code",
	"opencode": "opencode",
	"pi":       "pi",
}

// HarnessLoginEnv are the API-key variables of the kinds whose harness file
// names login_env. The store asks for and gives a harness key by them; a
// test in agents pins this table to the files. Read-only.
var HarnessLoginEnv = map[string][]string{
	"claude": {"ANTHROPIC_API_KEY"},
}

// PresetNames are the permission presets some harness offers, each in its
// one-word form (PresetLabel). The store holds permissions_preset to them;
// a test in agents pins this list to the files. Read-only.
var PresetNames = []string{
	"accept-edits",
	"allow-all-tools",
	"always-approve",
	"approve-for-me",
	"auto",
	"bypass",
	"no-approval",
	"read-only",
	"trust-all-tools",
	"yolo",
}
