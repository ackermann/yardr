package source

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// herdrKinds are the agent kinds herdr 0.9 can start, a copy of
// store.HerdrKinds: this package cannot import the store until the store
// stops importing it (yardr-j8vt), and it cannot import agents since agents
// imports the store (yardr-8nca). The copy goes away with yardr-j8vt.
var herdrKinds = []string{"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline", "omp",
	"mastracode", "opencode", "copilot", "kimi", "kiro", "droid", "amp", "grok", "hermes", "kilo",
	"qodercli", "qwen", "letta", "maki", "muse"}

// ProviderMark is where, in the command of a limits key, the provider's own
// name for the harness kind asked about goes.
const ProviderMark = "{provider}"

// Limits says how a source answers what a harness kind's subscription still
// carries: by a command of the operator's, which the yard runs. It is the
// one thing of a declaration that is run and not sent, so it is the
// operator's alone to add and no pack brings it.
//
// A source with no kinds answers for itself instead: its command takes no
// ProviderMark and is asked by the source's name, for the windows it
// reports. yardr limits names it beside the providers, and a route's
// condition names its windows as source.window.
type Limits struct {
	// Command is the program and its arguments, run without a shell. One
	// argument at least holds ProviderMark, unless the source answers for
	// itself; the program never does.
	Command []string `json:"command"`
	// Kinds maps a harness kind (a group's or a crew member's kind) to the
	// provider's own name for it. A kind that is not here has no limits
	// known through this source. None at all, and the source answers for
	// itself.
	Kinds map[string]string `json:"kinds"`
}

// Self reports whether the source answers for itself: its limits key names
// no kinds, and its command is asked by the source's name.
func (l *Limits) Self() bool { return len(l.Kinds) == 0 }

// Argv is the command that asks for kind: Command with the provider's name
// for the kind in place of ProviderMark. ok is false for a kind the source
// does not map.
func (l *Limits) Argv(kind string) (argv []string, ok bool) {
	name, ok := l.Kinds[kind]
	if !ok {
		return nil, false
	}
	return l.ArgvName(name), true
}

// ArgvName is the command that asks for the provider named name: Command
// with the name in place of ProviderMark.
func (l *Limits) ArgvName(name string) []string {
	argv := make([]string, len(l.Command))
	for i, a := range l.Command {
		argv[i] = strings.ReplaceAll(a, ProviderMark, name)
	}
	return argv
}

// KindNames are the harness kinds the source answers for, sorted.
func (l *Limits) KindNames() []string {
	names := make([]string, 0, len(l.Kinds))
	for k := range l.Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// providerName is a provider's own name for a harness kind. It becomes
// an argument of a command, so it cannot be read as a flag.
var providerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// limits reads the key that makes a source one that says what a harness
// kind's subscription still carries, or, with no kinds, what the source
// says of itself. What it refuses is what would fail at every ask, or would
// ask something else than the file reads as: a command that is no list of
// words, one with no place for the provider's name where kinds are named, a
// kind herdr starts no agent of.
func (r *reader) limits(e entry) *Limits {
	l := &Limits{Kinds: map[string]string{}}
	commanded, kinded := false, false
	var commandNode *yaml.Node
	for _, f := range r.entries(e.v, e.path, "command", "kinds") {
		switch f.key {
		case "command":
			commanded, commandNode = true, f.v
			l.Command = r.command(f, strconv.Quote(ProviderMark)+", --json")
			if l.Command != nil && strings.Contains(l.Command[0], ProviderMark) {
				r.errf(f.v, f.path, "%s in the program: it is the same program for every kind, and the provider's name is an argument of it", ProviderMark)
			}
		case "kinds":
			kinded = true
			kinds := r.entries(f.v, f.path)
			for _, k := range kinds {
				name, ok := r.str(k.v, k.path)
				switch {
				case !slices.Contains(herdrKinds, k.key):
					r.errf(k.k, k.path, "%s is no harness kind herdr starts (it starts: %s)", k.key, strings.Join(herdrKinds, ", "))
				case !ok:
				case !providerName.MatchString(name):
					r.errf(k.v, k.path, "%q is no name of a provider: use letters, digits, '.', '-' and '_', starting with a letter or digit", name)
				default:
					l.Kinds[k.key] = name
				}
			}
			if f.v.Kind == yaml.MappingNode && len(f.v.Content) == 0 {
				r.errf(f.v, f.path, "no kind: the source answers for the harness kinds listed here (kimi: kimi)")
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return l
	}
	if !commanded {
		r.errf(e.v, join(e.path, "command"), "missing: the program that says the limits and its arguments, as a list with %s in it", ProviderMark)
	}
	// The mark and the kinds go together: with kinds it is where the
	// provider's name goes, and without both the source answers for
	// itself.
	marked := len(l.Command) > 0 && slices.ContainsFunc(l.Command[1:], func(a string) bool { return strings.Contains(a, ProviderMark) })
	switch {
	case l.Command == nil:
	case kinded && !marked:
		r.errf(commandNode, join(e.path, "command"), "no %s among the arguments: it is where the provider's name for the kind asked about goes", ProviderMark)
	case !kinded && marked:
		r.errf(e.v, join(e.path, "kinds"), "missing: the harness kinds the source answers for, each with the provider's own name for it (kimi: kimi)")
	}
	return l
}
