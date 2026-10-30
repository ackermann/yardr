// Package agents knows the coding-agent CLIs herdr can start: what yardr
// knows of each kind beyond what herdr does, read from the kind's file
// (store.Harness), and how each lists its models. Model lists are read live
// from the installed tool, so they stay current.
package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/store"
)

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

// kiro serves its models as JSON: `chat --list-models --format json` (the
// CLI commands reference, https://kiro.dev/docs/reference/cli-commands/).
func kiroModels(ctx context.Context) ([]string, error) {
	out, err := run(ctx, "kiro-cli", "chat", "--list-models", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParseKiroModels([]byte(out)), nil
}

// ParseKiroModels reads the model_ids of `kiro-cli chat --list-models
// --format json`: an object with a models array, each with a model_id, and
// a default_model, as 2.15.2 printed it (kirodotdev/kirocrew#1355; the
// same shape in two more tools that read it). Anything else names none.
func ParseKiroModels(body []byte) []string {
	var list struct {
		Models []struct {
			Model string `json:"model_id"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []string
	for _, m := range list.Models {
		if m.Model != "" {
			out = append(out, m.Model)
		}
	}
	return uniq(out)
}

// museModels reads the installed agent's model catalog: one JSON file per
// provider and profile under .local/share/muse/model-catalog, each with
// rows of models. Only rows the catalog marks visible are listed: the
// hidden ones are the same models' contributor variants and an image
// model, not the list a coding model is picked from.
func museModels(ctx context.Context) ([]string, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "muse", "model-catalog")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []string
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, ParseMuseCatalog(body)...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no visible models in %s", dir)
	}
	return uniq(out), nil
}

// ParseMuseCatalog reads the model_ids of the rows a model catalog file
// marks visible. A file that is not the catalog names none.
func ParseMuseCatalog(body []byte) []string {
	var catalog struct {
		Rows []struct {
			Model      string `json:"model_id"`
			Visibility string `json:"visibility"`
		} `json:"rows"`
	}
	if json.Unmarshal(body, &catalog) != nil {
		return nil
	}
	var out []string
	for _, r := range catalog.Rows {
		if r.Visibility == "visible" && r.Model != "" {
			out = append(out, r.Model)
		}
	}
	return uniq(out)
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

// KimiHook is one [[hooks]] table of Kimi's config: the event it runs on
// and the command it runs.
type KimiHook struct {
	Event, Command string
}

// A hooks table of Kimi's config, and the two keys doctor reads, as herdr's
// integration and moshi-hook write them:
//
//	[[hooks]]
//	event = "Stop"
//	command = "bash '/Users/jo/.kimi-code/hooks/herdr-agent-state.sh' idle"
var (
	kimiHooksTable  = regexp.MustCompile(`^\[\[\s*hooks\s*\]\]\s*(?:#.*)?$`)
	kimiHookEvent   = regexp.MustCompile(`^event\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')\s*(?:#.*)?$`)
	kimiHookCommand = regexp.MustCompile(`^command\s*=\s*("(?:[^"\\]|\\.)*"|'[^']*')\s*(?:#.*)?$`)
)

// ParseKimiHooks reads the [[hooks]] tables of Kimi's config: the event and
// the command of each, in order. Anything else a table holds (matcher,
// timeout) is not read.
func ParseKimiHooks(config string) []KimiHook {
	var out []KimiHook
	in := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case kimiHooksTable.MatchString(line):
			out = append(out, KimiHook{})
			in = true
		case strings.HasPrefix(line, "["):
			in = false
		case in:
			if m := kimiHookEvent.FindStringSubmatch(line); m != nil {
				out[len(out)-1].Event = tomlString(m[1])
			} else if m := kimiHookCommand.FindStringSubmatch(line); m != nil {
				out[len(out)-1].Command = tomlString(m[1])
			}
		}
	}
	return out
}

// tomlString is the value of a TOML basic or literal string.
func tomlString(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s[1 : len(s)-1]
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
	for _, k := range store.HerdrKinds {
		if _, ok := Lookup(k); !ok {
			out = append(out, k)
		}
	}
	return out
}

// IsKind reports whether herdr can start kind.
func IsKind(kind string) bool {
	for _, k := range store.HerdrKinds {
		if k == kind {
			return true
		}
	}
	return false
}
