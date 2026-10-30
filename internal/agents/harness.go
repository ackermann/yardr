package agents

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"yardr.dev/yardr/harnesses"
	"yardr.dev/yardr/internal/store"
)

// DefaultKind is what the herdr runner starts when a config names no kind.
const DefaultKind = "claude"

// modelLists say how each kind lists the models of the installed agent,
// which is code, not something a file can say (see models.go).
var modelLists = map[string]func(ctx context.Context) ([]string, error){
	"claude":   claudeModels,
	"codex":    codexModels,
	"grok":     grokModels,
	"kimi":     kimiModels,
	"kiro":     kiroModels,
	"muse":     museModels,
	"opencode": opencodeModels,
	"pi":       piModels,
}

// Harnesses are the kinds yardr has a file for, in display order (see
// order). They are read once, when the program starts: the files are part
// of the binary, so one that cannot be read is a fault of the build, which
// TestHarnessFiles finds.
var Harnesses = func() []store.Harness {
	hs, err := Load(harnesses.FS)
	if err != nil {
		panic("yardr's own harness files: " + err.Error())
	}
	return hs
}()

// byName is each of Harnesses by the names it is asked for by.
var byName = index(Harnesses)

// order is the kinds offered first, in display order: the first is what a
// yard is created on when nothing says otherwise. A kind with a file that
// is not named here follows them, by name.
var order = []string{"claude", "codex", "grok", "kimi", "opencode", "pi"}

// index is each of hs by its kind and by its aliases.
func index(hs []store.Harness) map[string]store.Harness {
	m := map[string]store.Harness{}
	for _, h := range hs {
		m[h.Kind] = h
		for _, a := range h.Aliases {
			m[a] = h
		}
	}
	return m
}

// Lookup returns the harness of kind, if yardr has a file for it; the zero
// Harness, which knows nothing, if not.
func Lookup(kind string) (store.Harness, bool) {
	h, ok := byName[kind]
	return h, ok
}

// Kind returns the kind a herdr config names, or the runner's default when
// it does not name one.
func Kind(cfg map[string]any) string {
	if kind, _ := cfg["kind"].(string); kind != "" {
		return kind
	}
	return DefaultKind
}

// Load reads every <kind>.yaml of fsys into a Harness, in display order.
func Load(fsys fs.FS) ([]store.Harness, error) {
	files, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, err
	}
	var out []store.Harness
	names := map[string]string{}
	for _, file := range files {
		body, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		h := store.Harness{Kind: strings.TrimSuffix(file, ".yaml")}
		dec := yaml.NewDecoder(bytes.NewReader(body))
		dec.KnownFields(true)
		if err := dec.Decode(&h); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := h.Check(); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for _, n := range append([]string{h.Kind}, h.Aliases...) {
			if other, taken := names[n]; taken {
				return nil, fmt.Errorf("%s: %s is already %s's name", file, n, other)
			}
			names[n] = h.Kind
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		place := func(kind string) int {
			if i := slices.Index(order, kind); i >= 0 {
				return i
			}
			return len(order)
		}
		if a, b := place(out[i].Kind), place(out[j].Kind); a != b {
			return a < b
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// Models lists the installed agent's models, bounded in time. A kind
// without a source, or a failing tool, yields an empty list and the error.
func Models(h store.Harness, ctx context.Context) ([]string, error) {
	models := modelLists[h.Kind]
	if models == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return models(ctx)
}

// UnattendedArgs are kind's unattended flags (Flags.Unattended); nil when
// it has none.
func UnattendedArgs(kind string) []string { return byName[kind].Flags.Unattended }

// Preset is kind's permission preset called label, found by PresetLabel;
// false when the kind offers none of that name. It is how a named preset
// (yardr init --permissions, a yard's permissions_preset) is held to the
// harness that will run with it.
func Preset(kind, label string) (store.Permission, bool) {
	h, ok := Lookup(kind)
	if !ok {
		return store.Permission{}, false
	}
	want := store.PresetLabel(label)
	for _, p := range h.Flags.Permissions {
		if store.PresetLabel(p.Label) == want {
			return p, true
		}
	}
	return store.Permission{}, false
}

// PresetLabels are kind's permission presets' labels as its file writes
// them, in the file's order; nil when it offers none.
func PresetLabels(kind string) []string {
	h, ok := Lookup(kind)
	if !ok {
		return nil
	}
	out := make([]string, len(h.Flags.Permissions))
	for i, p := range h.Flags.Permissions {
		out[i] = p.Label
	}
	return out
}

// TrustEnv are the KEY=VALUE entries the pane of kind starts with for its
// folder-trust dialog. When enabled is false, each key is set to an empty
// value so the harness cannot inherit a trust bypass from herdr or env_file.
func TrustEnv(kind string, enabled bool) []string {
	h, ok := Lookup(kind)
	if !ok || h.Trust == nil {
		return nil
	}
	if enabled {
		return h.Trust.Env
	}
	out := make([]string, 0, len(h.Trust.Env))
	for _, e := range h.Trust.Env {
		k, _, _ := strings.Cut(e, "=")
		out = append(out, k+"=")
	}
	return out
}

// MemoryOff are the environment entries and harness arguments that switch
// kind's own memory off in a session the yard starts, so the yard's memory
// is the only one the session has. A kind with no switch reads (nil, nil).
// The runner puts the entries after the session's env file wins, and omits
// Codex's disabling argument when the session's own args enable memories,
// so the operator turns the harness's own memory back on per session
// (CLAUDE_CODE_DISABLE_AUTO_MEMORY=0, --enable memories).
//
// Claude Code's switch is the environment (code.claude.com/docs/en/env-vars,
// 2.1.310): CLAUDE_CODE_DISABLE_AUTO_MEMORY=1, "When disabled, Claude does
// not create or load auto memory files"; 0 forces it on. With it set the
// system prompt carries no memory section either (asked live, yardr-2eka).
// Its --bare and --safe-mode also disable auto memory but skip much else.
// Codex keeps its own in ~/.codex/memories, behind the memories feature
// (stable, off unless enabled); `codex --help` (0.160.1) documents
// --disable memories for it, and names no environment switch (only
// CODEX_HOME; CODEX_DISABLE_PROJECT_DOC is project docs, not memory). No
// other kind is researched: whether it keeps a memory of its own, and what
// switches it off, is unknown here.
func MemoryOff(kind string) (env, args []string) {
	switch kind {
	case "claude":
		return []string{"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1"}, nil
	case "codex":
		return nil, []string{"--disable", "memories"}
	default:
		return nil, nil
	}
}

// MemoryDir is where kind's harness keeps its own memory for project under
// the user's home; false when no such place is known. Only names are ever
// read from it (see doctor); its contents are the harness's. Claude Code's
// project is the git repository root, shared across its worktrees and
// subdirectories; outside git it is the project root. Every character
// outside letters, digits and "-" reads as "-", as its project directories
// show (yardr-2eka). Codex's memories are global to ~/.codex, not per
// project, and off unless enabled, so no directory names them.
func MemoryDir(kind, home, project string) (string, bool) {
	if kind != "claude" {
		return "", false
	}
	var b strings.Builder
	for _, r := range project {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return filepath.Join(home, ".claude", "projects", b.String(), "memory"), true
}

// UnattendedHelp lists the unattended flags per kind, one line each, for
// command help.
func UnattendedHelp() string {
	var b strings.Builder
	for _, k := range Kinds() {
		if args := UnattendedArgs(k); args != nil {
			b.WriteString("  " + k + ": " + strings.Join(args, " ") + "\n")
		}
	}
	b.WriteString("  any other kind: none; it may stop to ask")
	return b.String()
}

// VersionArgs are the arguments that make kind's harness print its version
// and exit; nil when none is known.
func VersionArgs(kind string) []string { return byName[kind].Flags.Version }

// harnessVersionTimeout bounds reading an installed harness's version; a
// test shortens it. A harness answers in well under a second, but five
// seconds ran out under load even on a stand-in that answers at once, so
// this is the twenty Models allows: both ask one installed tool for one
// short answer.
var harnessVersionTimeout = 20 * time.Second

// HarnessVersion is the version the installed harness of kind reports: the
// first line its file's version flags print, "" when the file names none,
// the harness is not installed, or it does not answer in time. It runs a
// program but changes nothing, as doctor's probe does.
func HarnessVersion(ctx context.Context, kind string) string {
	h, ok := Lookup(kind)
	if !ok || len(h.Flags.Version) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, harnessVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, store.Executable(kind), h.Flags.Version...).Output()
	if err != nil {
		return ""
	}
	return versionLine(string(out))
}

// versionLine is the first line of out that is not blank, cut to a length
// an event carries.
func versionLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 60 {
				l = string(r[:60]) + "…"
			}
			return l
		}
	}
	return ""
}

// OneShotArgs are the arguments that make kind's harness answer prompt and
// exit: one real call to its model. nil when no such form is known.
func OneShotArgs(kind, prompt string) []string {
	args := byName[kind].Flags.OneShot
	if len(args) == 0 {
		return nil
	}
	return append(append([]string{}, args...), prompt)
}

// InstallHint is where to get kind's harness: the command or page known for
// it, else a pointer to the agent's own documentation.
func InstallHint(kind string) string {
	h, ok := Lookup(kind)
	if h.Install != "" {
		return h.Install
	}
	name := kind
	if ok {
		name = h.Label
	}
	return "see " + name + "'s own documentation; it must be on PATH as " + store.Executable(kind)
}
