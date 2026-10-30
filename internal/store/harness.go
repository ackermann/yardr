package store

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FileVersion is the shape of a harness file this yardr reads: the file's
// `version`. A file of another shape is refused, not half read.
const FileVersion = 1

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
	// Context is where the context use of a session's agent is read: the
	// same session file the model source names, read for its last use.
	// nil when the file names none, and the kind has no context-based
	// rotation.
	Context *ContextSource `yaml:"context" json:"context,omitempty"`
	// Usage is where the tokens a whole session used are read: the same
	// session file the model source names, once the session has ended. The
	// entries are tried in order and the first that finds a line is the
	// reading, so a harness's own total can come first and a sum of its
	// lines after it as a fallback. nil when the file names none, and the
	// kind's sessions are not counted.
	Usage  []UsageSource `yaml:"usage" json:"usage,omitempty"`
	Errors []ErrorLine   `yaml:"errors" json:"errors,omitempty"`
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
	// LoginEnv are the variables of an API key the harness runs with
	// when they are set in its session's environment, named here and
	// not in yardr's code. A kind that names them gets an "API key"
	// choice on the yard's first-login ask, which writes the given key
	// into the crew member's environment and leads no login program; a
	// kind without it gets no such choice. nil when the file names
	// none, and the harness is logged in some other way.
	LoginEnv []string `yaml:"login_env" json:"login_env,omitempty"`
	// ApiKey is the dialog the harness draws on its first start with
	// such a key set and no login, which yardr answers with the
	// accepting answer, as it answers folder trust: the key is only
	// ever in the session's environment by the operator's hand. nil
	// when the file names none, and the dialog is left for a human.
	ApiKey *Trust `yaml:"api_key" json:"api_key,omitempty"`
	// Onboarding are the first-start screens the harness draws in a
	// fresh home, before any login and before any input, which yardr
	// answers in turn as it answers folder trust, each entry once
	// wherever it shows. An entry with no cursor is answered with
	// Enter alone, without moving one. nil when the file names none.
	Onboarding []Trust `yaml:"onboarding" json:"onboarding,omitempty"`
	// Background are the lines the harness draws around its input while
	// work its agent started in the background still runs (a shell): the
	// agent waits for it and the harness wakes it, so it is idle to herdr
	// and not to yardr's idle limit.
	Background []BackgroundLine `yaml:"background" json:"background,omitempty"`
	Hints      Hints            `yaml:"hints" json:"hints,omitzero"`
}

// Marks are the marks the pane of a harness is read by. Input begins the
// first line of its input, and Message a message it took, which it echoes
// above the input. Edge, for a kind that draws its input in a box, is the
// side of the box: it comes before the input mark, and before and after
// each line of the input's text, of which it is no part. Placeholder is
// the input's hint when it holds nothing, for a harness that draws one in
// no faint style: while the input's first line begins with it, the input
// reads empty. PlaceholderSGR is the colour such a hint is drawn in, for a
// harness whose hint's words are not fixed: text drawn in it reads as no
// text, as faint text does. Error is every mark the harness's TUI may draw
// before a message of its own, taken off a line before it is read for an
// error.
type Marks struct {
	Input          string `yaml:"input" json:"input,omitempty"`
	Message        string `yaml:"message" json:"message,omitempty"`
	Edge           string `yaml:"edge" json:"edge,omitempty"`
	Placeholder    string `yaml:"placeholder" json:"placeholder,omitempty"`
	PlaceholderSGR string `yaml:"placeholder_sgr" json:"placeholder_sgr,omitempty"`
	Error          string `yaml:"error" json:"error,omitempty"`
}

// parseSGRColor is colour, a foreground colour as a harness file names it,
// in its canonical shape: `38;2;r;g;b` or `38;5;n`. A colour yardr cannot
// match would leave the hint it names reading as held text, so anything
// else is refused with the file.
func parseSGRColor(colour string) (string, error) {
	bad := func() (string, error) {
		return "", fmt.Errorf("want 38;2;r;g;b or 38;5;n, each number 0 to 255")
	}
	parts := strings.Split(colour, ";")
	if len(parts) < 3 || parts[0] != "38" {
		return bad()
	}
	nums := make([]int, 0, len(parts)-2)
	for _, p := range parts[2:] {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return bad()
		}
		nums = append(nums, n)
	}
	switch {
	case parts[1] == "2" && len(nums) == 3:
		return fmt.Sprintf("38;2;%d;%d;%d", nums[0], nums[1], nums[2]), nil
	case parts[1] == "5" && len(nums) == 1:
		return fmt.Sprintf("38;5;%d", nums[0]), nil
	}
	return bad()
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
	// Always is added first to the arguments of every agent of the kind
	// yardr starts, before the group's own, whatever the group says; the
	// kind's file says why.
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
	// Workspace finds the session file of a session the runtime reports
	// no id for (herdr names none for Muse panes): the file whose
	// workspace line names the session's worktree. nil when the file
	// names none, and the kind's sessions are found by id alone.
	Workspace *WorkspaceSource `yaml:"workspace" json:"workspace,omitempty"`
}

// WorkspaceSource is how the session file of a session with no reported id
// is found: by the worktree the session works in. A file qualifies when one
// of its lines is of Type (any line, for no Type) and names the worktree at
// Field, a path of keys joined by dots, and when the file's start, the time
// of its first line with one that reads, falls in the session's window. At
// is the path to a line's own time, read as a UsageSource's is: a string as
// RFC 3339, a number as Unix time in AtUnit ("s", "ms" or "us"; "s" when
// none is named). The type is read with the model source's own TypeField:
// the workspace line is a line of the same file.
type WorkspaceSource struct {
	Type   string `yaml:"type" json:"type,omitempty"`
	Field  string `yaml:"field" json:"field"`
	At     string `yaml:"at" json:"at"`
	AtUnit string `yaml:"at_unit" json:"at_unit,omitempty"`
}

// WorkspaceLine is what one line of a session file holds towards finding
// the file by its worktree: the worktree the line works in, when the line
// is of the workspace type and names one, and when the line was written,
// whatever its type. A line with neither reads as ok == false.
type WorkspaceLine struct {
	Workspace string
	At        time.Time
}

// WorkspaceIn is what one line of a session file holds towards finding the
// file by its worktree (WorkspaceLine). ok is false when the source names
// no workspace, and when the line names no worktree and has no time that
// reads.
func (s ModelSource) WorkspaceIn(line []byte) (WorkspaceLine, bool) {
	var v map[string]any
	if s.Workspace == nil || json.Unmarshal(line, &v) != nil {
		return WorkspaceLine{}, false
	}
	var l WorkspaceLine
	key := s.TypeField
	if key == "" {
		key = "type"
	}
	if t, _ := v[key].(string); s.Workspace.Type == "" || t == s.Workspace.Type {
		l.Workspace, _ = valueAt(v, s.Workspace.Field).(string)
	}
	l.At = atTime(v, s.Workspace.At, s.Workspace.AtUnit)
	return l, l.Workspace != "" || !l.At.IsZero()
}

// SessionIn is the session id the path of a matched file holds: the segment
// where Glob has {session}, when path is a file the glob matched with
// {session} read as *. dir is the harness's own directory the pattern was
// read under. ok is false when path is not under dir, has not the glob's
// segments, or the glob holds {session} inside a longer segment, which
// names no id this way.
func (s ModelSource) SessionIn(dir, path string) (id string, ok bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	segs := strings.Split(rel, "/")
	want := strings.Split(s.Glob, "/")
	if len(segs) != len(want) {
		return "", false
	}
	for i, w := range want {
		if w == sessionID {
			return segs[i], true
		}
	}
	return "", false
}

// sessionID is where a ModelSource's Glob takes the session id.
const sessionID = "{session}"

// DefaultContextTokens are the fields of a usage object that add up to
// what a session holds when its file names none: what the last request
// sent, from the prompt's own tokens and its cache. Output tokens are
// what the session said, not what it holds.
var DefaultContextTokens = []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"}

// ContextSource is where the context use of a session's agent is read:
// the agent's own session file, the one the model source names for the
// session id herdr reports. The use is the numbers at Usage, a path of
// keys joined by dots to an object, in the last line whose type is Type
// (any line, for no Type) that has one. Tokens are the fields of that
// object that add up to what the session holds, DefaultContextTokens
// when the file names none. The type is read from the key TypeField
// names, "type" when the file names none.
type ContextSource struct {
	Type      string   `yaml:"type" json:"type,omitempty"`
	TypeField string   `yaml:"type_field" json:"type_field,omitempty"`
	Usage     string   `yaml:"usage" json:"usage"`
	Tokens    []string `yaml:"tokens" json:"tokens,omitempty"`
}

// tokens is the fields of a usage object that add up to what a session
// holds: the file's own, or the default.
func (s ContextSource) tokens() []string {
	if len(s.Tokens) > 0 {
		return s.Tokens
	}
	return DefaultContextTokens
}

// UsageIn is the context use one line of the session file holds, ok when
// the line is of Type and has numbers at Usage. ok is false for any
// other line, so the last ok line is the session's use.
func (s ContextSource) UsageIn(line []byte) (used int, ok bool) {
	var v map[string]any
	if json.Unmarshal(line, &v) != nil {
		return 0, false
	}
	key := s.TypeField
	if key == "" {
		key = "type"
	}
	if t, _ := v[key].(string); s.Type != "" && t != s.Type {
		return 0, false
	}
	u := v
	for _, k := range strings.Split(s.Usage, ".") {
		if u, _ = u[k].(map[string]any); u == nil {
			return 0, false
		}
	}
	used, ok = 0, false
	for _, k := range s.tokens() {
		if n, is := u[k].(float64); is {
			used, ok = used+int(n), true
		}
	}
	return used, ok
}

// UsageSource is where the tokens a whole session used are read: the agent's
// own session file, the one the model source names. It is ContextSource made
// to tell what a session used and not what it holds, in five ways:
//
//   - TypeField may be a path of keys joined by dots, as Usage is: Codex
//     keeps the type of its token lines in payload.type.
//   - A step of the Usage path that is "*" takes every value of the map at
//     that step, so Claude's modelUsage.* adds up every model it called, the
//     side calls it makes with a small model included.
//   - Read says which lines count: the last one that has numbers ("last", the
//     default, and what ContextSource does) or all of them ("sum"). Key is a
//     path to a string in the line and narrows "sum" to the last line for each
//     value found there: Claude Code writes one line for each content block
//     of a message and every one repeats the whole message's usage, so keyed
//     by message.id each message counts once.
//   - The four counts are named by what they mean: Input, Output, CacheRead
//     and CacheWrite each name the field that holds it, and one that names
//     none reads as 0. Input stays as the harness reports it. CacheInInput
//     says that figure includes what the harness read from its cache (Codex's
//     and Muse's do), and UsageIn then derives FreshInput, the input less the
//     cache, so that one harness's fresh input adds up with another's. A
//     harness that reports its input apart from its cache (Claude's does) has
//     a FreshInput equal to its Input.
//   - At is a path to the line's own time: a string is read as RFC 3339, a
//     number as Unix time in AtUnit ("s", "ms" or "us"; "s" when none is
//     named). UsageIn gives it to a caller that counts only up to a time.
//
// Partial says that what the source reads falls short of what the session
// used, as a fallback does; the caller keeps it with the count.
type UsageSource struct {
	Type         string `yaml:"type" json:"type,omitempty"`
	TypeField    string `yaml:"type_field" json:"type_field,omitempty"`
	Usage        string `yaml:"usage" json:"usage"`
	Read         string `yaml:"read" json:"read,omitempty"`
	Key          string `yaml:"key" json:"key,omitempty"`
	Partial      bool   `yaml:"partial" json:"partial,omitempty"`
	At           string `yaml:"at" json:"at,omitempty"`
	AtUnit       string `yaml:"at_unit" json:"at_unit,omitempty"`
	Input        string `yaml:"input" json:"input,omitempty"`
	Output       string `yaml:"output" json:"output,omitempty"`
	CacheRead    string `yaml:"cache_read" json:"cache_read,omitempty"`
	CacheWrite   string `yaml:"cache_write" json:"cache_write,omitempty"`
	CacheInInput bool   `yaml:"cache_in_input" json:"cache_in_input,omitempty"`
}

// Usage is what a session used. Input is what it sent, as its harness reports
// it: with the cache read in it for a harness whose input includes the cache
// (Codex, Muse) and without it for one whose does not (Claude), so Input does
// not add up across harnesses. FreshInput is what it sent that its provider
// did not serve from the cache, which does: UsageIn derives it from Input.
// CacheRead is what the provider served from the cache, CacheWrite what it
// stored there, and Output what the agent said, its thinking included. The
// tokens a session newly spent are FreshInput, CacheWrite and Output together
// (design section 3.4); CacheRead is left out of them.
type Usage struct {
	Input      int `json:"input"`
	FreshInput int `json:"fresh_input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Add is u and o added up, count by count.
func (u Usage) Add(o Usage) Usage {
	return Usage{Input: u.Input + o.Input, FreshInput: u.FreshInput + o.FreshInput, Output: u.Output + o.Output, CacheRead: u.CacheRead + o.CacheRead, CacheWrite: u.CacheWrite + o.CacheWrite}
}

// UsageLine is what one line of the session file reads as under a
// UsageSource. At is the line's own time, the zero time when the source names
// none or the line has none that reads: its counts still stand, but a caller
// that counts only up to a time cannot place such a line and leaves it out.
// Key is the string at the source's Key, "" when it names none.
type UsageLine struct {
	Usage Usage
	At    time.Time
	Key   string
}

// UsageIn is what one line of the session file holds, ok when the line is of
// Type and has at least one of the source's fields as a number at Usage. A
// source with a Key reads no line that has no string there: such a line
// cannot be told from the repeat of another, and a count that may be too
// high is worse than one that is known to be short. ok is false for any
// other line, so the lines that are ok are the ones the source reads.
func (s UsageSource) UsageIn(line []byte) (UsageLine, bool) {
	var v map[string]any
	if json.Unmarshal(line, &v) != nil {
		return UsageLine{}, false
	}
	key := s.TypeField
	if key == "" {
		key = "type"
	}
	if t, _ := valueAt(v, key).(string); s.Type != "" && t != s.Type {
		return UsageLine{}, false
	}
	objects := objectsAt(v, strings.Split(s.Usage, "."))
	found := false
	count := func(field string) (n int) {
		if field == "" {
			return 0 // a count the file names no field for
		}
		for _, o := range objects {
			if x, is := o[field].(float64); is {
				n, found = n+int(x), true
			}
		}
		return n
	}
	u := Usage{Input: count(s.Input), Output: count(s.Output), CacheRead: count(s.CacheRead), CacheWrite: count(s.CacheWrite)}
	if !found {
		return UsageLine{}, false
	}
	u.FreshInput = u.Input
	if s.CacheInInput {
		u.FreshInput -= u.CacheRead
	}
	l := UsageLine{Usage: u}
	if s.Key != "" {
		if l.Key, _ = valueAt(v, s.Key).(string); l.Key == "" {
			return UsageLine{}, false
		}
	}
	l.At = s.timeIn(v)
	return l, true
}

// timeIn is the time of the line v by the source's At: the zero time when it
// names none or the line's does not read.
func (s UsageSource) timeIn(v map[string]any) time.Time {
	return atTime(v, s.At, s.AtUnit)
}

// atTime is the time v holds at the path at: a string reads as RFC 3339, a
// number as Unix time in the unit atUnit names. The zero time when at names
// none or the line's does not read.
func atTime(v map[string]any, at, unitName string) time.Time {
	if at == "" {
		return time.Time{}
	}
	switch at := valueAt(v, at).(type) {
	case string:
		t, _ := time.Parse(time.RFC3339, at)
		return t.UTC()
	case float64:
		unit, ok := atUnit(unitName)
		if !ok {
			return time.Time{}
		}
		// Whole units into seconds and a remainder by integer arithmetic: a
		// float64 holds a microsecond Unix time exactly, but not that many
		// nanoseconds.
		whole, frac := math.Modf(at)
		perSec := int64(time.Second / unit)
		return time.Unix(int64(whole)/perSec, int64(whole)%perSec*int64(unit)+int64(frac*float64(unit))).UTC()
	}
	return time.Time{}
}

// atUnit is the unit a file's at_unit names, "s" for none; ok is false for a
// name that is no unit.
func atUnit(name string) (unit time.Duration, ok bool) {
	switch name {
	case "", "s":
		return time.Second, true
	case "ms":
		return time.Millisecond, true
	case "us":
		return time.Microsecond, true
	}
	return 0, false
}

// valueAt is the value at path, a path of keys joined by dots, in v; nil
// when a step is missing or is not an object.
func valueAt(v map[string]any, path string) any {
	var at any = v
	for _, k := range strings.Split(path, ".") {
		m, ok := at.(map[string]any)
		if !ok {
			return nil
		}
		at = m[k]
	}
	return at
}

// objectsAt are the objects at steps in v: a step that is "*" goes through
// every value of the map there that is an object, any other through the key
// it names.
func objectsAt(v map[string]any, steps []string) []map[string]any {
	if len(steps) == 0 {
		return []map[string]any{v}
	}
	var out []map[string]any
	into := func(x any) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, objectsAt(m, steps[1:])...)
		}
	}
	if steps[0] != "*" {
		into(v[steps[0]])
		return out
	}
	for _, x := range v {
		into(x)
	}
	return out
}

// check finds what an entry of a file's usage says that yardr cannot go by:
// it would read no line, or a wrong number without saying so.
func (s UsageSource) check() error {
	if _, ok := atUnit(s.AtUnit); !ok {
		return fmt.Errorf("at_unit %q is none of s, ms, us", s.AtUnit)
	}
	switch {
	case s.Usage == "":
		return fmt.Errorf("usage is needed, the path to the counts its lines hold")
	case s.Input == "" && s.Output == "" && s.CacheRead == "" && s.CacheWrite == "":
		return fmt.Errorf("none of input, output, cache_read and cache_write names a field, so there is nothing to count")
	case s.Read != "" && s.Read != "last" && s.Read != "sum":
		return fmt.Errorf("read %q is neither last nor sum", s.Read)
	case s.Key != "" && s.Read != "sum":
		return fmt.Errorf("key counts one line for each of its values under read: sum, and this entry does not sum")
	case s.CacheInInput && s.Input == "":
		return fmt.Errorf("cache_in_input names no input to take the cache off")
	case s.CacheInInput && s.CacheRead == "":
		return fmt.Errorf("cache_in_input names no cache_read to take off input")
	}
	return nil
}

// A UsageTally adds up the lines of one session file as their UsageSource
// says to read them: UsageIn each line, Add those that read in the order of
// the file, and ask for the Usage. Read "last" keeps the last line, "sum"
// adds every line up, and a Key keeps the last line for each value of it and
// adds those up.
type UsageTally struct {
	src   UsageSource
	added bool
	last  Usage
	sum   Usage
	byKey map[string]Usage
}

// Tally starts a tally of lines read by s, with none added yet.
func (s UsageSource) Tally() *UsageTally { return &UsageTally{src: s} }

// Add counts l, a line that UsageIn read.
func (t *UsageTally) Add(l UsageLine) {
	t.added = true
	switch {
	case t.src.Read != "sum":
		t.last = l.Usage
	case t.src.Key != "":
		if t.byKey == nil {
			t.byKey = map[string]Usage{}
		}
		t.byKey[l.Key] = l.Usage
	default:
		t.sum = t.sum.Add(l.Usage)
	}
}

// Usage is what the lines added so far say the session used. ok is false
// while none was added, which is not a count of 0: the file may only not
// have said yet.
func (t *UsageTally) Usage() (u Usage, ok bool) {
	switch {
	case !t.added:
		return Usage{}, false
	case t.src.Read != "sum":
		return t.last, true
	case t.src.Key != "":
		for _, k := range t.byKey {
			u = u.Add(k)
		}
		return u, true
	}
	return t.sum, true
}

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

// harnessEnvKey is a name of a variable: the env_file rule
// (internal/config/envfile.go), for a file's own entries.
var harnessEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Check finds what a harness file says that yardr cannot go by, and
// compiles its error lines.
func (h *Harness) Check() error {
	switch {
	case h.Version != FileVersion:
		return fmt.Errorf("version %d: this yardr reads version %d", h.Version, FileVersion)
	case !slices.Contains(HerdrKinds, h.Kind):
		return fmt.Errorf("%s is no kind herdr starts", h.Kind)
	case h.Label == "":
		return fmt.Errorf("no label")
	case h.Marks.Input == "" && (h.Marks.Message != "" || h.Marks.Edge != "" || h.Marks.Placeholder != "" || h.Marks.PlaceholderSGR != ""):
		return fmt.Errorf("marks: message, edge, placeholder or placeholder_sgr without input, which is what a pane is read from")
	}
	if sgr := h.Marks.PlaceholderSGR; sgr != "" {
		colour, err := parseSGRColor(sgr)
		if err != nil {
			return fmt.Errorf("marks: placeholder_sgr %q: %w", sgr, err)
		}
		h.Marks.PlaceholderSGR = colour
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
	if c := h.Context; c != nil {
		switch {
		case h.Model == nil:
			return fmt.Errorf("context: without a model source there is no session file to read it from")
		case c.Usage == "":
			return fmt.Errorf("context: usage is needed, the path to the use its lines hold")
		case slices.Contains(c.Tokens, ""):
			return fmt.Errorf("context: tokens names an empty field")
		}
	}
	if len(h.Usage) > 0 && h.Model == nil {
		return fmt.Errorf("usage: without a model source there is no session file to read it from")
	}
	for i, u := range h.Usage {
		if err := u.check(); err != nil {
			return fmt.Errorf("usage: entry %d: %w", i+1, err)
		}
	}
	if h.Model != nil && h.Model.Workspace != nil {
		w := h.Model.Workspace
		switch {
		case w.Field == "":
			return fmt.Errorf("workspace: field is needed, the path to the worktree its lines hold")
		case w.At == "":
			return fmt.Errorf("workspace: at is needed, the path to the time its lines were written")
		}
		if _, ok := atUnit(w.AtUnit); !ok {
			return fmt.Errorf("workspace: at_unit %q is none of s, ms, us", w.AtUnit)
		}
		whole := 0
		for _, seg := range strings.Split(h.Model.Glob, "/") {
			if strings.Contains(seg, sessionID) {
				if seg != sessionID {
					return fmt.Errorf("workspace: glob %q holds %s inside a longer segment, so a matched file names no session", h.Model.Glob, sessionID)
				}
				whole++
			}
		}
		if whole != 1 {
			return fmt.Errorf("workspace: glob %q must hold %s as one whole segment, so a matched file names its session", h.Model.Glob, sessionID)
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
	seen := map[string]bool{}
	for _, e := range h.LoginEnv {
		switch {
		case !harnessEnvKey.MatchString(e):
			return fmt.Errorf("login_env: %q is no name of a variable", e)
		case seen[e]:
			return fmt.Errorf("login_env: %q is named twice", e)
		}
		seen[e] = true
	}
	if k := h.ApiKey; k != nil {
		if k.Dialog == "" || len(k.Answers) == 0 || slices.Contains(k.Answers, "") || k.Cursor == "" {
			return fmt.Errorf("api_key: a dialog yardr answers names its text, its accepting answers and its cursor")
		}
		if len(k.Env) > 0 {
			return fmt.Errorf("api_key takes no env: nothing skips this dialog")
		}
	}
	for i := range h.Onboarding {
		o := &h.Onboarding[i]
		if o.Dialog == "" || len(o.Answers) == 0 || slices.Contains(o.Answers, "") {
			return fmt.Errorf("onboarding: entry %d: a screen yardr answers names its text and its accepting answers", i+1)
		}
		if len(o.Env) > 0 {
			return fmt.Errorf("onboarding: entry %d takes no env: nothing skips these screens", i+1)
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

// PresetLabel is a permission preset's label as one word, as a flag takes
// it: lower case, with each space a dash ("accept edits" reads
// "accept-edits"). A preset is found by this form, so either spelling
// names it.
func PresetLabel(label string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(label)), " ", "-")
}
