package source

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// call reads one call of a declaration. credential says the file names a
// credential, inPath that the service takes it in the request path, and
// address that it is the address the call goes to (credential.url), so
// the call has no path of its own. A call names the request it sends, or
// run, the command that answers it: never both, and a run call is a read.
func (r *reader) call(e entry, credential, inPath, address bool) Call {
	c := Call{Name: e.key, Answer: Answer{Max: AnswerMax}}
	var request, build, run *entry
	kind := false
	for _, f := range r.entries(e.v, e.path, "kind", "about", "params", "request", "build", "run", "answer") {
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
		case "build":
			build = &f
			c.Build = r.callCommand(f, "a build's")
		case "run":
			run = &f
			c.Run = r.callCommand(f, "a run's")
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
	// A command answers the call: there is no request to read, and what
	// else the call names says how it would be sent.
	if run != nil {
		switch {
		case request != nil:
			r.errf(request.v, request.path, "a call names request or run, never both: a request is sent to the base, and a call that names run is answered by a command and sends none")
		case build != nil:
			r.errf(build.v, build.path, "a call names build or run, never both: build prints the body of a request, and a call that names run sends none")
		}
		if c.Kind == Update {
			r.errf(run.v, run.path, "a call that names run is a read: an update is a request, sent with a key so that the service writes it once, and a command is handed no key")
		}
		return c
	}
	if request == nil {
		r.errf(e.v, join(e.path, "request"), "missing: the method and the path the call sends, or run, the command that answers it")
		return c
	}
	hasBody := r.request(&c, *request, credential, inPath, address)
	if build != nil && hasBody {
		r.errf(build.v, build.path, "a call names request.body or build, never both")
	}
	var hasFile, hasSecret bool
	for _, p := range c.Params {
		hasFile, hasSecret = hasFile || p.Type == "file", hasSecret || p.Type == "secret-file"
	}
	if hasFile && hasSecret {
		r.errf(e.v, e.path, "a call names both a file and a secret-file parameter: a file goes as an upload and a secret as a body's leaf, never both in one call")
	}
	return c
}

// callCommand reads the command a call names, its build or its run: like
// every command a declaration brings, its program is a file beside the
// declaration's file, which the yard copies and hashes, and never the
// operator's own absolute one. whose says whose program it is, in the error.
func (r *reader) callCommand(f entry, whose string) []string {
	argv := r.command(f, "argument")
	if len(argv) > 0 && strings.HasPrefix(argv[0], "/") {
		r.errf(f.v, f.path, "the program %q is absolute: %s program must be relative, a file beside the declaration's file that the yard copies and hashes", argv[0], whose)
	}
	return argv
}

func (r *reader) param(e entry) Param {
	p := Param{Name: e.key}
	typed := false
	var pattern, most, values, defaultValue, logE *entry
	for _, f := range r.entries(e.v, e.path, "type", "pattern", "max", "values", "optional", "default", "log") {
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
		case "default":
			defaultValue = &f
		case "log":
			logE = &f
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
	// Max bounds a string in characters and a file in bytes; a file's is
	// held to no bound above, since max is the bound and may be left out.
	switch {
	case most == nil:
	case p.Type != "string" && p.Type != "file" && p.Type != "secret-file":
		r.errf(most.k, most.path, "is for a parameter of type string, file or secret-file, and this one is %s", p.Type)
	case p.Type == "string":
		p.Max, _ = r.count(most.v, most.path, AnswerLimit)
	default:
		p.Max, _ = r.count(most.v, most.path, 0)
	}
	if only(values, "enum") {
		p.Values = r.strs(values.v, values.path)
	}
	if p.Type == "enum" && len(p.Values) == 0 && values == nil {
		r.errf(e.v, join(e.path, "values"), "missing: what an enum may be ([a, b])")
	} else if p.Type == "enum" && len(p.Values) == 0 && values.v.Kind == yaml.SequenceNode && len(values.v.Content) == 0 {
		r.errf(values.v, values.path, "an enum that may be nothing")
	}
	if p.Type == "secret-file" && p.Log && logE != nil {
		r.errf(logE.v, logE.path, "a secret-file parameter is never logged: its record keeps the parameter's name alone")
	}
	if defaultValue != nil {
		if raw, ok := r.paramDefault(p, *defaultValue); ok {
			p.Default = raw
			p.Optional = true
		}
	}
	return p
}

// paramDefault reads a default as the parameter's own type, then checks the
// same string bounds that a caller's value will have. A default makes the
// parameter optional because callers need not repeat the value it supplies.
func (r *reader) paramDefault(p Param, e entry) (json.RawMessage, bool) {
	var value any
	switch p.Type {
	case "string", "enum":
		if e.v.Kind != yaml.ScalarNode || e.v.Tag != "!!str" {
			r.errf(e.v, e.path, "want a %s default, got %s", p.Type, describe(e.v))
			return nil, false
		}
		s := e.v.Value
		if p.Type == "enum" && !slices.Contains(p.Values, s) {
			r.errf(e.v, e.path, "default is one of %s", strings.Join(p.Values, ", "))
			return nil, false
		}
		if p.Max > 0 && utf8.RuneCountInString(s) > p.Max {
			r.errf(e.v, e.path, "default is %d characters, and at most %d", utf8.RuneCountInString(s), p.Max)
			return nil, false
		}
		if p.re != nil && !p.re.MatchString(s) {
			r.errf(e.v, e.path, "default does not match its pattern %s", p.Pattern)
			return nil, false
		}
		value = s
	case "int":
		if e.v.Kind != yaml.ScalarNode || e.v.Tag != "!!int" {
			r.errf(e.v, e.path, "want an int default, got %s", describe(e.v))
			return nil, false
		}
		n, err := strconv.ParseInt(e.v.Value, 0, 64)
		if err != nil {
			r.errf(e.v, e.path, "want a whole number, got %s", describe(e.v))
			return nil, false
		}
		value = n
	case "bool":
		b, ok := r.boolean(e.v, e.path)
		if !ok {
			return nil, false
		}
		value = b
	case "file", "secret-file":
		// A path, held to nothing here: its bounds are the file's, read
		// when the call is made.
		if e.v.Kind != yaml.ScalarNode || e.v.Tag != "!!str" {
			r.errf(e.v, e.path, "want a file default, got %s", describe(e.v))
			return nil, false
		}
		value = e.v.Value
	default:
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		r.errf(e.v, e.path, "cannot encode default: %v", err)
		return nil, false
	}
	return raw, true
}

func (r *reader) request(c *Call, e entry, credential, inPath, address bool) (hasBody bool) {
	method, path := false, false
	// marks checks the {name}s of a string that stands at where (a path,
	// a query): each a parameter of the call and none that may be absent,
	// and the credential only in a path the file says takes it (in_path).
	marks := func(n *yaml.Node, key, s, where string) bool {
		ok := true
		for _, m := range mark.FindAllStringSubmatch(s, -1) {
			p, declared := c.param(m[1])
			switch {
			case m[0] == CredentialMark && where == "a path" && inPath:
				continue
			case m[0] == CredentialMark && address:
				r.errf(n, key, "%s in %s: the credential is the address the call goes to (credential.url: true), and is sent nowhere else", CredentialMark, where)
			case m[0] == CredentialMark:
				msg := "%s in %s: a credential there is in the logs of every server on the way. It goes in the credential's header, or as a leaf of the body"
				if where == "a path" {
					msg += "; a service that takes it in the path says so (credential.in_path: true)"
				}
				r.errf(n, key, msg, CredentialMark, where)
			case !declared:
				r.errf(n, key, "%s is no parameter of the call %s", m[0], c.Name)
			case p.Type == "file":
				r.errf(n, key, "%s is a file: it stands as a value of the body, which is sent as one upload, not in %s", m[0], where)
			case p.Type == "secret-file":
				r.errf(n, key, "%s is a secret-file: it stands as a value of the body, not in %s", m[0], where)
			case p.Optional && len(p.Default) == 0 && where == "a path":
				r.errf(n, key, "%s is optional, and a path has no place for a value that is not given", m[0])
			case p.Optional && len(p.Default) == 0 && s != m[0]:
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
			case address && s != "/":
				r.errf(f.v, f.path, "%q: the credential is the address the call goes to (credential.url: true), so the call has no path of its own: leave it out, or write /", s)
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
			hasBody = true
			r.checkFileMarks(c, f.v, f.path)
			c.Request.Body = r.body(c, f.v, f.path, credential, address)
			// Left out of a mapping or a list, an optional parameter with no
			// default that is not given leaves a body. As the whole body it
			// leaves none to send, and what went instead was null.
			if m := mark.FindStringSubmatch(f.v.Value); f.v.Kind == yaml.ScalarNode && m != nil && m[0] == f.v.Value {
				if p, ok := c.param(m[1]); ok && p.Optional && len(p.Default) == 0 {
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
	if !path && address {
		c.Request.Path = "/"
	} else if !path {
		r.errf(e.v, join(e.path, "path"), "missing: the path under the source's base")
	}
	return hasBody
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

// checkFileMarks holds a file or secret-file parameter to where its
// value goes: alone as a value of the body's mapping, one value to one
// part or one leaf. Anywhere else there is nowhere to send it: no key
// of its own in a list or as the whole body, and no bytes or secret to
// put inside a larger string.
func (r *reader) checkFileMarks(c *Call, root *yaml.Node, key string) {
	// nested walks what is no value of the body's mapping: a file mark
	// under it stands nowhere an upload carries.
	var nested func(n *yaml.Node, key string)
	nested = func(n *yaml.Node, key string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				nested(n.Content[i+1], join(key, n.Content[i].Value))
			}
		case yaml.SequenceNode:
			for _, v := range n.Content {
				nested(v, key)
			}
		case yaml.ScalarNode:
			r.fileMark(c, n, key, false)
		}
	}
	if root.Kind != yaml.MappingNode {
		nested(root, key)
		return
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		p := join(key, k.Value)
		if v.Kind == yaml.ScalarNode {
			r.fileMark(c, v, p, true)
		} else {
			nested(v, p)
		}
	}
}

// fileMark refuses a file or secret-file parameter's mark where
// nothing carries it. topValue says the mark stands alone as a value
// of the body's mapping; what is no such mark is r.body's to check.
func (r *reader) fileMark(c *Call, n *yaml.Node, key string, topValue bool) {
	if n.Tag != "!!str" {
		return
	}
	for _, m := range mark.FindAllStringSubmatch(n.Value, -1) {
		p, declared := c.param(m[1])
		if !declared || (p.Type != "file" && p.Type != "secret-file") {
			continue
		}
		switch {
		case n.Value != m[0] && p.Type == "file":
			r.errf(n, key, "%s is a file: it stands alone as a value, which is sent as one upload", m[0])
		case n.Value != m[0]:
			r.errf(n, key, "%s is a secret-file: it stands alone as a value", m[0])
		case !topValue && p.Type == "file":
			r.errf(n, key, "%s is a file: it stands as a value of the body's mapping, which is sent as one upload", m[0])
		case !topValue:
			r.errf(n, key, "%s is a secret-file: it stands as a value of the body's mapping", m[0])
		}
	}
}

// body reads a request's body into what encoding/json sends: maps, lists
// and scalars. Its {name}s are checked here, so that Do finds nothing in a
// body it cannot fill.
func (r *reader) body(c *Call, n *yaml.Node, key string, credential, address bool) any {
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
				out[k.Value] = r.body(c, v, p, credential, address)
			}
		}
		return out
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, v := range n.Content {
			out = append(out, r.body(c, v, key, credential, address))
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
			case m[0] == CredentialMark && address:
				r.errf(n, key, "%s in a body: the credential is the address the call goes to (credential.url: true), and is sent nowhere else", CredentialMark)
			case m[0] == CredentialMark && n.Value != m[0]:
				r.errf(n, key, "%s is a whole value of a body, not a part of one", CredentialMark)
			case m[0] == CredentialMark && !credential:
				r.errf(n, key, "%s, and the file names no credential", CredentialMark)
			case m[0] == CredentialMark:
			case !declared:
				r.errf(n, key, "%s is no parameter of the call %s", m[0], c.Name)
			case p.Optional && len(p.Default) == 0 && n.Value != m[0]:
				r.errf(n, key, "%s is optional: it stands alone as a value, which is left out when it is not given", m[0])
			}
		}
		return n.Value
	}
	r.errf(n, key, "want a value, a mapping or a list, got %s", describe(n))
	return nil
}
