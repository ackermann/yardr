package source

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Notify says which call of a source tells a person, and which of the
// call's parameters take the four values the yard has for it: where the
// person is reached, a title, a text and a link.
type Notify struct {
	Call string `json:"call"`
	// Address is the parameter that takes where the person is reached, and
	// AddressEnv the name of the variable that holds it in the user's own
	// env file: it is one person's and often private, so it is kept as the
	// source's credential is, and is no more in the store.
	Address    string `json:"address"`
	AddressEnv string `json:"address_env"`
	// Title, Text and Link are the parameters that take each; "" for one
	// the call has no place for. One of Title and Text is always there.
	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"`
	Link  string `json:"link,omitempty"`
}

// Hear says which deliveries of the source the yard keeps: one whose
// envelope field holds an address a user's env file sets, whatever the
// user is told through. A delivery from anything else is dropped after
// verification: answered 200, recorded nowhere, and counted in sums
// beside refusals.
type Hear struct {
	// Field is the envelope field that must hold a user's address.
	Field string `json:"field"`
	// Address is where that address is read from: the variable of the
	// user's own env file, the same one telling reads.
	Address HearAddress `json:"address"`
	// From narrows whom the source hears: HearFromOperator, or "" for
	// every user whose env file sets the address.
	From string `json:"from,omitempty"`
}

// HearAddress is where the address is read from: the variable of the
// user's own env file, as notify.address says it.
type HearAddress struct {
	Env string `json:"env"`
}

// HearFromOperator is the one from a hear key takes: the yard's operator
// (the operator setting, a user of the yard), so the file names a role,
// never a person.
const HearFromOperator = "operator"

// notify reads the key that makes a source one a user can be told through.
// What it refuses is what would fail at every send, or at some: a call that
// is not there or only reads, a value given to no parameter or to one that
// is no text, a required parameter nothing fills, and a link the call
// insists on, since most of what the yard tells has none.
func (r *reader) notify(d *Declaration, e entry) *Notify {
	n := &Notify{}
	var c *Call
	called, addressed := false, false
	fills := map[string]string{} // parameter -> the key that fills it
	// param reads the name of a parameter of the call that takes text.
	param := func(f entry) string {
		name, ok := r.str(f.v, f.path)
		if !ok || c == nil {
			return ""
		}
		p, found := c.param(name)
		switch {
		case !found:
			r.errf(f.v, f.path, "%s is no parameter of the call %s (it takes: %s)", name, c.Name, or(strings.Join(c.names(), ", "), "none"))
		case p.Type != "string":
			r.errf(f.v, f.path, "the parameter %s is %s: what the yard gives is text, so it is a string", name, p.Type)
		case fills[name] != "":
			r.errf(f.v, f.path, "the parameter %s is filled by %s already", name, fills[name])
		default:
			fills[name] = f.path
			return name
		}
		return ""
	}
	fields := r.entries(e.v, e.path, "call", "address", "title", "text", "link")
	// The call first, wherever the file has it: the others name its
	// parameters.
	for _, f := range fields {
		if f.key != "call" {
			continue
		}
		called = true
		name, ok := r.str(f.v, f.path)
		found, has := d.Call(name)
		switch {
		case !ok:
		case !has:
			r.errf(f.v, f.path, "%s is no call of this file", name)
		case found.Kind != Update:
			r.errf(f.v, f.path, "%s is a %s call: telling a person sends something, so it is an %s", name, Read, Update)
		default:
			n.Call, c = name, found
		}
	}
	for _, f := range fields {
		switch f.key {
		case "address":
			addressed = true
			var hasParam, hasEnv bool
			for _, g := range r.entries(f.v, f.path, "param", "env") {
				switch g.key {
				case "param":
					hasParam = true
					n.Address = param(g)
				case "env":
					hasEnv = true
					if s, ok := r.str(g.v, g.path); ok && !paramName.MatchString(s) {
						r.errf(g.v, g.path, "%q is no name of a variable: this is the name the address is under in a user's env file, never the address", s)
					} else if ok {
						n.AddressEnv = s
					}
				}
			}
			if f.v.Kind == yaml.MappingNode && !hasParam {
				r.errf(f.v, join(f.path, "param"), "missing: the parameter that takes where the person is reached")
			}
			if f.v.Kind == yaml.MappingNode && !hasEnv {
				r.errf(f.v, join(f.path, "env"), "missing: the name of the variable the address is in, in a user's env file")
			}
		case "title":
			n.Title = param(f)
		case "text":
			n.Text = param(f)
		case "link":
			n.Link = param(f)
			if p, ok := c.param(n.Link); ok && !p.Optional {
				r.errf(f.v, f.path, "the parameter %s is required, and most of what the yard tells has no link: make it optional: true", n.Link)
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return n
	}
	if !called {
		r.errf(e.v, join(e.path, "call"), "missing: the call that tells a person")
	}
	if !addressed {
		r.errf(e.v, join(e.path, "address"), "missing: { param: <the parameter that takes where the person is reached>, env: <its name in a user's env file> }")
	}
	if !slices.ContainsFunc(fields, func(f entry) bool { return f.key == "title" || f.key == "text" }) {
		r.errf(e.v, e.path, "neither title nor text: the call is given nothing to say")
	}
	if c != nil {
		for _, p := range c.Params {
			if !p.Optional && fills[p.Name] == "" {
				r.errf(e.v, e.path, "the call %s needs the parameter %s, and nothing here fills it (address, title, text and link are what the yard has)", c.Name, p.Name)
			}
		}
	}
	return n
}

// hear reads the key that makes a source one that hears only the
// addresses its users' env files set. What it refuses is what would fail
// at every delivery, or hear nothing: a key beside field, address and
// from, a field the envelope carries under no key the file names, an
// address that is not the one telling reads, and a from that is no role
// the yard has. Without a hook key there is no delivery to hear, and
// without a notify key no address is named to match, so with neither is
// hear refused too.
func (r *reader) hear(d *Declaration, e entry) *Hear {
	h := &Hear{}
	var field, address, env *entry
	for _, f := range r.entries(e.v, e.path, "field", "address", "from") {
		switch f.key {
		case "field":
			field = &f
			if s, ok := r.str(f.v, f.path); ok && !hookKey.MatchString(s) {
				r.errf(f.v, f.path, "%q: a field is lowercase letters, digits and _, starting with a letter or digit (chat)", s)
			} else if ok {
				h.Field = s
			}
		case "address":
			address = &f
			for _, g := range r.entries(f.v, f.path, "env") {
				env = &g
				if s, ok := r.str(g.v, g.path); ok && !paramName.MatchString(s) {
					r.errf(g.v, g.path, "%q is no name of a variable: this is the name the address is under in a user's env file, never the address", s)
				} else if ok {
					h.Address.Env = s
				}
			}
		case "from":
			if s, ok := r.str(f.v, f.path); ok && s != HearFromOperator {
				r.errf(f.v, f.path, "%q: want %s", s, HearFromOperator)
			} else if ok {
				h.From = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return h
	}
	if field == nil {
		r.errf(e.v, join(e.path, "field"), "missing: the envelope field that must hold a user's address (chat)")
	}
	if address == nil {
		r.errf(e.v, join(e.path, "address"), "missing: { env: <the name the address is under in a user's env file> }")
	} else if address.v.Kind == yaml.MappingNode && env == nil {
		r.errf(address.v, join(address.path, "env"), "missing: the name of the variable the address is in, in a user's env file")
	}
	if d.Hook == nil {
		r.errf(e.v, e.path, "needs hook: hear keeps the deliveries of a source, and this file has no hook key to receive any")
	} else if field != nil && h.Field != "" && !slices.Contains(d.Hook.Keys, h.Field) {
		r.errf(field.v, field.path, "%s is no key the envelope carries: hook.keys names %s", h.Field, or(strings.Join(d.Hook.Keys, ", "), "none"))
	}
	if d.Notify == nil {
		r.errf(e.v, e.path, "needs notify: hear keeps the deliveries from an address the notify key names, and this file has no notify key to name one")
	} else if env != nil && h.Address.Env != "" && h.Address.Env != d.Notify.AddressEnv {
		r.errf(env.v, env.path, "%s is not the address telling reads: notify.address.env names %s, and hear reads the same file", h.Address.Env, d.Notify.AddressEnv)
	}
	return h
}
