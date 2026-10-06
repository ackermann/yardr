// Package pack reads a pack, a directory holding one yard's way of working
// (pack.yaml, roles/, scripts/, flows/), and plans what importing it into a yard
// would change. It writes nothing: see docs/packs.md. The directory is read
// as an fs.FS, so it may as well be one yardr embeds (package packs).
package pack

import (
	"errors"
	"fmt"
	"io/fs"
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
	Dir    string       `json:"dir"`
	Name   string       `json:"name"`
	Flows  []Flow       `json:"flows"`
	Groups []Group      `json:"groups"`
	Crew   []CrewMember `json:"crew"`
	// Sources are the outside sources whose issues hear the pack's flows, in
	// file order.
	Sources []StageMap `json:"sources,omitempty"`
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
type StageMap struct {
	Source string            `json:"source"`
	As     string            `json:"as"`
	Map    map[string]string `json:"map"`
}

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
	file := filepath.Join(dir, File)
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

// oldNames are the names an earlier release gave what a pack names, each
// with the name it has now: the starter pack's crew member was mayor until it
// became yardmaster. For this release a yard's own files and commands may
// still say the old name where the pack has only the new one.
var oldNames = map[string]string{"mayor": "yardmaster"}

// Renamed is the pack's crew member that old named in an earlier release: a
// pack that still has a crew member called old has renamed nothing.
func (p *Pack) Renamed(old string) (string, bool) {
	name, ok := oldNames[old]
	has := func(n string) bool {
		return slices.ContainsFunc(p.Crew, func(r CrewMember) bool { return r.Name == n })
	}
	if !ok || has(old) || !has(name) {
		return "", false
	}
	return name, true
}

// oldGroups are the names an earlier release gave a pack's groups, each with
// the name it has now: the starter pack's group that lands approved beads
// was refinery until it became assembly.
var oldGroups = map[string]string{"refinery": "assembly"}

// WasGroup is the name the pack's group name had in an earlier release. A
// pack that still has a group of the old name has renamed nothing.
func (p *Pack) WasGroup(name string) (string, bool) {
	for old, now := range oldGroups {
		if now == name && !slices.ContainsFunc(p.Groups, func(g Group) bool { return g.Name == old }) {
			return old, true
		}
	}
	return "", false
}

// placeholder is anything between double braces on one line. A name the
// import does not fill is an error of the pack, so what is installed never
// carries one.
var placeholder = regexp.MustCompile(`\{\{[^{}\n]*\}\}`)

// Fill puts names where text has their placeholders: {{<key>}} becomes the
// key's value, and a key's earlier name (oldNames) is read as the key. A
// placeholder without a key in names is left as it is.
func Fill(text []byte, names map[string]string) []byte {
	return placeholder.ReplaceAllFunc(text, func(m []byte) []byte {
		key := string(m[2 : len(m)-2])
		if v, ok := names[key]; ok {
			return []byte(v)
		}
		if v, ok := names[oldNames[key]]; ok {
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
		file := filepath.Join(p.Dir, filepath.FromSlash(rel))
		text, err := fs.ReadFile(p.fsys, rel)
		if err != nil {
			errs = append(errs, Error{File: file, Msg: err.Error()})
			continue
		}
		for i, line := range strings.Split(string(text), "\n") {
			for _, m := range placeholder.FindAllString(line, -1) {
				if name := m[2 : len(m)-2]; !slices.Contains(known, name) && !slices.Contains(known, oldNames[name]) {
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
	// residents is what crew was until this release: a pack that still says
	// it is read as it stands, and one that says both means crew.
	for _, e := range r.entries(root, "", "name", "flows", "groups", "crew", "residents", "sources") {
		switch e.key {
		case "name":
			named = true
			if s, ok := r.str(e.v, e.path); ok {
				if !validName.MatchString(s) {
					r.errf(e.v, e.path, "name %q: use letters, digits, '.', '_' or '-'", s)
				}
				p.Name = s
			}
		case "flows":
			flows = &e
		case "groups":
			groups = &e
		case "crew":
			crew = &e
		case "residents":
			if crew == nil {
				crew = &e
			}
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
	// Last: a source names a crew member, and its map the flows' stages.
	if sources != nil {
		for _, e := range r.entries(sources.v, sources.path) {
			if m, ok := r.source(e, p); ok {
				p.Sources = append(p.Sources, m)
			}
		}
	}
}

// source reads what the issues of one outside source hear. ok is false for
// a source yardr could not set a status in, which is not read further.
func (r *reader) source(e entry, p *Pack) (m StageMap, ok bool) {
	m = StageMap{Source: e.key, Map: map[string]string{}}
	if _, has := hooks.Adapters[e.key]; !has {
		r.errf(e.k, e.path, "this yardr has no adapter for source %s (it has %s)", e.key, strings.Join(hooks.Sources(), ", "))
		return m, false
	}
	f, can := hooks.ForwarderFor(e.key)
	if !can {
		r.errf(e.k, e.path, "yardr cannot set a status in %s (it can in %s)", e.key, strings.Join(hooks.Forwardable(), ", "))
		return m, false
	}
	var as, hears bool
	for _, k := range r.entries(e.v, e.path, "as", "hears") {
		switch k.key {
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
				flows := flowsFor(p.Flows, store.BeadType(t.key))
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
					if !slices.Contains(hooks.States(), word) {
						r.errf(s.v, s.path, "%q is no state: one of %s", word, strings.Join(hooks.States(), ", "))
						continue
					}
					if err := f.Places(word); err != nil {
						r.errf(s.v, s.path, "%v", err)
						continue
					}
					m.Map[store.MapKey(store.BeadType(t.key), s.key)] = word
				}
			}
		}
	}
	if e.v.Kind == yaml.MappingNode {
		if !as {
			r.errf(e.k, join(e.path, "as"), "missing: the crew member of the pack the status is set as")
		}
		if !hears {
			r.errf(e.k, join(e.path, "hears"), "missing: which stage of which bead type means which status")
		}
	}
	return m, true
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
	for _, f := range r.entries(e.v, e.path, "agent", "run", "people", "limit") {
		switch f.key {
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
	full := filepath.Join(r.dir, filepath.FromSlash(clean))
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
			// A pack from before the types were renamed says epic and
			// part, which are still read (store.BeadType).
			if s, ok := r.str(k.v, k.path); ok {
				f.Type = store.BeadType(s)
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
	for _, k := range r.entries(e.v, e.path, "role", "hears", "subscribe", "open_threads") {
		switch k.key {
		case "role":
			res.Role = r.rel(k.v, k.path, "role")
		case "hears":
			for _, h := range r.entries(k.v, k.path, "events", "stages", "done") {
				switch h.key {
				case "events":
					// A kind an earlier release logged (resident_gave_up)
					// names the one new events carry.
					for _, kind := range r.strs(h.v, h.path) {
						res.Events = append(res.Events, store.EventKind(kind))
					}
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
				if _, ok := hooks.Adapters[s.key]; !ok {
					r.errf(s.k, s.path, "this yardr has no adapter for source %s (it has %s)", s.key, strings.Join(hooks.Sources(), ", "))
					continue
				}
				scope, ok := r.str(s.v, s.path)
				if !ok {
					continue
				}
				if !slices.Contains(Scopes, scope) {
					r.errf(s.v, s.path, "scope %s: one of %s", scope, strings.Join(Scopes, ", "))
					continue
				}
				res.Subscribe = append(res.Subscribe, Source{Name: s.key, Scope: scope})
			}
		case "open_threads":
			for _, s := range r.strs(k.v, k.path) {
				if _, ok := hooks.OpenerFor(s); !ok {
					r.errf(k.v, k.path, "yardr cannot open threads in %s (it can in %s)", s, strings.Join(hooks.Openable(), ", "))
					continue
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
