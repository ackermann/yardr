package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"yardr.dev/yardr/harnesses"
)

// FileVersion is the shape of a harness file this yardr reads: the file's
// `version`. A file of another shape is refused, not half read.
const FileVersion = 1

// DefaultKind is what the herdr runner starts when a config names no kind.
const DefaultKind = "claude"

// Harness is what yardr knows of a harness kind that herdr has no field
// for, as the kind's file says it (harnesses/<kind>.yaml, embedded). Whether
// an agent is idle, working, blocked or done is herdr's to say and is not
// here. A kind with no file is the zero Harness: no marks, no error lines,
// no flags.
type Harness struct {
	Kind    string `yaml:"-" json:"kind"`
	Version int    `yaml:"version" json:"version"`
	// Label is the harness's name as people write it.
	Label string `yaml:"label" json:"label"`
	// Provider is who makes the harness, as people write it
	// ("Anthropic"). "" when the file names none.
	Provider string `yaml:"provider" json:"provider,omitempty"`
	// Aliases are other names the kind is asked for by (Lookup).
	Aliases []string `yaml:"aliases" json:"aliases,omitempty"`
	// Install says where to get the harness: yardr prints it and never
	// runs it. "" when no source is known here.
	Install string `yaml:"install" json:"install,omitempty"`
	// Fetch is how the harness gets onto a Mac without a terminal, for a
	// program that fetches it; nil when no such way is known, and the
	// harness is installed by hand, as Install says.
	Fetch *Fetch `yaml:"fetch" json:"fetch,omitempty"`
	// Trust is the harness's folder-trust dialog, which yardr answers
	// (internal/runner/trust.go); nil when it answers none.
	Trust *Trust `yaml:"trust" json:"trust,omitempty"`
	Marks Marks  `yaml:"marks" json:"marks"`
	Flags Flags  `yaml:"flags" json:"flags"`
	// Offers are the models the file suggests a person pick: a list to
	// choose from, not what is installed, which Models lists live. nil
	// when the file names none.
	Offers []Offer      `yaml:"offers" json:"offers,omitempty"`
	Model  *ModelSource `yaml:"model" json:"model,omitempty"`
	Errors []ErrorLine  `yaml:"errors" json:"errors,omitempty"`
	// Dialogs are the questions the harness is known to draw in place of
	// its input, which yardr tells of and never answers.
	Dialogs []Dialog `yaml:"dialogs" json:"dialogs,omitempty"`
	// Login are the lines of the harness's login screen, drawn in place
	// of its input when it has no login, which yardr types nothing into
	// and tells of as not logged in.
	Login []LoginLine `yaml:"login" json:"login,omitempty"`
	// LoginRecipe is how a program leads the harness's login without a
	// terminal: its own login command, not the screen Login names. nil
	// when none was read, and the harness is logged in in a terminal.
	LoginRecipe *LoginRecipe `yaml:"login_recipe" json:"login_recipe,omitempty"`
	// Background are the lines the harness draws around its input while
	// work its agent started in the background still runs (a shell): the
	// agent waits for it and the harness wakes it, so it is idle to herdr
	// and not to yardr's idle limit.
	Background []BackgroundLine `yaml:"background" json:"background,omitempty"`
	Hints      Hints            `yaml:"hints" json:"hints,omitzero"`
}

// Trust is one folder-trust dialog yardr answers itself: the text that
// marks it on the pane's screen, the answers that accept it, and the mark
// of the dialog's selected answer, as the harness prints them. The dialog
// is answered only when the mark shows, one answer shows exactly, and the
// cursor is seen on that answer (internal/runner/trust.go). There is one
// answer per harness version that renamed it: Kimi 2.2.0 renamed 'Trust
// this folder' to 'Trust and continue', so its file names both. Env are
// entries that let a harness skip this dialog; the runner sets their active
// values only when it is allowed to answer the dialog. EnvNames exposes
// their names without values in JSON. The cursor is the dialog's own mark:
// Kimi's is ❯ though its input mark is ">".
type Trust struct {
	Dialog   string   `yaml:"dialog" json:"dialog"`
	Answers  []string `yaml:"answers" json:"answers"`
	Cursor   string   `yaml:"cursor" json:"cursor"`
	Env      []string `yaml:"env" json:"-"`
	EnvNames []string `yaml:"-" json:"env,omitempty"`
}

// Marks are the marks the pane of a harness is read by. Input begins the
// first line of its input, and Message a message it took, which it echoes
// above the input. Edge, for a kind that draws its input in a box, is the
// side of the box: it comes before the input mark, and before and after
// each line of the input's text, of which it is no part. Error is every
// mark the harness's TUI may draw before a message of its own, taken off a
// line before it is read for an error.
type Marks struct {
	Input   string `yaml:"input" json:"input,omitempty"`
	Message string `yaml:"message" json:"message,omitempty"`
	Edge    string `yaml:"edge" json:"edge,omitempty"`
	Error   string `yaml:"error" json:"error,omitempty"`
}

// Flags are the harness's own arguments yardr knows a use for. Each was
// taken from the agent's own --help, or run against the installed tool.
type Flags struct {
	// Model is the flag that selects a model when yardr starts an agent.
	Model string `yaml:"model" json:"model,omitempty"`
	// Unattended are the flags an agent yardr starts runs with by default,
	// so it does not stop at a prompt nobody is watching: what `yardr init`
	// gives the yardmaster and what `group add --help` suggests. A kind
	// with none may ask; its permission flags are the operator's call.
	Unattended []string `yaml:"unattended" json:"unattended,omitempty"`
	// Version makes the harness print its version and exit. A kind with
	// none is not asked: some agents open their interface on an unknown
	// flag instead of failing.
	Version []string `yaml:"version" json:"version,omitempty"`
	// OneShot comes before a prompt the harness answers and exits on,
	// without a terminal.
	OneShot []string `yaml:"one_shot" json:"one_shot,omitempty"`
	// Always is added to the arguments of every agent of the kind yardr
	// starts, whatever its group says; the kind's file says why.
	Always []string `yaml:"always" json:"always,omitempty"`
	// Permissions are the named flag sets offered where a person chooses
	// how much an agent may do without asking.
	Permissions []Permission `yaml:"permissions" json:"permissions,omitempty"`
}

// Permission is a named set of flags that makes an agent ask less.
type Permission struct {
	Label string   `yaml:"label" json:"label"`
	Args  []string `yaml:"args" json:"args"`
	Note  string   `yaml:"description" json:"description"`
}

// Offer is one model a harness file suggests: the id the kind's --model
// flag takes, the label a person reads, and what it is recommended for.
// Recommended is "yardmaster" on the one frontier model a yardmaster should
// run on, and empty on the others. A list in a file goes stale, but a wrong
// id fails loudly at the first session; the file's comment says where each
// id came from and on what date. A suggestion, not a check: init takes any
// id, whether the file names it or not.
type Offer struct {
	Model       string `yaml:"model" json:"model"`
	Label       string `yaml:"label" json:"label"`
	Recommended string `yaml:"recommended,omitempty" json:"recommended,omitempty"`
}

// ModelSource is where the model a session's agent itself reports is read:
// the agent's own session file, one JSON value per line. The file is the
// first that matches Glob, a slash path with {session} where the session id
// herdr reports for the pane goes, under the harness's own directory: the
// one HomeEnv names in the environment, else Home in the user's home. The
// model is the string at Field, a path of keys joined by dots, in the first
// line whose type is Type (any line, for no Type) that has one. The type is
// read from the key TypeField names, "type" when the file names none.
type ModelSource struct {
	Home    string `yaml:"home" json:"home"`
	HomeEnv string `yaml:"home_env" json:"home_env,omitempty"`
	Glob    string `yaml:"glob" json:"glob"`
	Type    string `yaml:"type" json:"type,omitempty"`
	// TypeField is the key a line's type is read from, "type" when the
	// file names none. Muse's lines name theirs "payload_type".
	TypeField string `yaml:"type_field" json:"type_field,omitempty"`
	Field     string `yaml:"field" json:"field"`
}

// sessionID is where a ModelSource's Glob takes the session id.
const sessionID = "{session}"

// Pattern is the glob of the session file of the session id, under the
// harness's own directory dir.
func (s ModelSource) Pattern(dir, id string) string {
	return filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(s.Glob, sessionID, id)))
}

// ModelIn is the model one line of the session file names, "" when it
// names none: the line is not of Type, or has no string at Field. A name
// that begins with "<" is the harness's own message (Claude Code's
// "<synthetic>", an API error shown as a reply), not a model's.
func (s ModelSource) ModelIn(line []byte) string {
	var v map[string]any
	if json.Unmarshal(line, &v) != nil {
		return ""
	}
	key := s.TypeField
	if key == "" {
		key = "type"
	}
	if t, _ := v[key].(string); s.Type != "" && t != s.Type {
		return ""
	}
	keys := strings.Split(s.Field, ".")
	for _, k := range keys[:len(keys)-1] {
		if v, _ = v[k].(map[string]any); v == nil {
			return ""
		}
	}
	model, _ := v[keys[len(keys)-1]].(string)
	if strings.HasPrefix(model, "<") {
		return ""
	}
	return model
}

// Reasons an error line of a harness gives (dispatch's
// session_harness_error). ReasonOther is an error line that is known to
// stop the harness and is none of the others (a model it may not use, no
// credit, an API error).
const (
	ReasonAtCapacity  = "at_capacity"
	ReasonNotLoggedIn = "not_logged_in"
	ReasonRateLimited = "rate_limited"
	ReasonOther       = "other"
)

// Reasons are the reasons an error line may give.
var Reasons = []string{ReasonAtCapacity, ReasonRateLimited, ReasonNotLoggedIn, ReasonOther}

// ErrorLine is one error line a harness is known to stop on: what it means,
// and a regular expression the line matches with the harness's marks taken
// off (Marks.Error). A kind's lines are tried in the order of its file, and
// the first that matches gives the reason.
type ErrorLine struct {
	Reason  string `yaml:"reason" json:"reason"`
	Pattern string `yaml:"pattern" json:"pattern"`
	re      *regexp.Regexp
}

// Matches reports whether line is this error line.
func (e ErrorLine) Matches(line string) bool { return e.re.MatchString(line) }

// Dialog is one line of a question a harness draws in place of its input:
// the word yardr calls the question by, and a regular expression a line of
// it matches, without the white space around it. A question with several
// lines that may be its first has one Dialog for each, under one word.
type Dialog struct {
	Dialog  string `yaml:"dialog" json:"dialog"`
	Pattern string `yaml:"pattern" json:"pattern"`
	re      *regexp.Regexp
}

// Matches reports whether line is of this dialog.
func (d Dialog) Matches(line string) bool { return d.re.MatchString(line) }

// LoginLine is one line of the harness's login screen: a regular
// expression a line of the pane's end matches, without the white space
// around it.
type LoginLine struct {
	Pattern string `yaml:"pattern" json:"pattern"`
	re      *regexp.Regexp
}

// Matches reports whether line is of the login screen.
func (l LoginLine) Matches(line string) bool { return l.re.MatchString(line) }

// BackgroundLine is one line a harness draws while background work of its
// agent runs: a regular expression a line of the pane's end matches, without
// the white space around it.
type BackgroundLine struct {
	Pattern string `yaml:"pattern" json:"pattern"`
	re      *regexp.Regexp
}

// Matches reports whether line says background work runs.
func (b BackgroundLine) Matches(line string) bool { return b.re.MatchString(line) }

// Hints are what a kind's file adds to hints yardr gives of an agent of the
// kind: what this harness does there that no other is known to. Each is a
// sentence without its full stop, set into the hint as it stands; a kind
// with none adds nothing.
type Hints struct {
	// Reset goes into the hint of a prompt yardr gave at a usage limit's
	// reset that does not show in the agent's pane (dispatch's
	// session_harness_error with prompt_shown false): what the harness may
	// be asking first.
	Reset string `yaml:"reset" json:"reset,omitempty"`
}

// modelLists say how each kind lists the models of the installed agent,
// which is code, not something a file can say (see models.go).
var modelLists = map[string]func(ctx context.Context) ([]string, error){
	"claude":   claudeModels,
	"codex":    codexModels,
	"grok":     grokModels,
	"kimi":     kimiModels,
	"muse":     museModels,
	"opencode": opencodeModels,
	"pi":       piModels,
}

// Harnesses are the kinds yardr has a file for, in display order (see
// order). They are read once, when the program starts: the files are part
// of the binary, so one that cannot be read is a fault of the build, which
// TestHarnessFiles finds.
var Harnesses = func() []Harness {
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
func index(hs []Harness) map[string]Harness {
	m := map[string]Harness{}
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
func Lookup(kind string) (Harness, bool) {
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
func Load(fsys fs.FS) ([]Harness, error) {
	files, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, err
	}
	var out []Harness
	names := map[string]string{}
	for _, file := range files {
		body, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		h := Harness{Kind: strings.TrimSuffix(file, ".yaml")}
		dec := yaml.NewDecoder(bytes.NewReader(body))
		dec.KnownFields(true)
		if err := dec.Decode(&h); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := h.check(); err != nil {
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

// harnessEnvKey is a name of a variable: the env_file rule
// (internal/config/envfile.go), for a file's own entries.
var harnessEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// check finds what a harness file says that yardr cannot go by, and
// compiles its error lines.
func (h *Harness) check() error {
	switch {
	case h.Version != FileVersion:
		return fmt.Errorf("version %d: this yardr reads version %d", h.Version, FileVersion)
	case !IsKind(h.Kind):
		return fmt.Errorf("%s is no kind herdr starts", h.Kind)
	case h.Label == "":
		return fmt.Errorf("no label")
	case h.Marks.Input == "" && (h.Marks.Message != "" || h.Marks.Edge != ""):
		return fmt.Errorf("marks: message or edge without input, which is what a pane is read from")
	}
	for _, p := range h.Flags.Permissions {
		if p.Label == "" || len(p.Args) == 0 {
			return fmt.Errorf("flags.permissions: one without label or args")
		}
	}
	if t := h.Trust; t != nil {
		if t.Dialog == "" || len(t.Answers) == 0 || slices.Contains(t.Answers, "") || t.Cursor == "" {
			return fmt.Errorf("trust: a dialog yardr answers names its text, its accepting answers and its cursor")
		}
		t.EnvNames = nil
		for i, e := range t.Env {
			k, _, ok := strings.Cut(e, "=")
			if !ok || !harnessEnvKey.MatchString(k) {
				return fmt.Errorf("trust.env: entry %d: want KEY=VALUE", i+1)
			}
			t.EnvNames = append(t.EnvNames, k)
		}
	}
	if h.Fetch != nil {
		if err := h.Fetch.Check(Executable(h.Kind)); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
	}
	if h.Flags.Model != "" && !strings.HasPrefix(h.Flags.Model, "-") {
		return fmt.Errorf("flags.model: %q is not a flag", h.Flags.Model)
	}
	if m := h.Model; m != nil {
		switch {
		case m.Home == "" || m.Field == "":
			return fmt.Errorf("model: home and field are needed")
		case !strings.Contains(m.Glob, sessionID):
			return fmt.Errorf("model: glob %q has no %s, so it finds any session's file", m.Glob, sessionID)
		}
	}
	if len(h.Offers) > 0 {
		if len(h.Offers) < 2 || len(h.Offers) > 4 {
			return fmt.Errorf("offers: %d entries, want 2 to 4", len(h.Offers))
		}
		marked := 0
		for _, o := range h.Offers {
			if o.Model == "" {
				return fmt.Errorf("offers: one without a model id")
			}
			if o.Label == "" {
				return fmt.Errorf("offers: one without a label")
			}
			if o.Recommended != "" {
				marked++
			}
		}
		if marked != 1 {
			return fmt.Errorf("offers: %d recommended, want exactly one", marked)
		}
	}
	for i := range h.Errors {
		e := &h.Errors[i]
		if !slices.Contains(Reasons, e.Reason) {
			return fmt.Errorf("errors: reason %q of %q is none of %s", e.Reason, e.Pattern, strings.Join(Reasons, ", "))
		}
		re, err := regexp.Compile(e.Pattern)
		if err != nil || e.Pattern == "" {
			return fmt.Errorf("errors: pattern %q: %v", e.Pattern, err)
		}
		e.re = re
	}
	for i := range h.Dialogs {
		d := &h.Dialogs[i]
		if d.Dialog == "" {
			return fmt.Errorf("dialogs: %q has no word to call the dialog by", d.Pattern)
		}
		re, err := regexp.Compile(d.Pattern)
		if err != nil || d.Pattern == "" {
			return fmt.Errorf("dialogs: pattern %q: %v", d.Pattern, err)
		}
		d.re = re
	}
	for i := range h.Login {
		l := &h.Login[i]
		re, err := regexp.Compile(l.Pattern)
		if err != nil || l.Pattern == "" {
			return fmt.Errorf("login: pattern %q: %v", l.Pattern, err)
		}
		l.re = re
	}
	if h.LoginRecipe != nil {
		if err := h.LoginRecipe.check(); err != nil {
			return fmt.Errorf("login_recipe: %w", err)
		}
	}
	for i := range h.Background {
		b := &h.Background[i]
		re, err := regexp.Compile(b.Pattern)
		if err != nil || b.Pattern == "" {
			return fmt.Errorf("background: pattern %q: %v", b.Pattern, err)
		}
		b.re = re
	}
	return nil
}

// Models lists the installed agent's models, bounded in time. A kind
// without a source, or a failing tool, yields an empty list and the error.
func (h Harness) Models(ctx context.Context) ([]string, error) {
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
	return "see " + name + "'s own documentation; it must be on PATH as " + Executable(kind)
}
