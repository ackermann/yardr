// Package agents knows the coding-agent CLIs herdr can start: how each
// lists its models and which flags stop it from asking for permission.
// Every flag here was taken from the agent's own --help; model lists are
// read live from the installed tool, so they stay current.
package agents

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Permission is a named set of flags that makes an agent ask less.
type Permission struct {
	Label string
	Args  []string
	Note  string
}

type Preset struct {
	Kind        string
	Label       string
	Permissions []Permission
	// models lists the installed agent's models; nil when it cannot.
	models func(ctx context.Context) ([]string, error)
}

// Presets are the kinds offered first, in display order.
var Presets = []Preset{
	{Kind: "claude", Label: "Claude Code", models: claudeModels, Permissions: []Permission{
		{"auto", []string{"--permission-mode", "auto"}, "Claude judges each action; asks only about risky ones"},
		{"accept edits", []string{"--permission-mode", "acceptEdits"}, "file edits allowed; shell commands still ask"},
		{"bypass", []string{"--dangerously-skip-permissions"}, "never asks; runs as you with full access"},
	}},
	{Kind: "codex", Label: "Codex", models: codexModels, Permissions: []Permission{
		{"approve for me", []string{"--approve-for-me"}, "automatic review inside a workspace-write sandbox"},
		{"bypass", []string{"--dangerously-bypass-approvals-and-sandbox"}, "never asks, no sandbox"},
	}},
	{Kind: "grok", Label: "Grok", models: grokModels, Permissions: []Permission{
		{"always approve", []string{"--always-approve"}, "auto-approves all tool executions"},
	}},
	{Kind: "kimi", Label: "Kimi", models: kimiModels, Permissions: []Permission{
		{"yolo", []string{"--yolo"}, "auto-approves tool calls; may still ask questions"},
		{"auto", []string{"--auto"}, "fully autonomous; asks nothing"},
	}},
	{Kind: "opencode", Label: "opencode", models: opencodeModels, Permissions: []Permission{
		{"auto", []string{"--auto"}, "auto-approves anything not explicitly denied"},
	}},
	{Kind: "pi", Label: "pi", models: piModels},
}

// HerdrKinds are the agent kinds herdr 0.9 can start (herdr agent start
// --help); a custom kind must be one of them.
var HerdrKinds = []string{"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp",
	"mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo",
	"qodercli", "qwen", "letta", "maki", "muse"}

// Lookup returns the preset for kind, if there is one.
func Lookup(kind string) (Preset, bool) {
	for _, p := range Presets {
		if p.Kind == kind {
			return p, true
		}
	}
	return Preset{}, false
}

// Models lists the installed agent's models, bounded in time. A preset
// without a source, or a failing tool, yields an empty list and the error.
func (p Preset) Models(ctx context.Context) ([]string, error) {
	if p.models == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return p.models(ctx)
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// claude names its model aliases in the --model help: e.g. 'fable', 'opus'.
func claudeModels(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "claude", "--help")
	if err != nil {
		return nil, err
	}
	return ParseClaudeHelp(out), nil
}

var quoted = regexp.MustCompile(`'([a-z0-9][a-z0-9.\-]*)'`)

func ParseClaudeHelp(help string) []string {
	var block strings.Builder
	in := false
	for _, line := range strings.Split(help, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "--model"):
			in = true
		case in && strings.HasPrefix(t, "-"):
			in = false
		}
		if in {
			block.WriteString(line + "\n")
		}
	}
	return uniq(matches(quoted, block.String()))
}

// codex has no list command; offer the model its config uses.
func codexModels(ctx context.Context) ([]string, error) {
	home, _ := os.UserHomeDir()
	return tomlValues(filepath.Join(home, ".codex", "config.toml"), regexp.MustCompile(`^model\s*=\s*"([^"]+)"`))
}

func grokModels(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "grok", "models")
	if err != nil && out == "" {
		return nil, err
	}
	return ParseGrokModels(out), nil
}

// ParseGrokModels reads "Available models:" followed by "  * id (default)"
// or "  id" lines.
func ParseGrokModels(out string) []string {
	var models []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Available models") {
			in = true
			continue
		}
		if !in || t == "" {
			continue
		}
		t = strings.TrimPrefix(t, "* ")
		if i := strings.Index(t, " ("); i > 0 {
			t = t[:i]
		}
		if f := strings.Fields(t); len(f) == 1 {
			models = append(models, f[0])
		}
	}
	return uniq(models)
}

func kimiModels(ctx context.Context) ([]string, error) {
	home, _ := os.UserHomeDir()
	return tomlValues(filepath.Join(home, ".kimi-code", "config.toml"), regexp.MustCompile(`^\[models\."([^"]+)"\]`))
}

func opencodeModels(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "opencode", "models")
	if err != nil {
		return nil, err
	}
	return ParseOpencodeModels(out), nil
}

// ParseOpencodeModels reads one provider/model id per line.
func ParseOpencodeModels(out string) []string {
	var models []string
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.ContainsAny(t, " \t") && strings.Contains(t, "/") {
			models = append(models, t)
		}
	}
	return uniq(models)
}

func piModels(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "pi", "--list-models")
	if err != nil {
		return nil, err
	}
	return ParsePiModels(out), nil
}

// ParsePiModels reads pi's table (provider, model, ...) as provider/model,
// the form pi's --model accepts.
func ParsePiModels(out string) []string {
	var models []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] == "provider" {
			continue
		}
		models = append(models, f[0]+"/"+f[1])
	}
	return uniq(models)
}

func tomlValues(path string, re *regexp.Regexp) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := re.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
			out = append(out, m[1])
		}
	}
	return uniq(out), sc.Err()
}

func matches(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// unattended are the flags an agent yardr starts runs with by default, so
// it does not stop at a prompt nobody is watching: what `yardr init` gives
// the yardmaster and what `group add --help` suggests. A kind not listed runs
// with none and may ask; its own permission flags are the operator's call.
var unattended = map[string][]string{
	"claude": {"--permission-mode", "auto"},
	"codex":  {"--dangerously-bypass-approvals-and-sandbox"},
	"kimi":   {"--yolo"},
}

// UnattendedArgs are kind's unattended flags (see unattended); nil when it
// has none.
func UnattendedArgs(kind string) []string { return unattended[kind] }

// UnattendedHelp lists the unattended flags per kind, one line each, for
// command help.
func UnattendedHelp() string {
	var b strings.Builder
	for _, k := range Kinds() {
		if args := unattended[k]; args != nil {
			b.WriteString("  " + k + ": " + strings.Join(args, " ") + "\n")
		}
	}
	b.WriteString("  any other kind: none; it may stop to ask")
	return b.String()
}

// Kinds are the herdr kinds, presets first in their display order.
func Kinds() []string {
	var out []string
	for _, p := range Presets {
		out = append(out, p.Kind)
	}
	for _, k := range HerdrKinds {
		if _, ok := Lookup(k); !ok {
			out = append(out, k)
		}
	}
	return out
}

// IsKind reports whether herdr can start kind.
func IsKind(kind string) bool {
	for _, k := range HerdrKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// executables are the kinds whose command is not named after the kind.
var executables = map[string]string{"cursor": "cursor-agent"}

// Executable is the command herdr runs for kind.
func Executable(kind string) string {
	if e, ok := executables[kind]; ok {
		return e
	}
	return kind
}

// versionArgs are the kinds whose harness prints its version and exits,
// with the flag that makes it. Each was run against the installed tool; a
// kind not listed was not, and is not asked (some agents open their
// interface on an unknown flag instead of failing).
var versionArgs = map[string][]string{
	"claude":   {"--version"},
	"codex":    {"--version"},
	"grok":     {"--version"},
	"kimi":     {"--version"},
	"opencode": {"--version"},
	"pi":       {"--version"},
}

// VersionArgs are the arguments that make kind's harness print its version
// and exit; nil when none is known.
func VersionArgs(kind string) []string { return versionArgs[kind] }

// oneShot are the kinds whose harness takes one prompt, answers it and
// exits without a terminal, with the arguments before the prompt. Each was
// taken from the agent's own --help (claude -p/--print, codex exec, grok
// -p/--single, kimi -p/--prompt, opencode run, pi -p/--print); a kind not
// listed has no form known here. codex exec refuses to run outside a git
// repository without --skip-git-repo-check.
var oneShot = map[string][]string{
	"claude":   {"-p"},
	"codex":    {"exec", "--skip-git-repo-check"},
	"grok":     {"-p"},
	"kimi":     {"-p"},
	"opencode": {"run"},
	"pi":       {"-p"},
}

// OneShotArgs are the arguments that make kind's harness answer prompt and
// exit: one real call to its model. nil when no such form is known.
func OneShotArgs(kind, prompt string) []string {
	args, ok := oneShot[kind]
	if !ok {
		return nil
	}
	return append(append([]string{}, args...), prompt)
}

// installHints say where to get a harness, for the kinds whose source is
// known here. yardr prints them and never runs them.
var installHints = map[string]string{
	"claude":   "npm install -g @anthropic-ai/claude-code (https://claude.com/claude-code)",
	"codex":    "npm install -g @openai/codex",
	"opencode": "https://opencode.ai",
}

// InstallHint is where to get kind's harness: the command or page known for
// it, else a pointer to the agent's own documentation.
func InstallHint(kind string) string {
	if h, ok := installHints[kind]; ok {
		return h
	}
	name := kind
	if p, ok := Lookup(kind); ok {
		name = p.Label
	}
	return "see " + name + "'s own documentation; it must be on PATH as " + Executable(kind)
}
