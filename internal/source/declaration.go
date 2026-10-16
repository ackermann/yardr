// Package source reads the declaration of a service outside the yard, one
// YAML file that names the service's address, the credential it takes and
// the requests the yard may make of it, and makes one such request (Do).
// A service is added by its file and no Go: what the file cannot say (a
// request that must be signed, an answer in pages, anything that is no
// JSON over HTTPS) is not a declared source.
//
// One thing a file can say is no request: a command the yard runs. The
// command that tells what a harness kind's subscription still carries (the
// key limits) is the one that is run today; internal/limits runs it. A file
// with that key may have no calls at all. The thread and link keys say how
// the yard speaks in the source's threads and how its issues are a bead's
// link — thread's by one command per thing the yard does there, run by
// internal/hooks. The hook key says what the service sends in: how the
// core verifies its webhooks, and the command that turns a verified body
// into the envelope.
//
// The events key says what the source sends in about pull requests and
// CI, and the commands that read them back: the daemon judges CI runs
// against the pull request's head, and reads the pull request and its CI
// live when no event told what it needs.
//
// The file holds no secret. It names the variable the credential is in, and
// the value is read from a file of the yard's at each call, so a
// declaration can be kept in the store, shown and copied.
package source

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// BuiltIn are the names a declaration may not take: the sources yardr
// has in Go (hooks.Adapters), but for one whose pack has taken the name
// while its Go adapter is still compiled in, its own name for itself
// (store.SourceYardr), and its name for the yards it is linked with
// (store.SourcePeer). The pack's source then wins every lookup when the
// yard has it. A test in hooks holds this list against the adapters.
var BuiltIn = []string{"peer", "token", "yardr"}

// MaxDeclaration bounds a declaration file: it is a list of requests, read
// whole at every use.
const MaxDeclaration = 256 << 10

// The kinds of a call. An update changes something in the service; a read
// does not, whatever its method (a search by POST, GraphQL).
const (
	Read   = "read"
	Update = "update"
)

// CredentialMark is where the credential's value goes: in the credential's
// header, or as a whole leaf of a body. Never in a path or a query, which
// end up in the logs of every proxy and server on the way.
const CredentialMark = "{credential}"

// Declaration is a source as its file declares it.
type Declaration struct {
	Name  string `json:"name"`
	About string `json:"about,omitempty"`
	// Base is where every call's path is under, without a trailing slash.
	// Empty for a source that declares no call, which has a Limits.
	Base       string      `json:"base"`
	Credential *Credential `json:"credential,omitempty"`
	// Check names the call `yardr source check` makes: a read call that
	// needs no parameter. Empty when the file names none.
	Check string `json:"check,omitempty"`
	// Idempotency is the name of the header the service takes a key in, by
	// which it tells an update it was sent before from a new one; "" for a
	// service that takes none. Whoever makes an update call gives the key
	// (DoKeyed): the same for a call run again after one whose outcome was
	// lost, so that it writes once.
	Idempotency string `json:"idempotency,omitempty"`
	Calls       []Call `json:"calls"` // in file order
	// Notify is the call the yard makes to tell a person outside the yard,
	// nil for a source that cannot: the capability a user names a source
	// for (yardr user set <name> notify=<source>).
	Notify *Notify `json:"notify,omitempty"`
	// Hear is which deliveries of the source the yard keeps, nil for a
	// source that keeps every delivery it verifies: one whose envelope
	// field holds an address a user's env file sets.
	Hear *Hear `json:"hear,omitempty"`
	// Limits is the command that says what a harness kind's subscription
	// still carries, nil for a source that cannot: see internal/limits.
	Limits *Limits `json:"limits,omitempty"`
	// Thread is how the yard speaks in the source's threads: a command per
	// thing it does there, nil for a source the yard does not speak in.
	Thread *Thread `json:"thread,omitempty"`
	// Link is the source's issues as a bead's link, nil for a source whose
	// issues are none.
	Link *Link `json:"link,omitempty"`
	// Identity is who a crew member is in the source, as the declaration
	// says it, nil for a source whose keys keep their case.
	Identity *Identity `json:"identity,omitempty"`
	// Hook is what the source sends in: how the core verifies its
	// webhooks and the command that turns a verified body into the
	// envelope, nil for a source the yard hears nothing from.
	Hook *Hook `json:"hook,omitempty"`
	// Events is what the source sends in about pull requests and CI, and
	// the commands that read them back, nil for a source without either.
	Events *Events `json:"events,omitempty"`
}

// Credential is where a source's credential comes from and where it goes.
type Credential struct {
	// Env is the name of the variable that holds it, in the env file the
	// operator sets for the source. The name, never the value.
	Env string `json:"env"`
	// Header is the header it is sent in, "Name: text {credential}", for a
	// service that takes it there. One that takes it in the body names it
	// as a leaf of the call's body instead.
	Header string `json:"header,omitempty"`
	// UpdateEnv is the name of the variable an update call is sent with,
	// in the same file, so that the read calls can run on a credential
	// that may only read. Empty for a source with one credential, which
	// its updates are sent with too.
	UpdateEnv string `json:"update_env,omitempty"`
	// Mint are the names of the variables that together select a minted
	// credential: when the acting env sets every one, the core runs the
	// source's mint_command with their values and hands what it mints to
	// the call under Env. Names only, never values and never the command,
	// so a pack may bring them as it brings Env. Empty for a source whose
	// credential is used as it is set.
	Mint []string `json:"mint,omitempty"`
	// InPath says the service takes the credential in the request path,
	// so a call's path may name {credential}. It is said out loud because
	// a credential there is in the logs of every server on the way;
	// without it a path refuses one. A query refuses one either way.
	InPath bool `json:"in_path,omitempty"`
}

// Minted reports whether the credential may be minted: the file names the
// variables a mint takes. Nil-safe, for a file that names no credential.
func (c *Credential) Minted() bool { return c != nil && len(c.Mint) > 0 }

// EnvOf is the name of the variable a call of kind is sent with.
func (c *Credential) EnvOf(kind string) string {
	if kind == Update && c.UpdateEnv != "" {
		return c.UpdateEnv
	}
	return c.Env
}

// Envs are the names of the variables the source's env file sets: the
// credential's, and the one of its updates where that is another.
func (c *Credential) Envs() []string {
	if c.UpdateEnv != "" {
		return []string{c.Env, c.UpdateEnv}
	}
	return []string{c.Env}
}

// Names says in words what an env file sets for the credential: the static
// names, or with a mint, the mint's names or the static ones, the mint
// first, since it is what a call uses when both are set.
func (c *Credential) Names() string {
	if !c.Minted() {
		return strings.Join(c.Envs(), " and ")
	}
	return strings.Join(c.Mint, " and ") + ", or " + strings.Join(c.Envs(), " and ")
}

// Variables are the names an env file may set for the credential, every
// one the declaration has: the mint's inputs first, then the static ones.
func (c *Credential) Variables() []string {
	if c == nil {
		return nil
	}
	return append(append([]string{}, c.Mint...), c.Envs()...)
}

// Call is one request the yard may make of the source.
type Call struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	About   string  `json:"about,omitempty"`
	Params  []Param `json:"params,omitempty"` // in file order
	Request Request `json:"request"`
	Answer  Answer  `json:"answer"`
}

// Param is one parameter of a call. All are required unless Optional.
type Param struct {
	Name string `json:"name"`
	// Type is string, int, bool or enum.
	Type     string   `json:"type"`
	Pattern  string   `json:"pattern,omitempty"` // of a string: a regular expression it must match
	Max      int      `json:"max,omitempty"`     // of a string: its length at most, in characters
	Values   []string `json:"values,omitempty"`  // of an enum: what it may be
	Optional bool     `json:"optional,omitempty"`
	// Log keeps the parameter's value in the record of each call (the
	// source_call event), which otherwise has its name alone: for what an
	// audit asks by (a customer's id), and never for free text.
	Log bool `json:"log,omitempty"`

	re *regexp.Regexp
}

// Request is what a call sends. A {param} in Path, Query or Body is
// replaced by the parameter's value, escaped by where it stands.
type Request struct {
	Method string       `json:"method"`
	Path   string       `json:"path"`
	Query  []QueryValue `json:"query,omitempty"` // in file order
	// Body is sent as JSON: maps, lists and scalars as the file has them.
	// A string that is exactly {param} becomes the parameter's typed value.
	Body any `json:"body,omitempty"`
}

// QueryValue is one key of a request's query.
type QueryValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Answer is what of the service's answer a call returns.
type Answer struct {
	// Select keeps these top-level keys, of each element when the answer
	// is a list; without it the answer is returned as it is.
	Select []string `json:"select,omitempty"`
	// Max is the most a call returns, in bytes after Select: AnswerMax
	// unless the file raises it, AnswerLimit at most.
	Max int `json:"max"`
}

// Call returns the call of that name.
func (d *Declaration) Call(name string) (*Call, bool) {
	for i := range d.Calls {
		if d.Calls[i].Name == name {
			return &d.Calls[i], true
		}
	}
	return nil, false
}

func (c *Call) param(name string) (*Param, bool) {
	if c == nil {
		return nil, false
	}
	for i := range c.Params {
		if c.Params[i].Name == name {
			return &c.Params[i], true
		}
	}
	return nil, false
}

// usesParam reports whether the request sends name in its path, query or
// body. Declaring a parameter alone does not put its value in the request.
func (c *Call) usesParam(name string) bool {
	mark := "{" + name + "}"
	if strings.Contains(c.Request.Path, mark) {
		return true
	}
	for _, q := range c.Request.Query {
		if strings.Contains(q.Value, mark) {
			return true
		}
	}
	var inBody func(any) bool
	inBody = func(v any) bool {
		switch v := v.(type) {
		case string:
			return strings.Contains(v, mark)
		case map[string]any:
			for _, child := range v {
				if inBody(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if inBody(child) {
					return true
				}
			}
		}
		return false
	}
	return inBody(c.Request.Body)
}

// Line is the call in a line: "POST /messages.json".
func (r Request) Line() string { return r.Method + " " + r.Path }

// Error is one thing wrong with a declaration: the file, the line and the
// key as a dotted path from the file's root.
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

// Errors is everything wrong with a declaration, in file order: it is read
// to its end so one run names every mistake.
type Errors []Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

var (
	// validName is what a source and a call may be named: each becomes a
	// key in the store and a word on a command line.
	validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// paramName is also the name of a variable, for the credential's.
	paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// mark is a {name} in a path, a query or a body.
	mark       = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	headerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	// hookKey is a key a hook's events may carry in their fields, as a
	// subscription's --where names it.
	hookKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
)

var (
	methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
	types   = []string{"string", "int", "bool", "enum"}
)

// booleans are the plain scalars YAML 1.1 reads as a boolean (see
// internal/pack, whose reader this one follows): a key or a word that is
// one would be a name to this reader and a boolean to the next.
var booleans = []string{"y", "yes", "n", "no", "on", "off", "true", "false"}

func booleanLike(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!bool" || (n.Style == 0 && slices.Contains(booleans, strings.ToLower(n.Value))))
}

// Parse reads the declaration in text. file is how errors name it. What it
// returns with a nil error is safe to call: every refusal that does not
// depend on a parameter's value is made here.
func Parse(file string, text []byte) (*Declaration, error) {
	if len(text) > MaxDeclaration {
		return nil, Errors{{File: file, Msg: fmt.Sprintf("%d bytes: a declaration is at most %d", len(text), MaxDeclaration)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, Errors{{File: file, Msg: err.Error()}}
	}
	if len(doc.Content) == 0 {
		return nil, Errors{{File: file, Msg: "the file is empty"}}
	}
	r := &reader{file: file}
	d := &Declaration{}
	r.declaration(d, doc.Content[0])
	if len(r.errs) > 0 {
		sort.SliceStable(r.errs, func(i, j int) bool { return r.errs[i].Line < r.errs[j].Line })
		return nil, r.errs
	}
	return d, nil
}

type reader struct {
	file string
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
// key outside it is an error. A key YAML reads as a boolean, a key that is
// not a string and a key given twice are errors always, and such an entry
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
		return "an alias (not read in a declaration)"
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

func (r *reader) boolean(n *yaml.Node, key string) (value, ok bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || (n.Value != "true" && n.Value != "false") {
		r.errf(n, key, "want true or false, got %s", describe(n))
		return false, false
	}
	return n.Value == "true", true
}

// count reads a whole number from 1 to most.
func (r *reader) count(n *yaml.Node, key string, most int) (int, bool) {
	v, err := strconv.Atoi(n.Value)
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || err != nil || v < 1 || v > most {
		r.errf(n, key, "want a whole number from 1 to %d, got %s", most, describe(n))
		return 0, false
	}
	return v, true
}

func (r *reader) declaration(d *Declaration, root *yaml.Node) {
	var calls, check, notify, hear, base, credential, idempotency, hook *entry
	named := false
	for _, e := range r.entries(root, "", "name", "about", "base", "credential", "check", "idempotency", "calls", "notify", "hear", "limits", "thread", "link", "hook", "identity", "events") {
		switch e.key {
		case "name":
			named = true
			name, ok := r.str(e.v, e.path)
			switch {
			case !ok:
			case !validName.MatchString(name):
				r.errf(e.v, e.path, "name %q: use lowercase letters, digits, - and _, starting with a letter or digit", name)
			case slices.Contains(BuiltIn, name):
				r.errf(e.v, e.path, "%s is a source yardr has built in (%s): a declared source takes another name", name, strings.Join(BuiltIn, ", "))
			default:
				d.Name = name
			}
		case "about":
			d.About, _ = r.str(e.v, e.path)
		case "base":
			base = &e
			if s, ok := r.str(e.v, e.path); ok {
				d.Base = r.base(e.v, e.path, s)
			}
		case "credential":
			credential = &e
			d.Credential = r.credential(e)
		case "check":
			check = &e
		case "idempotency":
			idempotency = &e
		case "calls":
			calls = &e
		case "notify":
			notify = &e
		case "hear":
			hear = &e
		case "limits":
			d.Limits = r.limits(e)
		case "thread":
			d.Thread = r.thread(e)
		case "link":
			d.Link = r.link(e)
		case "hook":
			hook = &e
			d.Hook = r.hook(e)
		case "identity":
			d.Identity = r.identity(e)
		case "events":
			d.Events = r.events(e)
		}
	}
	if root.Kind != yaml.MappingNode {
		return
	}
	if !named {
		r.errf(root, "name", "missing: what the source is called in this yard")
	}
	if calls == nil && (d.Limits != nil || (d.Hook != nil && d.Hook.Register == nil)) {
		// A source that is its limits command and/or what it sends in,
		// and nothing else: there is no request, so nothing a request
		// needs has a meaning.
		alone := "limits alone"
		if d.Hook != nil && d.Limits != nil {
			alone = "hook and limits alone"
		} else if d.Hook != nil {
			alone = "hook alone"
		}
		for _, e := range []*entry{base, credential, check, idempotency, notify, hear} {
			if e != nil {
				r.errf(e.k, e.path, "is for the calls of a source, and this file declares none (it has %s)", alone)
			}
		}
		d.Base, d.Credential = "", nil
		return
	}
	if base == nil {
		r.errf(root, "base", "missing: the address every call's path is under (https://…)")
	}
	if calls == nil {
		r.errf(root, "calls", "missing: a source is the calls it declares, or its limits command, or what it sends in (hook)")
		return
	}
	for _, e := range r.entries(calls.v, calls.path) {
		switch {
		case !validName.MatchString(e.key):
			r.errf(e.k, e.path, "name %q: use lowercase letters, digits, - and _, starting with a letter or digit", e.key)
		// The word a grant uses for every read call of a source.
		case e.key == Read:
			r.errf(e.k, e.path, "a call cannot be named %s: the word stands for every read call of a source", Read)
		default:
			inPath := d.Credential != nil && d.Credential.InPath
			d.Calls = append(d.Calls, r.call(e, d.Credential != nil, inPath))
		}
	}
	if calls.v.Kind == yaml.MappingNode && len(calls.v.Content) == 0 {
		r.errf(calls.v, calls.path, "no call: a source is the calls it declares")
	}
	if check != nil {
		name, ok := r.str(check.v, check.path)
		c, found := d.Call(name)
		switch {
		case !ok:
		case !found:
			r.errf(check.v, check.path, "%s is no call of this file", name)
		case c.Kind != Read:
			r.errf(check.v, check.path, "%s is an update: the check is a read call, made to see that the source answers", name)
		case slices.ContainsFunc(c.Params, func(p Param) bool { return !p.Optional }):
			r.errf(check.v, check.path, "%s needs a parameter: the check is a call without any", name)
		default:
			d.Check = name
		}
	}
	if idempotency != nil {
		d.Idempotency = r.idempotency(d, *idempotency)
	}
	if notify != nil {
		d.Notify = r.notify(d, *notify)
	}
	if hear != nil {
		d.Hear = r.hear(d, *hear)
	}
	if hook != nil && d.Hook != nil && d.Hook.Register != nil {
		r.hookRegisterCheck(d, *hook)
	}
}

// hookRegisterCheck holds a hook's register to the calls as read: the
// call is there and is an update, its URL and secret parameters are
// its own and take text, and nothing it requires is left unfilled.
// What would fail at every registration is refused here.
func (r *reader) hookRegisterCheck(d *Declaration, hookE entry) {
	reg := d.Hook.Register
	base := join(hookE.path, "register")
	c, found := d.Call(reg.Call)
	if reg.Call == "" {
		return // hookRegister named the missing call
	}
	if !found {
		r.errf(hookE.v, join(base, "call"), "%s is no call of this file", reg.Call)
		return
	}
	if c.Kind != Update {
		r.errf(hookE.v, join(base, "call"), "%s is a %s call: registering a webhook sends something, so it is an %s", reg.Call, c.Kind, Update)
		return
	}
	fills := map[string]string{}
	param := func(name, key string) {
		if name == "" {
			return
		}
		p, ok := c.param(name)
		switch {
		case !ok:
			r.errf(hookE.v, key, "%s is no parameter of the call %s (it takes: %s)", name, c.Name, or(strings.Join(c.names(), ", "), "none"))
		case p.Type != "string":
			r.errf(hookE.v, key, "the parameter %s is %s: what the yard gives is text, so it is a string", name, p.Type)
		case fills[name] != "":
			r.errf(hookE.v, key, "the parameter %s is filled by %s already", name, fills[name])
		case !c.usesParam(name):
			r.errf(hookE.v, key, "the call %s does not send parameter %s in its path, query or body", c.Name, name)
		default:
			fills[name] = key
		}
	}
	param(reg.URL, join(base, "url"))
	param(reg.Secret, join(base, "secret"))
	for _, p := range c.Params {
		if !p.Optional && fills[p.Name] == "" {
			r.errf(hookE.v, base, "the call %s needs the parameter %s, and nothing here fills it (url and secret are what the yard has)", c.Name, p.Name)
		}
	}
}

// ownHeaders are the headers Do sets on every request itself.
var ownHeaders = []string{"Accept", "Content-Type", "User-Agent"}

// idempotency reads the name of the header an update's key goes in. What
// it refuses is a header that is another's already, the credential's above
// all, and a file whose calls are all reads, which no key is sent with.
func (r *reader) idempotency(d *Declaration, e entry) string {
	name, ok := r.str(e.v, e.path)
	if !ok {
		return ""
	}
	credential := ""
	if d.Credential != nil {
		credential, _, _ = strings.Cut(d.Credential.Header, ":")
	}
	switch {
	case !headerName.MatchString(name):
		r.errf(e.v, e.path, "%q is no name of a header: this is the header the service takes an update's key in (Idempotency-Key)", name)
	case credential != "" && strings.EqualFold(name, credential):
		r.errf(e.v, e.path, "%s is the header the credential is sent in", name)
	case slices.ContainsFunc(ownHeaders, func(h string) bool { return strings.EqualFold(h, name) }):
		r.errf(e.v, e.path, "%s is a header every call is sent with already (%s)", name, strings.Join(ownHeaders, ", "))
	case !slices.ContainsFunc(d.Calls, func(c Call) bool { return c.Kind == Update }):
		r.errf(e.v, e.path, "the file has no %s call, and a key is sent with those alone", Update)
	default:
		return name
	}
	return ""
}

// command reads one command of a declaration: the program and its
// arguments, a list of strings run without a shell, so each word is one
// element. example shows the list's shape in an error (`"{provider}",
// --json`). A program with no / at its start is a file beside the
// declaration's file; one with a '.' or '..' segment would leave it.
func (r *reader) command(f entry, example string) []string {
	if f.v.Kind != yaml.SequenceNode {
		r.errf(f.v, f.path, "want a list of strings ([program, %s]), got %s: the command is run without a shell, so each word is one element", example, describe(f.v))
		return nil
	}
	var out []string
	whole := true
	for _, c := range f.v.Content {
		s, ok := r.str(c, f.path)
		whole = whole && ok
		out = append(out, s)
	}
	switch {
	case !whole:
		return nil
	case len(out) == 0:
		r.errf(f.v, f.path, "an empty list: want the program and its arguments")
		return nil
	case hasDots(out[0]):
		r.errf(f.v, f.path, "the program %q has a '.' or '..' segment: one with no / at its start is a file beside the declaration's file, and stays under it", out[0])
		return nil
	}
	return out
}

// base reads the address calls are under. Anything but https is refused,
// since the credential goes with every call; plain http is for a stand-in
// on this machine alone. Do holds a call to that again when it connects.
func (r *reader) base(n *yaml.Node, key, s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil || u.Host == "" || u.Opaque != "":
		r.errf(n, key, "%q is no address: want https://host/path", s)
	case u.User != nil:
		r.errf(n, key, "an address with a user or password in it: the credential is named under credential, never written here")
	case u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s, "?#"):
		r.errf(n, key, "an address with a query or a fragment: a call's query is under its request")
	case u.Scheme == "http" && !loopbackHost(u.Hostname()):
		r.errf(n, key, "http:// is for a stand-in on this machine only (127.0.0.1, [::1], localhost): %s is not, so it is https:// or nothing", u.Hostname())
	case u.Scheme != "https" && u.Scheme != "http":
		r.errf(n, key, "%s:// is not https://", u.Scheme)
	case hasDots(u.Path) || strings.Contains(u.Path, "//") || strings.Contains(s, `\`):
		r.errf(n, key, "the path of an address has no '..', '//' or '\\'")
	default:
		return strings.TrimRight(s, "/")
	}
	return ""
}

// loopbackHost reports whether host, as written, is this machine's
// loopback: an address of it, or the name every system gives it.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hasDots reports whether a segment of path is "." or "..".
func hasDots(path string) bool {
	return slices.ContainsFunc(strings.Split(path, "/"), func(s string) bool { return s == "." || s == ".." })
}

func (r *reader) credential(e entry) *Credential {
	c, has := &Credential{}, false
	var update, mint *entry
	for _, f := range r.entries(e.v, e.path, "env", "update_env", "header", "mint", "in_path") {
		switch f.key {
		case "env":
			has = true
			if s, ok := r.str(f.v, f.path); ok && !paramName.MatchString(s) {
				r.errf(f.v, f.path, "%q is no name of a variable: this is the name the credential is under in the source's env file, never the credential", s)
			} else if ok {
				c.Env = s
			}
		case "update_env":
			update = &f
			if s, ok := r.str(f.v, f.path); ok && !paramName.MatchString(s) {
				r.errf(f.v, f.path, "%q is no name of a variable: this is the name the credential of an update call is under in the source's env file, never the credential", s)
			} else if ok {
				c.UpdateEnv = s
			}
		case "mint":
			mint = &f
			c.Mint = r.mintNames(f)
		case "in_path":
			if v, ok := r.boolean(f.v, f.path); ok {
				c.InPath = v
			}
		case "header":
			s, ok := r.str(f.v, f.path)
			if !ok {
				continue
			}
			name, value, cut := strings.Cut(s, ":")
			marks := mark.FindAllString(value, -1)
			switch {
			case !cut || !headerName.MatchString(name) || strings.TrimSpace(value) == "":
				r.errf(f.v, f.path, `want "Name: text %s", got %q`, CredentialMark, s)
			case strings.ContainsFunc(value, func(c rune) bool { return c < ' ' || c == 0x7f }):
				r.errf(f.v, f.path, "a header is one line")
			case len(marks) != 1 || marks[0] != CredentialMark:
				r.errf(f.v, f.path, "a header is fixed text and %s, once: a parameter never reaches a header", CredentialMark)
			default:
				c.Header = name + ": " + strings.TrimSpace(value)
			}
		}
	}
	if e.v.Kind == yaml.MappingNode && !has {
		r.errf(e.v, join(e.path, "env"), "missing: the name of the variable the credential is in")
	}
	// The same name twice says there are two credentials where there is one.
	if update != nil && c.UpdateEnv != "" && c.UpdateEnv == c.Env {
		r.errf(update.v, update.path, "%s is env already: update_env is for a second credential, which the update calls are sent with. Leave it out for a source with one", c.Env)
		c.UpdateEnv = ""
	}
	// A mint's name cannot be the variable the minted token is handed
	// under: the mint's inputs are beside the credential, never it.
	if mint != nil {
		for _, m := range c.Mint {
			switch m {
			case c.Env:
				r.errf(mint.v, mint.path, "%s is env already: mint names the variables the token is minted from, beside the variable the token is handed under", m)
			case c.UpdateEnv:
				r.errf(mint.v, mint.path, "%s is update_env already: mint names the variables the token is minted from, beside the variables a call is sent with", m)
			}
		}
	}
	return c
}

// mintNames reads the names of the variables that together select a minted
// credential: a list of one or more, each a name of a variable, never a
// value. env stays required beside them: it is the variable the minted
// token is handed under.
func (r *reader) mintNames(e entry) []string {
	if e.v.Kind == yaml.SequenceNode && len(e.v.Content) == 0 {
		r.errf(e.v, e.path, "missing: the names of the variables that together select a minted credential")
		return nil
	}
	var out []string
	for _, s := range r.strs(e.v, e.path) {
		if !paramName.MatchString(s) {
			r.errf(e.v, e.path, "%q is no name of a variable: these are the names the mint's inputs are under in the source's env file, never the values", s)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (r *reader) call(e entry, credential, inPath bool) Call {
	c := Call{Name: e.key, Answer: Answer{Max: AnswerMax}}
	var request *entry
	kind := false
	for _, f := range r.entries(e.v, e.path, "kind", "about", "params", "request", "answer") {
		switch f.key {
		case "kind":
			kind = true
			if s, ok := r.str(f.v, f.path); ok && s != Read && s != Update {
				r.errf(f.v, f.path, "%q: want %s or %s", s, Read, Update)
			} else if ok {
				c.Kind = s
			}
		case "about":
			c.About, _ = r.str(f.v, f.path)
		case "params":
			for _, p := range r.entries(f.v, f.path) {
				switch {
				case !paramName.MatchString(p.key):
					r.errf(p.k, p.path, "name %q: use letters, digits and _, not starting with a digit", p.key)
				case p.key == "credential":
					r.errf(p.k, p.path, "a parameter cannot be named credential: %s is the source's own", CredentialMark)
				default:
					c.Params = append(c.Params, r.param(p))
				}
			}
		case "request":
			request = &f
		case "answer":
			for _, a := range r.entries(f.v, f.path, "select", "max") {
				switch a.key {
				case "select":
					c.Answer.Select = r.strs(a.v, a.path)
				case "max":
					if n, ok := r.count(a.v, a.path, AnswerLimit); ok {
						c.Answer.Max = n
					}
				}
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return c
	}
	if !kind {
		r.errf(e.v, join(e.path, "kind"), "missing: %s or %s", Read, Update)
	}
	if request == nil {
		r.errf(e.v, join(e.path, "request"), "missing: the method and the path the call sends")
		return c
	}
	r.request(&c, *request, credential, inPath)
	return c
}

func (r *reader) param(e entry) Param {
	p := Param{Name: e.key}
	typed := false
	var pattern, most, values *entry
	for _, f := range r.entries(e.v, e.path, "type", "pattern", "max", "values", "optional", "log") {
		switch f.key {
		case "type":
			typed = true
			if s, ok := r.str(f.v, f.path); ok && !slices.Contains(types, s) {
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(types, ", "))
			} else if ok {
				p.Type = s
			}
		case "pattern":
			pattern = &f
		case "max":
			most = &f
		case "values":
			values = &f
		case "optional":
			p.Optional, _ = r.boolean(f.v, f.path)
		case "log":
			p.Log, _ = r.boolean(f.v, f.path)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return p
	}
	if !typed {
		r.errf(e.v, join(e.path, "type"), "missing: %s", strings.Join(types, ", "))
	}
	if p.Type == "" {
		return p
	}
	// only refuses a key under a type it says nothing of.
	only := func(f *entry, typ string) bool {
		if f != nil && p.Type != typ {
			r.errf(f.k, f.path, "is for a parameter of type %s, and this one is %s", typ, p.Type)
			return false
		}
		return f != nil
	}
	if only(pattern, "string") {
		if s, ok := r.str(pattern.v, pattern.path); ok {
			re, err := regexp.Compile(s)
			if err != nil {
				r.errf(pattern.v, pattern.path, "%v", err)
			} else {
				p.Pattern, p.re = s, re
			}
		}
	}
	if only(most, "string") {
		p.Max, _ = r.count(most.v, most.path, AnswerLimit)
	}
	if only(values, "enum") {
		p.Values = r.strs(values.v, values.path)
	}
	if p.Type == "enum" && len(p.Values) == 0 && values == nil {
		r.errf(e.v, join(e.path, "values"), "missing: what an enum may be ([a, b])")
	} else if p.Type == "enum" && len(p.Values) == 0 && values.v.Kind == yaml.SequenceNode && len(values.v.Content) == 0 {
		r.errf(values.v, values.path, "an enum that may be nothing")
	}
	return p
}

func (r *reader) request(c *Call, e entry, credential, inPath bool) {
	method, path := false, false
	// marks checks the {name}s of a string that stands at where (a path,
	// a query): each a parameter of the call and none optional, and the
	// credential only in a path the file says takes it (in_path).
	marks := func(n *yaml.Node, key, s, where string) bool {
		ok := true
		for _, m := range mark.FindAllStringSubmatch(s, -1) {
			p, declared := c.param(m[1])
			switch {
			case m[0] == CredentialMark && where == "a path" && inPath:
				continue
			case m[0] == CredentialMark:
				msg := "%s in %s: a credential there is in the logs of every server on the way. It goes in the credential's header, or as a leaf of the body"
				if where == "a path" {
					msg += "; a service that takes it in the path says so (credential.in_path: true)"
				}
				r.errf(n, key, msg, CredentialMark, where)
			case !declared:
				r.errf(n, key, "%s is no parameter of the call %s", m[0], c.Name)
			case p.Optional && where == "a path":
				r.errf(n, key, "%s is optional, and a path has no place for a value that is not given", m[0])
			case p.Optional && s != m[0]:
				r.errf(n, key, "%s is optional: it stands alone as a value, which is left out when it is not given", m[0])
			default:
				continue
			}
			ok = false
		}
		return ok
	}
	for _, f := range r.entries(e.v, e.path, "method", "path", "query", "body") {
		switch f.key {
		case "method":
			method = true
			s, ok := r.str(f.v, f.path)
			switch {
			case !ok:
			case !slices.Contains(methods, s):
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(methods, ", "))
			case c.Kind == Read && s != "GET" && s != "POST":
				r.errf(f.v, f.path, "%s in a call of kind %s: %s changes what it is sent to, so the call is an %s", s, Read, s, Update)
			default:
				c.Request.Method = s
			}
		case "path":
			path = true
			s, ok := r.str(f.v, f.path)
			switch {
			case !ok:
			case strings.Contains(s, "://") || strings.HasPrefix(s, "//"):
				r.errf(f.v, f.path, "%q is an address, not a path: a call goes to the source's base and nowhere else", s)
			case !strings.HasPrefix(s, "/"):
				r.errf(f.v, f.path, "%q: a path begins with /", s)
			case hasDots(s):
				r.errf(f.v, f.path, "%q has a '.' or '..' segment, which leaves the source's base", s)
			case strings.ContainsAny(s, "?#\\") || strings.ContainsFunc(s, func(c rune) bool { return c <= ' ' || c == 0x7f }):
				r.errf(f.v, f.path, "%q: a path has no '?', '#', '\\' or space (a query is under query)", s)
			case marks(f.v, f.path, s, "a path"):
				c.Request.Path = s
			}
		case "query":
			for _, q := range r.pairs(f.v, f.path) {
				if mark.MatchString(q.key) {
					r.errf(q.k, q.path, "a key is fixed text: a parameter is a value")
				} else if marks(q.v, q.path, q.v.Value, "a query") {
					c.Request.Query = append(c.Request.Query, QueryValue{Key: q.key, Value: q.v.Value})
				}
			}
		case "body":
			c.Request.Body = r.body(c, f.v, f.path, credential)
			// Left out of a mapping or a list, an optional parameter that is
			// not given leaves a body. As the whole body it leaves none to
			// send, and what went instead was null.
			if m := mark.FindStringSubmatch(f.v.Value); f.v.Kind == yaml.ScalarNode && m != nil && m[0] == f.v.Value {
				if p, ok := c.param(m[1]); ok && p.Optional {
					r.errf(f.v, f.path, "%s is optional, and it is the whole body: not given, there is no body to leave it out of. Put it in a mapping ({ key: %q }), or make it required", m[0], m[0])
				}
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return
	}
	if !method {
		r.errf(e.v, join(e.path, "method"), "missing: %s", strings.Join(methods, ", "))
	}
	if !path {
		r.errf(e.v, join(e.path, "path"), "missing: the path under the source's base")
	}
}

// pairs are a query's keys and values: any text for a key (filter[name]),
// one line of text, a number or true or false for a value.
func (r *reader) pairs(n *yaml.Node, key string) []entry {
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
		case k.Kind != yaml.ScalarNode || k.Value == "":
			r.errf(k, p, "a key must be a string, got %s", describe(k))
		case seen[k.Value]:
			r.errf(k, p, "given twice")
		case v.Kind != yaml.ScalarNode || v.Tag == "!!null":
			r.errf(v, p, "want one value, got %s", describe(v))
		default:
			seen[k.Value] = true
			out = append(out, entry{key: k.Value, path: p, k: k, v: v})
		}
	}
	return out
}

// body reads a request's body into what encoding/json sends: maps, lists
// and scalars. Its {name}s are checked here, so that Do finds nothing in a
// body it cannot fill.
func (r *reader) body(c *Call, n *yaml.Node, key string, credential bool) any {
	switch n.Kind {
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := join(key, k.Value)
			switch {
			case k.Kind != yaml.ScalarNode:
				r.errf(k, p, "a key must be a string, got %s", describe(k))
			case mark.MatchString(k.Value):
				r.errf(k, p, "a key is fixed text: a parameter or the credential is a value")
			default:
				if _, twice := out[k.Value]; twice {
					r.errf(k, p, "given twice")
				}
				out[k.Value] = r.body(c, v, p, credential)
			}
		}
		return out
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, v := range n.Content {
			out = append(out, r.body(c, v, key, credential))
		}
		return out
	case yaml.ScalarNode:
		if n.Tag != "!!str" {
			var v any
			if err := n.Decode(&v); err != nil {
				r.errf(n, key, "%v", err)
			}
			return v
		}
		for _, m := range mark.FindAllStringSubmatch(n.Value, -1) {
			p, declared := c.param(m[1])
			switch {
			case m[0] == CredentialMark && n.Value != m[0]:
				r.errf(n, key, "%s is a whole value of a body, not a part of one", CredentialMark)
			case m[0] == CredentialMark && !credential:
				r.errf(n, key, "%s, and the file names no credential", CredentialMark)
			case m[0] == CredentialMark:
			case !declared:
				r.errf(n, key, "%s is no parameter of the call %s", m[0], c.Name)
			case p.Optional && n.Value != m[0]:
				r.errf(n, key, "%s is optional: it stands alone as a value, which is left out when it is not given", m[0])
			}
		}
		return n.Value
	}
	r.errf(n, key, "want a value, a mapping or a list, got %s", describe(n))
	return nil
}
