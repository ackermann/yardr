// Package agents knows the coding-agent CLIs herdr can start: what yardr
// knows of each kind beyond what herdr does, read from the kind's file
// (Harness), and how each lists its models. Model lists are read live from
// the installed tool, so they stay current.
package agents

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// HerdrKinds are the agent kinds herdr 0.9 can start (herdr agent start
// --help); a custom kind must be one of them.
var HerdrKinds = []string{"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp",
	"mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo",
	"qodercli", "qwen", "letta", "maki", "muse"}

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

// Kinds are the herdr kinds, those yardr has a file for first, in their
// display order.
func Kinds() []string {
	var out []string
	for _, h := range Harnesses {
		out = append(out, h.Kind)
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
