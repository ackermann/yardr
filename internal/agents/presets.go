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
