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

// HarnessPresets are each harness kind's permission presets, as its file
// writes them (label, args, description). The store holds a crew add card
// to them, so that it imports no internal package; a test in agents pins
// this table to the catalogue, so a file that renames a label or changes
// its flags fails until this follows. Read-only.
var HarnessPresets = map[string][]Permission{
	"claude": {
		{Label: "auto", Args: []string{"--permission-mode", "auto"}, Note: "Claude judges each action; asks only about risky ones"},
		{Label: "accept edits", Args: []string{"--permission-mode", "acceptEdits"}, Note: "file edits allowed; shell commands still ask"},
		{Label: "bypass", Args: []string{"--dangerously-skip-permissions"}, Note: "never asks; runs as you with full access"},
		{Label: "read only", Args: []string{"--settings", `{"sandbox":{"enabled":true,"filesystem":{"denyWrite":["{workdir}"]},"network":{"allowUnixSockets":["{socket}"]}}}`}, Note: "reads in a read-only sandbox, no network but the yard's socket; asks for anything else"},
	},
	"codex": {
		{Label: "approve for me", Args: []string{"--approve-for-me"}, Note: "automatic review inside a workspace-write sandbox"},
		{Label: "bypass", Args: []string{"--dangerously-bypass-approvals-and-sandbox"}, Note: "never asks, no sandbox"},
		{Label: "read only", Args: []string{"--enable", "network_proxy", "-c", `default_permissions="yardr_read_only"`, "-c", `permissions.yardr_read_only.extends=":read-only"`, "-c", "permissions.yardr_read_only.network.enabled=true", "-c", `permissions.yardr_read_only.network.unix_sockets={"{socket}"="allow"}`, "-c", `permissions.yardr_read_only.filesystem={"{tmpdir}"="write"}`}, Note: "reads in a read-only sandbox, writes only to a private temporary directory, no network but the yard's socket; asks for anything else; needs Codex's network_proxy feature"},
	},
	"grok": {
		{Label: "always approve", Args: []string{"--always-approve"}, Note: "auto-approves all tool executions"},
	},
	"kimi": {
		{Label: "yolo", Args: []string{"--yolo"}, Note: "auto-approves tool calls; may still ask questions"},
		{Label: "auto", Args: []string{"--auto"}, Note: "fully autonomous; asks nothing"},
	},
	"kiro": {
		{Label: "allow all tools", Args: []string{"--trust-tools=*"}, Note: "every built-in tool without asking; a Hook confirmation is denied"},
		{Label: "trust all tools", Args: []string{"--trust-all-tools"}, Note: "everything without asking, after one warning acknowledged at every start"},
	},
	"muse": {
		{Label: "yolo", Args: []string{"--yolo"}, Note: "never asks, no sandbox, trusts the workspace for the run"},
		{Label: "no approval", Args: []string{"--disable-approval"}, Note: "no approval prompts; the sandbox still applies"},
	},
	"opencode": {
		{Label: "auto", Args: []string{"--auto"}, Note: "auto-approves anything not explicitly denied"},
	},
	"pi": {},
}

// HarnessPreset is kind's permission preset called label, in either
// spelling (PresetLabel or as the file writes it). None is no preset.
func HarnessPreset(kind, label string) (Permission, bool) {
	want := PresetLabel(label)
	for _, p := range HarnessPresets[kind] {
		if PresetLabel(p.Label) == want {
			return p, true
		}
	}
	return Permission{}, false
}

// HarnessPresetLabels are kind's permission presets' labels as its file
// writes them, in file order.
func HarnessPresetLabels(kind string) []string {
	var out []string
	for _, p := range HarnessPresets[kind] {
		out = append(out, p.Label)
	}
	return out
}
