// Package pack reads a pack, a directory holding one yard's way of working
// (pack.yaml, roles/, scripts/, flows/), and plans what importing it into a yard
// would change. It writes nothing: see docs/packs.md. The directory is read
// as an fs.FS, so it may as well be one yardr embeds (package packs).
package pack

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// File is the pack's one declarative file, at the root of its directory.
const File = "pack.yaml"

// Pack is a pack as written. Everything that has an order in the file
// keeps it: flows, a flow's stages (the first is where a bead starts), a
// stage's outcomes, groups and crew members.
type Pack struct {
	// Dir is where the pack was read from: a directory, absolute, or for a
	// pack in no directory what Load was told it is from.
	Dir  string `json:"dir"`
	Name string `json:"name"`
	// Kind is the kind of depot the pack is written for, one of Kinds, or
	// KindSource for a pack that is imported into the yard itself, and
	// into no depot; "" for a pack that says none and fits either depot.
	Kind   string       `json:"kind,omitempty"`
	Flows  []Flow       `json:"flows"`
	Groups []Group      `json:"groups"`
	Crew   []CrewMember `json:"crew"`
	// Sources are the outside sources whose issues hear the pack's flows, in
	// file order.
	Sources []StageMap `json:"sources,omitempty"`
	// Declared are the sources the pack brings by a declaration file, in
	// file order: services yardr has no Go for.
	Declared []Declared `json:"declared,omitempty"`
	// Setup is the pack's setup notes, its author's words about what a
	// setup takes: a slash path relative to the pack's directory, "" for a
	// pack without any. SetupText is what the file holds.
	Setup     string `json:"setup,omitempty"`
	SetupText string `json:"setup_text,omitempty"`
	// fsys is the pack's directory: every path in the pack is a slash path
	// in it.
	fsys fs.FS
}

// StageMap says which status an issue of an outside source goes to when the
// bead tracking it enters a stage of the pack's flows, and as which of the
// pack's crew the status is set. Map is keyed as a hook's map is
// (store.MapKey: "<bead type>:<stage>") and holds one of hooks.States. The
// pack carries it because which stage means which status is opinion about
// the flow; the import sets it on the yard's hook of the source.
//
// Says is the other direction: what the source's events say of the bead
// they are about, as an outcome of it, which the bead's flow routes
// (store.Say), and Makes what they make: a bead for a thread no open bead
// tracks (store.Make). A source may carry any of the three; one with the
// tables alone has no As.
type StageMap struct {
	Source string            `json:"source"`
	As     string            `json:"as"`
	Map    map[string]string `json:"map"`
	Says   []store.Say       `json:"says,omitempty"`
	Makes  []store.Make      `json:"makes,omitempty"`
}

// Declared is a source the pack brings: the declaration of a service
// outside the yard (internal/source), which the import adds to the yard's
// declared sources as yardr source add would. The file holds no secret and
// names no file of the yard: where the credential is read from stays the
// operator's to say (yardr source set).
type Declared struct {
	Name string `json:"source"`
	// File is the declaration, a slash path relative to the pack's directory,
	// and Text what it holds: the store keeps a declaration as its text.
	File string `json:"file"`
	Text string `json:"declaration"`
	// Declaration is the file as read.
	Declaration *source.Declaration `json:"-"`
}

// DeclarationKeys are the keys a declaration in a pack for a depot may have
// at its root. The list is the pack's own, not the reader's of
// internal/source: a key that reader learns later is not thereby one a pack
// may bring, and is refused until it is added here.
var DeclarationKeys = []string{"name", "about", "base", "credential", "check", "idempotency", "calls", "notify"}

// SourceDeclarationKeys are the keys a declaration in a pack of kind
// source may have at its root: a pack for the yard brings the commands a
// declaration names, which a pack for a depot does not. limits stays
// refused in every pack: what a harness kind's subscription still carries
// is the yard's to ask, not a pack's to say.
var SourceDeclarationKeys = []string{"name", "about", "base", "credential", "check", "idempotency", "calls", "notify", "hook", "thread", "link", "identity"}

type Flow struct {
	Name string `json:"name"`
	// Type is the bead type the flow is for, beside the depot's flow; "" for
	// a flow a depot may follow as its own.
	Type   string  `json:"type,omitempty"`
	Stages []Stage `json:"stages"`
}

type Stage struct {
	Name     string          `json:"name"`
	Group    string          `json:"group,omitempty"`
	Outcomes []store.Outcome `json:"outcomes,omitempty"`
	Manual   []string        `json:"manual,omitempty"`
	End      bool            `json:"end,omitempty"`
	// File is the stage's stage file, a slash path relative to the pack's
	// directory; "" for a stage the pack says nothing more about.
	File string `json:"file,omitempty"`
}

// Group is exactly one of an agent with a role (Agent), a script (Run) or
// people. Agent and Run are slash paths relative to the pack's directory.
type Group struct {
	Name   string `json:"name"`
	Agent  string `json:"agent,omitempty"`
	Run    string `json:"run,omitempty"`
	People bool   `json:"people,omitempty"`
	Limit  int    `json:"limit,omitempty"` // 0: the pack names none
	// Calls are the calls of declared sources the pack asks that the group's
	// sessions may make, in file order. As a crew member's OpenThreads it is
	// a request and no more: the import asks the operator, and only their
	// yes is a grant (store.GrantSource).
	Calls []CallsAsked `json:"calls,omitempty"`
}

// CallsAsked is what a pack asks that a group's sessions or a crew member
// may call of one declared source: store.GrantRead for every read call, or
// a call by its name, which is the one way an update is asked for.
type CallsAsked struct {
	Source string   `json:"source"`
	Calls  []string `json:"calls"`
}

type CrewMember struct {
	Name      string   `json:"name"`
	Role      string   `json:"role,omitempty"`
	Events    []string `json:"events,omitempty"`
	Stages    []string `json:"stages,omitempty"`
	Done      bool     `json:"done,omitempty"`
	Subscribe []Source `json:"subscribe,omitempty"`
	// OpenThreads are the sources the pack asks that its crew member may open
	// threads in, in file order. It is a request and no more: the import
	// asks the operator, and only their yes sets it on the yard's crew member.
	OpenThreads []string `json:"open_threads,omitempty"`
	// Calls are the calls of declared sources the pack asks that the crew
	// member may make: a request, as a group's.
	Calls []CallsAsked `json:"calls,omitempty"`
	// ExposeToPeers asks that the yards this one is linked with may see the
	// crew member and write to it. A request, as OpenThreads is: the import
	// asks the operator, peer by peer, and only their yes exposes it
	// (store.AllowPeer). A pack names no peer and no person.
	ExposeToPeers bool `json:"expose_to_peers,omitempty"`
}

// Source is one outside source a crew member hears, and how much of it.
type Source struct {
	Name  string `json:"source"`
	Scope string `json:"scope"`
}

// Error is one thing wrong with a pack: the file, the line when the file
// was read that far, and the key as a dotted path from the file's root.
type Error struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
	Key  string `json:"key,omitempty"`
	Msg  string `json:"error"`
}

func (e Error) Error() string {
	out := e.File
	if e.Line > 0 {
		out += ":" + strconv.Itoa(e.Line)
	}
	if e.Key != "" {
		out += ": " + e.Key
	}
	return out + ": " + e.Msg
}

// Errors is everything wrong with a pack, in file order: a pack is read to
// its end so one run names every mistake.
type Errors []Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// validName is what a pack may call itself and its flows, groups,
// crew members, stages and outcomes: each becomes, or becomes part of, a name
// in the store or a directory under the yard's home.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// booleans are the plain scalars YAML 1.1 reads as a boolean. The parser
// used here keeps most of them as strings, but another reader of the same
// file would not, so a pack must not depend on which one reads it.
var booleans = []string{"y", "yes", "n", "no", "on", "off", "true", "false"}

func booleanLike(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!bool" || (n.Style == 0 && slices.Contains(booleans, strings.ToLower(n.Value))))
}

// Kinds are the kinds of depot a pack may say it is for: a git depot's
// beads are built on branches and landed on a base, a dir depot's are worked
// in the directory itself and land nowhere.
var Kinds = []string{store.DepotGit, store.DepotDir}

// KindSource is the kind of a pack that is imported into the yard itself,
// and into no depot: an integration, a declaration and the commands it
// names, with no flows, groups or crew.
const KindSource = "source"

// MaxSetup bounds a pack's setup notes: they are printed after every plan
// of the pack, so they stay short enough to read there.
const MaxSetup = 64 << 10

// Scopes are how much of an outside source a crew member may hear.
var Scopes = []string{store.ScopeInvolved, store.ScopeMentions, store.ScopeAll}

// LoadDir reads and checks the pack in the directory dir.
func LoadDir(dir string) (*Pack, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p, err := Load(os.DirFS(abs), dir)
	if p != nil {
		p.Dir = abs
	}
	return p, err
}

// Load reads and checks the pack that fsys is the directory of. dir says
// where that is: it is how an error names a file of the pack, and for a pack
// in no directory (one yardr embeds) what its plan and the record of its
// import give as the source. A pack with anything wrong is not returned: the
// error is an Errors naming each mistake.
func Load(fsys fs.FS, dir string) (*Pack, error) {
	file := joinName(dir, File)
	text, err := fs.ReadFile(fsys, File)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, Errors{{File: file, Msg: "not found: a pack is a directory with a " + File}}
		}
		return nil, Errors{{File: file, Msg: err.Error()}}
	}
	r := &reader{file: file, dir: dir, fsys: fsys}
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, Errors{{File: file, Msg: err.Error()}}
	}
	p := &Pack{Dir: dir, fsys: fsys}
	if len(doc.Content) == 0 {
		r.errs = append(r.errs, Error{File: file, Msg: "empty: a pack has at least a name"})
	} else {
		r.pack(p, doc.Content[0])
	}
	sort.SliceStable(r.errs, func(i, j int) bool { return r.errs[i].Line < r.errs[j].Line })
	// After the pack's own file, the files it names, each in line order.
	r.errs = append(r.errs, r.late...)
	r.errs = append(r.errs, p.placeholderErrors()...)
	if len(r.errs) > 0 {
		return nil, r.errs
	}
	return p, nil
}

// Operator is the placeholder for the yard's operator: a role or stage file
// says {{operator}} where the import puts that person's name. The other
// placeholders are the pack's crew, {{<name>}}, each filled with the
// yard's crew member that takes it. A pack names no person and no crew member of
// a yard, so these are how its prose refers to them.
const Operator = "operator"

// placeholder is anything between double braces on one line. A name the
// import does not fill is an error of the pack, so what is installed never
// carries one.
var placeholder = regexp.MustCompile(`\{\{[^{}\n]*\}\}`)

// Fill puts names where text has their placeholders: {{<key>}} becomes the
// key's value. A placeholder without a key in names is left as it is.
func Fill(text []byte, names map[string]string) []byte {
	return placeholder.ReplaceAllFunc(text, func(m []byte) []byte {
		key := string(m[2 : len(m)-2])
		if v, ok := names[key]; ok {
			return []byte(v)
		}
		return m
	})
}

// Filled reports whether the import fills the placeholders of the pack's
// file rel: it does for a role and a stage file, and copies a script as it
// is, where braces are the shell's or a template's own. A script is what a
// run: group names, and that also makes its copy executable: the file's own
// mode says nothing, since a pack yardr embeds has none.
func (p *Pack) Filled(rel string) bool {
	return !slices.ContainsFunc(p.Groups, func(g Group) bool { return g.Run == rel })
}

// Placeholders are the names a role or stage file of the pack may put in
// double braces: the operator, then the pack's crew.
func (p *Pack) Placeholders() []string {
	out := []string{Operator}
	for _, r := range p.Crew {
		if r.Name != Operator {
			out = append(out, r.Name)
		}
	}
	return out
}

// placeholderErrors names every placeholder in the pack's role and stage
// files that an import would not fill, by file and line.
func (p *Pack) placeholderErrors() Errors {
	files := p.Files()
	for _, f := range p.Flows {
		for _, s := range f.Stages {
			if s.File != "" && !slices.Contains(files, s.File) {
				files = append(files, s.File)
			}
		}
	}
	known := p.Placeholders()
	may := make([]string, len(known))
	for i, name := range known {
		may[i] = "{{" + name + "}}"
	}
	var errs Errors
	for _, rel := range files {
		if !p.Filled(rel) {
			continue
		}
		file := joinName(p.Dir, rel)
		text, err := fs.ReadFile(p.fsys, rel)
		if err != nil {
			errs = append(errs, Error{File: file, Msg: err.Error()})
			continue
		}
		for i, line := range strings.Split(string(text), "\n") {
			for _, m := range placeholder.FindAllString(line, -1) {
				if name := m[2 : len(m)-2]; !slices.Contains(known, name) {
					errs = append(errs, Error{File: file, Line: i + 1, Msg: fmt.Sprintf("placeholder %s: an import fills %s (the operator and the pack's crew) and nothing else", m, strings.Join(may, ", "))})
				}
			}
		}
	}
	return errs
}

type reader struct {
	file string
	dir  string // as given: how an error names a file of the pack
	fsys fs.FS
	errs Errors
	// late are the errors of the declaration files the pack names: they
	// follow those of pack.yaml, whose lines theirs are not.
	late Errors
	// asked are the calls the pack's groups and crew ask for, each where it
	// stands: what a source of the pack declares is known only once the
	// sources are read, which is last.
	asked []askedCall
}

// hasDeclaration reports whether v, a source's value, names a declaration
// file: a mapping with a declaration key.
func hasDeclaration(v *yaml.Node) bool {
	if v.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		if k := v.Content[i]; k.Kind == yaml.ScalarNode && k.Value == "declaration" {
			return true
		}
	}
	return false
}

// askedCall is one word under a calls key: a source and what of it.
type askedCall struct {
	n            *yaml.Node
	key          string
	source, call string
}

func (r *reader) errf(n *yaml.Node, key, format string, args ...any) {
	r.errs = append(r.errs, Error{File: r.file, Line: n.Line, Key: key, Msg: fmt.Sprintf(format, args...)})
}

type entry struct {
	key  string
	path string // the key's dotted path from the root
	k, v *yaml.Node
}

func join(key, name string) string {
	if key == "" {
		return name
	}
	return key + "." + name
}

// entries are a mapping's keys and values in file order. With allowed, a
// key outside it is an error; without, the keys are names the pack chose
// and must be valid ones. Either way a key YAML reads as a boolean, a key
// that is not a string and a key given twice are errors, and such an entry
// is left out.
func (r *reader) entries(n *yaml.Node, key string, allowed ...string) []entry {
	if n.Kind != yaml.MappingNode {
		r.errf(n, key, "want a mapping (key: value), got %s", describe(n))
		return nil
	}
	var out []entry
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		p := join(key, k.Value)
		switch {
		case booleanLike(k):
			r.errf(k, p, "YAML reads the bare key %s as a boolean, not a name: rename it, or quote it (%q)", k.Value, k.Value)
		case k.Kind != yaml.ScalarNode || k.Tag != "!!str":
			r.errf(k, p, "a key must be a string, got %s", describe(k))
		case seen[k.Value]:
			r.errf(k, p, "given twice")
		case len(allowed) > 0 && !slices.Contains(allowed, k.Value):
			r.errf(k, p, "unknown key (known here: %s)", strings.Join(allowed, ", "))
		case len(allowed) == 0 && !validName.MatchString(k.Value):
			r.errf(k, p, "name %q: use letters, digits, '.', '_' or '-'", k.Value)
		default:
			seen[k.Value] = true
			out = append(out, entry{key: k.Value, path: p, k: k, v: v})
		}
	}
	return out
}

func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a list"
	case yaml.AliasNode:
		return "an alias (not read in a pack)"
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return "nothing"
		}
		if n.Tag == "!!str" {
			return strconv.Quote(n.Value)
		}
		return strings.TrimPrefix(n.Tag, "!!") + " " + n.Value
	}
	return "something else"
}

// str reads a string value. A plain word YAML reads as a boolean is
// refused like a boolean key: as a stage or outcome name it would be one
// thing to this reader and another to the next.
func (r *reader) str(n *yaml.Node, key string) (string, bool) {
	if booleanLike(n) {
		r.errf(n, key, "YAML reads the bare word %s as a boolean, not a string: quote it (%q)", n.Value, n.Value)
		return "", false
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || n.Value == "" {
		r.errf(n, key, "want a string, got %s", describe(n))
		return "", false
	}
	return n.Value, true
}

func (r *reader) strs(n *yaml.Node, key string) []string {
	if n.Kind != yaml.SequenceNode {
		r.errf(n, key, "want a list ([a, b]), got %s", describe(n))
		return nil
	}
	var out []string
	for _, c := range n.Content {
		s, ok := r.str(c, key)
		if !ok {
			continue
		}
		if slices.Contains(out, s) {
			r.errf(c, key, "%s is listed twice", s)
			continue
		}
		out = append(out, s)
	}
	return out
}

// boolean reads true or false and nothing else: yes and on are not
// accepted here, for the reason they are refused as keys.
func (r *reader) boolean(n *yaml.Node, key string) (value, ok bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || (n.Value != "true" && n.Value != "false") {
		r.errf(n, key, "want true or false, got %s", describe(n))
		return false, false
	}
	return n.Value == "true", true
}

func (r *reader) pack(p *Pack, root *yaml.Node) {
	var flows, groups, crew, sources *entry
	named := false
	for _, e := range r.entries(root, "", "name", "kind", "setup", "flows", "groups", "crew", "sources") {
		switch e.key {
		case "setup":
			p.Setup, p.SetupText = r.setupFile(e.v, e.path)
		case "name":
			named = true
			if s, ok := r.str(e.v, e.path); ok {
				if !validName.MatchString(s) {
					r.errf(e.v, e.path, "name %q: use letters, digits, '.', '_' or '-'", s)
				}
				p.Name = s
			}
		case "kind":
			if s, ok := r.str(e.v, e.path); ok {
				if !slices.Contains(Kinds, s) && s != KindSource {
					r.errf(e.v, e.path, "kind %q: a pack is for a depot of kind %s, for the yard itself (%s), or says none and fits either depot", s, strings.Join(Kinds, " or "), KindSource)
					break
				}
				p.Kind = s
			}
		case "flows":
			flows = &e
		case "groups":
			groups = &e
		case "crew":
			crew = &e
		case "sources":
			sources = &e
		}
	}
	if !named && root.Kind == yaml.MappingNode {
		r.errf(root, "name", "missing: a pack has a name")
	}
	// Groups first: stages name them. Flows before crew members: hears.stages
	// names their stages.
	if groups != nil {
		for _, e := range r.entries(groups.v, groups.path) {
			p.Groups = append(p.Groups, r.group(e))
		}
	}
	if flows != nil {
		types := map[string]string{}
		for _, e := range r.entries(flows.v, flows.path) {
			f := r.flow(e, p.Groups)
			if f.Type != "" {
				if other, ok := types[f.Type]; ok {
					r.errf(e.k, join(e.path, "type"), "flow %s is for type %s already: a type follows one flow", other, f.Type)
				}
				types[f.Type] = f.Name
			}
			p.Flows = append(p.Flows, f)
		}
	}
	if crew != nil {
		for _, e := range r.entries(crew.v, crew.path) {
			p.Crew = append(p.Crew, r.member(e, p.Flows))
		}
	}
	if p.Kind == KindSource {
		for _, e := range []struct {
			e    *entry
			what string
		}{{flows, "flows"}, {groups, "groups"}, {crew, "crew"}} {
			if e.e != nil {
				r.errf(e.e.k, e.e.path, "a pack of kind source carries sources alone, and no %s", e.what)
			}
		}
		if sources == nil && root.Kind == yaml.MappingNode {
			r.errf(root, "sources", "missing: a pack of kind source declares its sources (sources: <name>: declaration: <file>)")
		} else if sources != nil && sources.v.Kind == yaml.MappingNode && len(sources.v.Content) == 0 {
			r.errf(sources.k, sources.path, "empty: a pack of kind source declares at least one source")
		}
	}
	// Last: a source names a crew member, and its map the flows' stages.
	if sources != nil {
		for _, e := range r.entries(sources.v, sources.path) {
			// A pack for the yard declares every source it names. A pack
			// for a depot declares one it names a file for, and says of
			// any other what its flow's stages mean in it: the source
			// itself is the yard's to have, from an integration pack.
			if p.Kind == KindSource || hasDeclaration(e.v) {
				if d, ok := r.declared(e, p.Kind); ok {
					p.Declared = append(p.Declared, d)
				}
				continue
			}
			if m, ok := r.source(e, p); ok {
				p.Sources = append(p.Sources, m)
			}
		}
	}
	// What is asked of a source the pack declares is held to its file. One
	// the pack does not declare is the yard's, and the plan says whether the
	// yard has it and the call.
	for _, a := range r.asked {
		i := slices.IndexFunc(p.Declared, func(d Declared) bool { return d.Name == a.source })
		if i < 0 || a.call == store.GrantRead {
			continue
		}
		d := p.Declared[i].Declaration
		if _, ok := d.Call(a.call); !ok {
			names := make([]string, len(d.Calls))
			for j, c := range d.Calls {
				names[j] = c.Name
			}
			r.errf(a.n, a.key, "%s declares no call %s (it has: %s): ask for %s, every read call, or a call by its name", p.Declared[i].File, a.call, strings.Join(names, ", "), store.GrantRead)
		}
	}
}

// calls reads what a group or a crew member asks to call: a mapping of a
// declared source to store.GrantRead and the names of calls.
func (r *reader) calls(e entry) []CallsAsked {
	var out []CallsAsked
	for _, s := range r.entries(e.v, e.path) {
		if slices.Contains(source.BuiltIn, s.key) {
			r.errf(s.k, s.path, "%s is a source yardr has built in (%s): calls are asked of a source a file declares (sources.<name>: declaration)", s.key, strings.Join(source.BuiltIn, ", "))
			continue
		}
		words := r.strs(s.v, s.path)
		if s.v.Kind == yaml.SequenceNode && len(s.v.Content) == 0 {
			r.errf(s.v, s.path, "empty: name %s or a call ([%s]), or leave the source out", store.GrantRead, store.GrantRead)
		}
		if len(words) == 0 {
			continue
		}
		for _, w := range words {
			r.asked = append(r.asked, askedCall{n: s.v, key: s.path, source: s.key, call: w})
		}
		out = append(out, CallsAsked{Source: s.key, Calls: words})
	}
	return out
}

// declared reads a source the pack brings by its declaration file. The file
// is read as yardr source add reads it, and held to one thing more: only
// the keys of DeclarationKeys, or of SourceDeclarationKeys in a pack of
// kind source, which brings the commands a declaration names.
func (r *reader) declared(e entry, kind string) (d Declared, ok bool) {
	d = Declared{Name: e.key}
	if slices.Contains(source.BuiltIn, e.key) {
		r.errf(e.k, e.path, "%s is a source yardr has built in (%s): a declaration is for a source of another name", e.key, strings.Join(source.BuiltIn, ", "))
		return d, false
	}
	allowed := []string{"declaration", "as", "hears"}
	if kind == KindSource {
		allowed = []string{"declaration", "as", "hears", "says", "makes"}
	}
	var file *entry
	var other []entry
	for _, k := range r.entries(e.v, e.path, allowed...) {
		if k.key == "declaration" {
			file = &k
		} else {
			other = append(other, k)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return d, false
	}
	if file == nil {
		if kind == KindSource {
			r.errf(e.k, e.path, "a source of a pack of kind source is declared by a file of the pack (declaration: <file>)")
		} else {
			r.errf(e.k, e.path, "this yardr has no adapter for source %s (it has %s): a source of another name is declared by a file of the pack (declaration: sources/%s.yaml)", e.key, strings.Join(hooks.Sources(), ", "), e.key)
		}
		return d, false
	}
	for _, k := range other {
		if kind == KindSource {
			r.errf(k.k, k.path, "a source of a pack of kind source has declaration: <file> and nothing else: hears, says and makes are the flow pack's to say of it")
		} else {
			r.errf(k.k, k.path, "a declared source has declaration: <file> and nothing else: a stage map is for a source yardr can set a status in (%s)", strings.Join(hooks.Forwardable(), ", "))
		}
	}
	if d.File = r.rel(file.v, file.path, "declaration"); d.File == "" {
		return d, false
	}
	full := joinName(r.dir, d.File)
	text, err := fs.ReadFile(r.fsys, d.File)
	if err != nil {
		r.errf(file.v, file.path, "declaration %s: %v", d.File, err)
		return d, false
	}
	// The pack's own refusal first, and the reader's of the same key not
	// after it: one mistake is one line.
	keys := DeclarationKeys
	refusal := fmt.Sprintf("a declaration in a pack has %s and no other key: hook, thread, link and identity are for a pack of kind source, and what else a source may have is set in the yard, by its operator", strings.Join(keys, ", "))
	if kind == KindSource {
		keys = SourceDeclarationKeys
		refusal = fmt.Sprintf("a declaration in a pack of kind source has %s and no other key: what else a source may have is set in the yard, by its operator", strings.Join(keys, ", "))
	}
	refused := map[string]bool{}
	var doc yaml.Node
	if yaml.Unmarshal(text, &doc) == nil && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
		root := doc.Content[0]
		for i := 0; i+1 < len(root.Content); i += 2 {
			if k := root.Content[i]; k.Kind == yaml.ScalarNode && !slices.Contains(keys, k.Value) {
				refused[k.Value] = true
				r.late = append(r.late, Error{File: full, Line: k.Line, Key: k.Value, Msg: refusal})
			}
		}
	}
	decl, err := source.Parse(full, text)
	var errs source.Errors
	switch {
	case errors.As(err, &errs):
		for _, e := range errs {
			// A key under a refused one is the same mistake.
			if top, _, _ := strings.Cut(e.Key, "."); !refused[top] {
				r.late = append(r.late, Error{File: e.File, Line: e.Line, Key: e.Key, Msg: e.Msg})
			}
		}
		return d, false
	case err != nil:
		r.late = append(r.late, Error{File: full, Msg: err.Error()})
		return d, false
	case len(refused) > 0:
		return d, false
	case decl.Name != e.key:
		r.errf(file.v, file.path, "%s declares the source %s, and the pack names it %s: one name, since it is the yard's name for the source", d.File, decl.Name, e.key)
		return d, false
	}
	// The commands' files are the pack's to carry, beside the declaration.
	if kind == KindSource && !r.commandFiles(file.v, file.path, d.File, decl) {
		return d, false
	}
	d.Text, d.Declaration = string(text), decl
	return d, true
}

// commandFiles checks the files the declaration's commands name by a
// relative program: each is a regular file of the pack, beside the
// declaration's file, since the import copies it into the yard. An absolute
// program is the operator's own, and is checked when the plan is made, on
// the machine the yard runs on.
func (r *reader) commandFiles(n *yaml.Node, key, rel string, decl *source.Declaration) bool {
	ok := true
	dir := path.Dir(rel)
	seen := map[string]bool{}
	for _, c := range decl.Commands() {
		prog := c.Argv[0]
		if !c.Relative() || seen[prog] {
			continue
		}
		seen[prog] = true
		clean := path.Join(dir, prog)
		switch {
		case clean == ".." || strings.HasPrefix(clean, "../"):
			r.errf(n, key, "declaration %s names the command file %s, which leaves the pack's directory", rel, prog)
			ok = false
		default:
			// Lstat, as for a role: a link could name a file outside
			// the pack, which a clone elsewhere would not have.
			info, err := fs.Lstat(r.fsys, clean)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				r.errf(n, key, "declaration %s names the command file %s, which is not in the pack (looked for %s)", rel, prog, joinName(r.dir, clean))
				ok = false
			case err != nil:
				r.errf(n, key, "declaration %s names the command file %s: %v", rel, prog, err)
				ok = false
			case !info.Mode().IsRegular():
				r.errf(n, key, "declaration %s names the command file %s, which is not a regular file", rel, prog)
				ok = false
			}
		}
	}
	return ok
}

// source reads what the issues of one outside source hear, and what its
// events say and make, for any name: the source itself is the yard's to
// have, from an integration pack, and the import skips the lines while it
// has none. What only the source can judge, a status it has and the keys
// its events carry, is asked when the plan is made; what the pack judges,
// the crew member, the flows' stages and the outcomes, is asked here.
func (r *reader) source(e entry, p *Pack) (m StageMap, ok bool) {
	m = StageMap{Source: e.key, Map: map[string]string{}}
	_, builtin := hooks.Adapters[e.key]
	f, can := hooks.ForwarderFor(e.key, nil, "")
	keys := r.entries(e.v, e.path, "as", "hears", "says", "makes")
	if e.v.Kind == yaml.MappingNode && len(e.v.Content) == 0 {
		r.errf(e.k, e.path, "empty: say what the source hears (hears:), what its events say (says:) or make (makes:)")
		return m, false
	}
	var as, hears bool
	table := func(key string) bool { return key == "says" || key == "makes" }
	says := slices.ContainsFunc(keys, func(k entry) bool { return table(k.key) })
	// A status is all there is to give such a source, besides its tables.
	if builtin && !can && !says {
		r.errf(e.k, e.path, "yardr cannot set a status in %s (it can in %s)", e.key, strings.Join(hooks.Forwardable(), ", "))
		return m, false
	}
	for _, k := range keys {
		if builtin && !can && !table(k.key) {
			r.errf(k.k, k.path, "yardr cannot set a status in %s (it can in %s)", e.key, strings.Join(hooks.Forwardable(), ", "))
			continue
		}
		switch k.key {
		case "says":
			m.Says = r.says(k, e.key, p, builtin)
		case "makes":
			m.Makes = r.makes(k, e.key, p, builtin)
		case "as":
			as = true
			name, ok := r.str(k.v, k.path)
			if !ok {
				continue
			}
			if !slices.ContainsFunc(p.Crew, func(have CrewMember) bool { return have.Name == name }) {
				r.errf(k.v, k.path, "no crew member %s is declared under crew: the status is set as one of the pack's crew", name)
				continue
			}
			m.As = name
		case "hears":
			hears = true
			types := r.entries(k.v, k.path)
			if k.v.Kind == yaml.MappingNode && len(k.v.Content) == 0 {
				r.errf(k.v, k.path, "empty: name a bead type and its stages (task: { review: review }), or leave the source out")
			}
			for _, t := range types {
				flows := flowsFor(p.Flows, t.key)
				if len(flows) == 0 {
					r.errf(t.k, t.path, "no flow of this pack is for beads of type %s: it has no flow with type: %s and none a depot follows", t.key, t.key)
					continue
				}
				for _, s := range r.entries(t.v, t.path) {
					if !hasStage(flows, s.key) {
						r.errf(s.k, s.path, "%s stage %s (stages: %s)", lacking(flows), s.key, strings.Join(stageNames(flows), ", "))
						continue
					}
					word, ok := r.str(s.v, s.path)
					if !ok {
						continue
					}
					if !hooks.Word(word) {
						r.errf(s.v, s.path, "%q is no state: one of %s", word, strings.Join(hooks.States(), ", "))
						continue
					}
					// A name no adapter has is held to the yard's source
					// when the plan is made.
					if can {
						if err := f.Places(word); err != nil {
							r.errf(s.v, s.path, "%v", err)
							continue
						}
					}
					m.Map[store.MapKey(t.key, s.key)] = word
				}
			}
		}
	}
	// A source with its tables alone sets no status: it needs neither.
	if e.v.Kind == yaml.MappingNode && (!builtin || can) && (as || hears || !says) {
		if !as {
			r.errf(e.k, join(e.path, "as"), "missing: the crew member of the pack the status is set as")
		}
		if !hears {
			r.errf(e.k, join(e.path, "hears"), "missing: which stage of which bead type means which status")
		}
	}
	return m, true
}

// says reads the table of what a source's events say: a list of lines,
// each the filter an event must match (where, as a subscription's is typed:
// key=value words), the outcome it reports for the bead it is about, and
// optionally the words its note begins with. A line of a source yardr has
// built in is refused as the yard's hook would refuse it (store.Hook's
// Says); any other name is held to the yard's source when the plan is made.
// A line is also refused when no flow of the pack could take its outcome in
// the stage it names: the bead an event is about is the one that tracks its
// thread, of any type, else the train of its branch, so every flow is asked,
// and the refusal says of each why not.
func (r *reader) says(k entry, source string, p *Pack, builtin bool) []store.Say {
	if k.v.Kind != yaml.SequenceNode {
		r.errf(k.v, k.path, "want a list of lines ({ where: \"event=pull_request action=closed merged=true\", outcome: merged }), got %s", describe(k.v))
		return nil
	}
	if len(k.v.Content) == 0 {
		r.errf(k.v, k.path, "empty: give a line, or leave says out")
	}
	var out []store.Say
	for i, n := range k.v.Content {
		path := fmt.Sprintf("%s[%d]", k.path, i)
		var say store.Say
		var where, outcome, bad bool
		for _, l := range r.entries(n, path, "where", "outcome", "note") {
			text, ok := r.str(l.v, l.path)
			if !ok {
				bad = true
				continue
			}
			switch l.key {
			case "where":
				where = true
				var err error
				if say.Where, err = store.ParseWhere(text); err != nil {
					r.errf(l.v, l.path, "%v", err)
					bad = true
				}
			case "outcome":
				outcome = true
				say.Outcome = text
			case "note":
				say.Note = text
			}
		}
		if n.Kind != yaml.MappingNode {
			continue
		}
		if !where {
			r.errf(n, join(path, "where"), "missing: the events the line is for (event=pull_request action=closed)")
		}
		if !outcome {
			r.errf(n, join(path, "outcome"), "missing: the outcome such an event reports for the bead")
		}
		if bad || !where || !outcome {
			continue
		}
		// What the store would refuse on the hook, said here with the line.
		// A name no adapter has is held to the yard's source when the
		// plan is made, whose keys no pack knows.
		if builtin {
			if err := (store.Hook{Name: source, Source: source, Says: []store.Say{say}}).CheckSays(); err != nil {
				r.errf(n, path, "%v", err)
				continue
			}
		}
		if len(p.Flows) == 0 {
			r.errf(n, path, "this pack has no flow, so no bead of it could take the outcome")
			continue
		}
		if err := unrouted(say, p.Flows); err != nil {
			r.errf(n, join(path, "outcome"), "no bead of this pack could take it: %v, so the flow would refuse it every time", err)
			continue
		}
		out = append(out, say)
	}
	return out
}

// makes reads the table of what a source's events make: a list of lines,
// each the filter an event must match (where, as in says, without stage)
// and the bead such an event makes for a thread no open bead tracks (bead:
// its type, its stage and, optionally, the depot of the yard it is made
// in). A line of a source yardr has built in is refused as the yard's hook
// would refuse it (store.Hook's Makes); any other name is held to the
// yard's source when the plan is made. A line is also refused when the flow
// of the pack that beads of the type follow has no such stage a bead can be
// made in.
func (r *reader) makes(k entry, source string, p *Pack, builtin bool) []store.Make {
	if k.v.Kind != yaml.SequenceNode {
		r.errf(k.v, k.path, "want a list of lines ({ where: \"event=issues action=opened\", bead: { type: issue, stage: triage } }), got %s", describe(k.v))
		return nil
	}
	if len(k.v.Content) == 0 {
		r.errf(k.v, k.path, "empty: give a line, or leave makes out")
	}
	var out []store.Make
	for i, n := range k.v.Content {
		path := fmt.Sprintf("%s[%d]", k.path, i)
		var m store.Make
		var where, bead, bad bool
		for _, l := range r.entries(n, path, "where", "bead") {
			switch l.key {
			case "where":
				where = true
				text, ok := r.str(l.v, l.path)
				if !ok {
					bad = true
					continue
				}
				var err error
				if m.Where, err = store.ParseWhere(text); err != nil {
					r.errf(l.v, l.path, "%v", err)
					bad = true
				}
			case "bead":
				bead = true
				for _, f := range r.entries(l.v, l.path, "type", "stage", "depot") {
					text, ok := r.str(f.v, f.path)
					if !ok {
						bad = true
						continue
					}
					switch f.key {
					case "type":
						m.Type = text
					case "stage":
						m.Stage = text
					case "depot":
						m.Depot = text
					}
				}
				if l.v.Kind != yaml.MappingNode {
					bad = true
					continue
				}
				if m.Type == "" {
					r.errf(l.v, join(l.path, "type"), "missing: the type of the bead such an event makes")
					bad = true
				}
				if m.Stage == "" {
					r.errf(l.v, join(l.path, "stage"), "missing: the stage the bead is made in")
					bad = true
				}
			}
		}
		if n.Kind != yaml.MappingNode {
			continue
		}
		if !where {
			r.errf(n, join(path, "where"), "missing: the events the line is for (event=issues action=opened)")
		}
		if !bead {
			r.errf(n, join(path, "bead"), "missing: the bead such an event makes ({ type: issue, stage: triage })")
		}
		if bad || !where || !bead {
			continue
		}
		// What the store would refuse on the hook, said here with the line.
		// A name no adapter has is held to the yard's source when the
		// plan is made, whose keys no pack knows.
		if builtin {
			if err := (store.Hook{Name: source, Source: source, Makes: []store.Make{m}}).CheckMakes(); err != nil {
				r.errf(n, path, "%v", err)
				continue
			}
		}
		flows := flowsFor(p.Flows, m.Type)
		if len(flows) == 0 {
			r.errf(n, join(path, "bead.type"), "no flow of this pack is for beads of type %s: it has no flow with type: %s and none a depot follows", m.Type, m.Type)
			continue
		}
		var why []string
		for _, f := range flows {
			err := m.Unstaged(slices.Sorted(maps.Keys(stageOutcomes(f))))
			if err == nil {
				why = nil
				break
			}
			why = append(why, fmt.Sprintf("flow %s: %v", f.Name, err))
		}
		if why != nil {
			r.errf(n, join(path, "bead.stage"), "no bead of type %s could be made there: %s", m.Type, strings.Join(why, "; "))
			continue
		}
		out = append(out, m)
	}
	return out
}

// unrouted says why no bead could take the line's outcome under any of
// flows, nil when one could: some flow has the outcome in the stage the
// line names (each of them, for a line that names several, which may be
// stages of different flows), or in any stage for a line that names none.
// The error names every flow and what it lacks for the stage.
func unrouted(say store.Say, flows []Flow) error {
	// The line, once for each stage it names; as it is when it names none.
	lines := []store.Say{say}
	if named, ok := say.Where[store.SayStage]; ok {
		lines = nil
		for _, stage := range store.WhereValues(named) {
			one := store.Say{Where: maps.Clone(say.Where), Outcome: say.Outcome}
			one.Where[store.SayStage] = stage
			lines = append(lines, one)
		}
	}
	for _, line := range lines {
		var why []string
		for _, f := range flows {
			err := line.Unrouted(stageOutcomes(f))
			if err == nil {
				why = nil
				break
			}
			why = append(why, fmt.Sprintf("flow %s: %v", f.Name, err))
		}
		if why != nil {
			return errors.New(strings.Join(why, "; "))
		}
	}
	return nil
}

// stageOutcomes are the outcomes a bead under f can report, by the stage it
// is in.
func stageOutcomes(f Flow) map[string][]string {
	stages := map[string][]string{}
	for _, s := range f.Stages {
		if s.End {
			continue
		}
		stages[s.Name] = nil
		for _, o := range s.Outcomes {
			stages[s.Name] = append(stages[s.Name], o.Name)
		}
		// A stage with one way out and no outcome named offers done.
		if len(s.Outcomes) == 0 && len(s.Manual) == 1 {
			stages[s.Name] = append(stages[s.Name], store.DefaultOutcome)
		}
	}
	return stages
}

// flowsFor are the pack's flows a bead of type typ may follow: the flow for
// that type, else every flow a depot could follow as its own, since a depot's
// flow governs the types that have none.
func flowsFor(flows []Flow, typ string) []Flow {
	var own []Flow
	for _, f := range flows {
		if f.Type == typ {
			return []Flow{f}
		}
		if f.Type == "" {
			own = append(own, f)
		}
	}
	return own
}

// lacking begins the sentence that none of flows has a stage.
func lacking(flows []Flow) string {
	if len(flows) == 1 {
		return "flow " + flows[0].Name + " has no"
	}
	names := make([]string, len(flows))
	for i, f := range flows {
		names[i] = f.Name
	}
	return "none of the flows a depot could follow (" + strings.Join(names, ", ") + ") has a"
}

func stageNames(flows []Flow) []string {
	var out []string
	for _, f := range flows {
		for _, s := range f.Stages {
			if !slices.Contains(out, s.Name) {
				out = append(out, s.Name)
			}
		}
	}
	return out
}

func (r *reader) group(e entry) Group {
	g := Group{Name: e.key}
	var kinds []string
	var calls *entry
	for _, f := range r.entries(e.v, e.path, "agent", "run", "people", "limit", "calls") {
		switch f.key {
		case "calls":
			calls = &f
			g.Calls = r.calls(f)
		case "agent":
			kinds = append(kinds, f.key)
			g.Agent = r.rel(f.v, f.path, "role")
		case "run":
			kinds = append(kinds, f.key)
			g.Run = r.rel(f.v, f.path, "script")
		case "people":
			// A value that is no boolean has its own error; it still counts
			// as the kind, so the group is not also said to have none.
			var ok bool
			if g.People, ok = r.boolean(f.v, f.path); g.People || !ok {
				kinds = append(kinds, f.key)
			}
		case "limit":
			n, err := strconv.Atoi(f.v.Value)
			if f.v.Kind != yaml.ScalarNode || f.v.Tag != "!!int" || err != nil || n < 1 {
				r.errf(f.v, f.path, "want a number of beads, 1 or more, got %s", describe(f.v))
				continue
			}
			g.Limit = n
		}
	}
	if e.v.Kind == yaml.MappingNode && len(kinds) != 1 {
		got := "none"
		if len(kinds) > 0 {
			got = strings.Join(kinds, " and ")
		}
		r.errf(e.k, e.path, "a group is exactly one of agent: <role file>, run: <script>, people: true; this one has %s", got)
	}
	// A person's call is told by who they are, never by the group whose
	// stage they work in (store.sourceCallerIn): a grant to a people group
	// would let nobody call.
	if calls != nil && g.People {
		r.errf(calls.k, calls.path, "a people group makes no call: a person calls as who they are. calls is for a group with agent: or run:")
		g.Calls = nil
	}
	return g
}

// rel reads a role or script path and checks the file: always relative
// to the pack's directory and inside it, since a pack must work wherever
// it is cloned, and a regular file there. The path is returned with
// slashes, cleaned.
func (r *reader) rel(n *yaml.Node, key, what string) string {
	s, ok := r.str(n, key)
	if !ok {
		return ""
	}
	clean := path.Clean(filepath.ToSlash(s))
	switch {
	case path.IsAbs(clean) || filepath.IsAbs(s) || strings.HasPrefix(s, "~"):
		r.errf(n, key, "%s %s: a pack's paths are relative to its directory, not absolute", what, s)
		return ""
	case clean == ".." || strings.HasPrefix(clean, "../"):
		r.errf(n, key, "%s %s leaves the pack's directory", what, s)
		return ""
	}
	full := joinName(r.dir, clean)
	// Lstat: a link could name a file outside the pack, which a clone
	// elsewhere would not have.
	info, err := fs.Lstat(r.fsys, clean)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.errf(n, key, "%s %s is missing (looked for %s)", what, s, full)
	case err != nil:
		r.errf(n, key, "%s %s: %v", what, s, err)
	case !info.Mode().IsRegular():
		r.errf(n, key, "%s %s is not a regular file", what, s)
	default:
		return clean
	}
	return ""
}

// setupFile reads the pack's setup notes as rel does, and refuses a file
// with nothing in it, which the pack would name and nobody would read, and
// one over MaxSetup, which is printed after every plan of the pack.
func (r *reader) setupFile(n *yaml.Node, key string) (file, text string) {
	clean := r.rel(n, key, "setup notes")
	if clean == "" {
		return "", ""
	}
	body, err := fs.ReadFile(r.fsys, clean)
	switch {
	case err != nil:
		r.errf(n, key, "setup notes %s: %v", clean, err)
	case strings.TrimSpace(string(body)) == "":
		r.errf(n, key, "setup notes %s are empty: leave setup out for a pack without any", clean)
	case len(body) > MaxSetup:
		r.errf(n, key, "setup notes %s hold %d bytes: at most %d", clean, len(body), MaxSetup)
	default:
		return clean, string(body)
	}
	return "", ""
}

// stageFile reads a stage file's path as rel does, and refuses a file with
// nothing in it: the yard reads such a file as no stage file at all, so the
// pack would name one and the stage would have none.
func (r *reader) stageFile(n *yaml.Node, key string) string {
	clean := r.rel(n, key, "stage file")
	if clean == "" {
		return ""
	}
	text, err := fs.ReadFile(r.fsys, clean)
	switch {
	case err != nil:
		r.errf(n, key, "stage file %s: %v", clean, err)
	case strings.TrimSpace(string(text)) == "":
		r.errf(n, key, "stage file %s is empty: leave the key out for a stage without one", clean)
	default:
		return clean
	}
	return ""
}

func (r *reader) flow(e entry, groups []Group) Flow {
	f := Flow{Name: e.key}
	var stages *entry
	for _, k := range r.entries(e.v, e.path, "type", "stages") {
		switch k.key {
		case "type":
			if s, ok := r.str(k.v, k.path); ok {
				f.Type = s
			}
		case "stages":
			stages = &k
		}
	}
	if stages == nil {
		if e.v.Kind == yaml.MappingNode {
			r.errf(e.k, join(e.path, "stages"), "missing: a flow lists its stages")
		}
		return f
	}
	es := r.entries(stages.v, stages.path)
	names := make([]string, len(es))
	for i, s := range es {
		names[i] = s.key
	}
	for _, s := range es {
		f.Stages = append(f.Stages, r.stage(s, names, groups))
	}
	if stages.v.Kind == yaml.MappingNode && !slices.ContainsFunc(f.Stages, func(s Stage) bool { return s.End }) {
		r.errf(stages.k, stages.path, "flow %s has no end: mark the stage that closes a bead with end: true", f.Name)
	}
	return f
}

func (r *reader) stage(e entry, stages []string, groups []Group) Stage {
	s := Stage{Name: e.key}
	target := func(n *yaml.Node, key, to string) bool {
		if !slices.Contains(stages, to) {
			r.errf(n, key, "no stage %s in this flow (stages: %s)", to, strings.Join(stages, ", "))
			return false
		}
		return true
	}
	var exits []string
	badEnd := false
	for _, k := range r.entries(e.v, e.path, "group", "file", "outcomes", "manual", "end") {
		switch k.key {
		case "file":
			// Nobody works a bead in a stage that closes it, so nobody would
			// be shown the file: it is refused with the ways out.
			exits = append(exits, k.key)
			s.File = r.stageFile(k.v, k.path)
		case "group":
			exits = append(exits, k.key)
			g, ok := r.str(k.v, k.path)
			if !ok {
				continue
			}
			if !slices.ContainsFunc(groups, func(have Group) bool { return have.Name == g }) {
				r.errf(k.v, k.path, "no group %s is declared under groups", g)
				continue
			}
			s.Group = g
		case "outcomes":
			exits = append(exits, k.key)
			for _, o := range r.entries(k.v, k.path) {
				if to, ok := r.str(o.v, o.path); ok && target(o.v, o.path, to) {
					s.Outcomes = append(s.Outcomes, store.Outcome{Name: o.key, To: to})
				}
			}
		case "manual":
			exits = append(exits, k.key)
			for _, to := range r.strs(k.v, k.path) {
				if target(k.v, k.path, to) {
					s.Manual = append(s.Manual, to)
				}
			}
		case "end":
			// As for people: a bad value is one error, not also a flow
			// without an end.
			var ok bool
			if s.End, ok = r.boolean(k.v, k.path); !ok {
				s.End, badEnd = true, true
			}
		}
	}
	if s.End && !badEnd && len(exits) > 0 {
		r.errf(e.k, e.path, "a stage with end: true closes the bead on entry, so it has no %s", strings.Join(exits, ", "))
	}
	return s
}

func (r *reader) member(e entry, flows []Flow) CrewMember {
	res := CrewMember{Name: e.key}
	for _, k := range r.entries(e.v, e.path, "role", "hears", "subscribe", "open_threads", "expose_to_peers", "calls") {
		switch k.key {
		case "expose_to_peers":
			res.ExposeToPeers, _ = r.boolean(k.v, k.path)
		case "calls":
			res.Calls = r.calls(k)
		case "role":
			res.Role = r.rel(k.v, k.path, "role")
		case "hears":
			for _, h := range r.entries(k.v, k.path, "events", "stages", "done") {
				switch h.key {
				case "events":
					res.Events = append(res.Events, r.strs(h.v, h.path)...)
				case "stages":
					for _, s := range r.strs(h.v, h.path) {
						if !hasStage(flows, s) {
							r.errf(h.v, h.path, "no flow of this pack has a stage %s", s)
							continue
						}
						res.Stages = append(res.Stages, s)
					}
				case "done":
					res.Done, _ = r.boolean(h.v, h.path)
				}
			}
		case "subscribe":
			for _, s := range r.entries(k.v, k.path) {
				scope, ok := r.str(s.v, s.path)
				if !ok {
					continue
				}
				if !slices.Contains(Scopes, scope) {
					r.errf(s.v, s.path, "scope %s: one of %s", scope, strings.Join(Scopes, ", "))
					continue
				}
				// Any name: the source is the yard's to have, and the
				// import skips what it has no source for.
				res.Subscribe = append(res.Subscribe, Source{Name: s.key, Scope: scope})
			}
		case "open_threads":
			for _, s := range r.strs(k.v, k.path) {
				// A source yardr has built in but cannot open threads
				// in is refused here; any other name is the yard's to
				// have, and the import asks once it has it.
				if _, builtin := hooks.Adapters[s]; builtin {
					if _, ok := hooks.OpenerFor(s, nil, ""); !ok {
						r.errf(k.v, k.path, "yardr cannot open threads in %s (it can in %s)", s, strings.Join(hooks.Openable(), ", "))
						continue
					}
				}
				res.OpenThreads = append(res.OpenThreads, s)
			}
		}
	}
	return res
}

func hasStage(flows []Flow, stage string) bool {
	return slices.ContainsFunc(flows, func(f Flow) bool {
		return slices.ContainsFunc(f.Stages, func(s Stage) bool { return s.Name == stage })
	})
}

// Files are the role and script files the pack's groups and crew members
// name, as slash paths relative to its directory, sorted, each once. Stage
// files are not among them: they belong to a flow and go where the yard
// reads that flow's (Stage.File).
func (p *Pack) Files() []string {
	var out []string
	for _, g := range p.Groups {
		out = append(out, g.Agent, g.Run)
	}
	for _, r := range p.Crew {
		out = append(out, r.Role)
	}
	slices.Sort(out)
	out = slices.Compact(out)
	return slices.DeleteFunc(out, func(s string) bool { return s == "" })
}

// StoreFlow is the flow as the store keeps it, under name: the stages in
// file order, each stage's outcomes first and its manual moves after, and
// the end stages terminal. It is in the form the store normalizes a flow
// to, so it compares equal to the same flow read back.
func (f Flow) StoreFlow(name string) store.Flow {
	out := store.Flow{Name: name, Transitions: map[string][]string{}}
	for _, s := range f.Stages {
		out.Stages = append(out.Stages, s.Name)
		if s.End {
			out.Terminal = append(out.Terminal, s.Name)
			continue
		}
		var next []string
		for _, o := range s.Outcomes {
			next = append(next, o.To)
		}
		next = append(next, s.Manual...)
		seen := map[string]bool{}
		for _, to := range next {
			if !seen[to] {
				seen[to] = true
				out.Transitions[s.Name] = append(out.Transitions[s.Name], to)
			}
		}
		if len(s.Outcomes) > 0 {
			if out.Outcomes == nil {
				out.Outcomes = map[string][]store.Outcome{}
			}
			out.Outcomes[s.Name] = slices.Clone(s.Outcomes)
		}
	}
	return out
}
