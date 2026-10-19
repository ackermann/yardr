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

// Hear says which deliveries of the source the yard keeps. By address
// (the spelling without by) a delivery is kept when its envelope field
// holds an address a user's env file sets, whatever the user is told
// through. By identity (by: identity) a delivery is kept when its
// envelope field holds the id of some user's identity in this source.
// A delivery from anything else is dropped after verification:
// answered 200, recorded nowhere, and counted in sums beside refusals.
type Hear struct {
	// Field is the envelope field that must hold a user's address, or,
	// by identity, a speaker's id in this source.
	Field string `json:"field"`
	// By is how Field is matched: HearByIdentity, or "" for by address.
	By string `json:"by,omitempty"`
	// Address is where that address is read from: the variable of the
	// user's own env file, the same one telling reads. It applies to
	// hearing by address alone, and is refused with by: identity.
	Address HearAddress `json:"address,omitempty"`
	// From narrows whom the source hears by address: HearFromOperator,
	// or "" for every user whose env file sets the address. It is
	// refused with by: identity.
	From string `json:"from,omitempty"`
	// Subscribe is the filter a claim approval subscribes the yard's
	// hearer with beside the claimed address: key -> value, as a
	// subscription's --where (type message, so a chat's other Updates
	// are not heard). The claim sets Field to the address itself, so
	// this names only the filter beside it.
	Subscribe map[string]string `json:"subscribe,omitempty"`
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

// HearByIdentity is the one by a hear key takes: the envelope field
// holds a speaker's id in this source, matched against every user's
// identity in it (User.IsIn), rather than an address in an env file.
const HearByIdentity = "identity"

// ByIdentity reports whether this hear matches by identity.
func (h *Hear) ByIdentity() bool { return h != nil && h.By == HearByIdentity }

// HearsByIdentity reports whether the declaration hears by identity.
func (d *Declaration) HearsByIdentity() bool { return d != nil && d.Hear.ByIdentity() }

// HearsChat reports whether the source hears a person's chat back: a hear
// key matched by address, rather than by identity (a machine source's
// speakers) or no hear key (every delivery kept). Such a hook has no
// steady rhythm to time silence by, so the quiet check judges its
// webhook's health instead of its gap.
func (d *Declaration) HearsChat() bool {
	return d != nil && d.Hear != nil && !d.Hear.ByIdentity()
}

// ClaimAddress is the address a delivery from no known user may claim:
// the envelope field that holds it and the variable it is written under
// in a user's env file. It is the hear key's field with the notify
// address's variable (hear is refused unless the two agree), read from
// the declaration alone and never from service-specific code. ok is
// false for a source that hears nothing back: such a source drops no
// delivery for an unknown address, so it offers no claim.
func (d *Declaration) ClaimAddress() (field, env string, ok bool) {
	if d == nil || d.Hear == nil || d.Hear.Field == "" || d.Hear.ByIdentity() {
		return "", "", false
	}
	env = d.Hear.Address.Env
	if d.Notify != nil && d.Notify.AddressEnv != "" {
		env = d.Notify.AddressEnv
	}
	if env == "" {
		return "", "", false
	}
	return d.Hear.Field, env, true
}

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
		case len(found.Build) > 0:
			r.errf(f.v, f.path, "%s names build: telling a person sends the request's template and runs no build command", name)
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

// hear reads the key that makes a source one that hears only some
// deliveries: by address, those from an address its users' env files
// set; by identity, those whose speaker a user is mapped to. What it
// refuses is what would fail at every delivery, or hear nothing: a key
// beside field, by, address, from and subscribe, a by that is no way
// the yard hears, a field the envelope carries under no key the file
// names, by: identity with an address or a from, an address that is
// not the one telling reads, a from that is no role the yard has, and
// a subscribe that is no filter or names the field the claim sets
// itself. Without a hook key there is no delivery to hear; hearing by
// address without a notify key names no address to match, so it is
// refused too, while hearing by identity needs no notify key.
func (r *reader) hear(d *Declaration, e entry) *Hear {
	h := &Hear{}
	var field, address, env, subscribe, fromEntry, byEntry *entry
	for _, f := range r.entries(e.v, e.path, "field", "by", "address", "from", "subscribe") {
		switch f.key {
		case "field":
			field = &f
			if s, ok := r.str(f.v, f.path); ok && !hookKey.MatchString(s) {
				r.errf(f.v, f.path, "%q: a field is lowercase letters, digits and _, starting with a letter or digit (chat)", s)
			} else if ok {
				h.Field = s
			}
		case "by":
			byEntry = &f
			if s, ok := r.str(f.v, f.path); ok && s != HearByIdentity {
				r.errf(f.v, f.path, "%q: want %s", s, HearByIdentity)
			} else if ok {
				h.By = s
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
			fromEntry = &f
			if s, ok := r.str(f.v, f.path); ok && s != HearFromOperator {
				r.errf(f.v, f.path, "%q: want %s", s, HearFromOperator)
			} else if ok {
				h.From = s
			}
		case "subscribe":
			subscribe = &f
			for _, g := range r.entries(f.v, f.path) {
				v, ok := r.str(g.v, g.path)
				if !ok {
					continue
				}
				if !hookKey.MatchString(g.key) {
					r.errf(g.k, g.path, "%q: a filter key is lowercase letters, digits and _, starting with a letter or digit (type)", g.key)
					continue
				}
				if h.Subscribe == nil {
					h.Subscribe = map[string]string{}
				}
				h.Subscribe[g.key] = v
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return h
	}
	if field == nil {
		r.errf(e.v, join(e.path, "field"), "missing: the envelope field that must hold a user's address (chat)")
	}
	byIdentity := h.By == HearByIdentity
	if byIdentity {
		if address != nil {
			r.errf(address.v, address.path, "is for hearing by address, and by: identity hears by who a speaker is: leave address out with by: identity")
		}
		if fromEntry != nil {
			r.errf(fromEntry.v, fromEntry.path, "is for hearing by address, and by: identity hears by who a speaker is: leave from out with by: identity")
		}
	} else if byEntry == nil || h.By == "" {
		// Hearing by address, the standing spelling: an invalid by has
		// already been refused above, and still names no other way.
		if address == nil {
			r.errf(e.v, join(e.path, "address"), "missing: { env: <the name the address is under in a user's env file> }")
		} else if address.v.Kind == yaml.MappingNode && env == nil {
			r.errf(address.v, join(address.path, "env"), "missing: the name of the variable the address is in, in a user's env file")
		}
	}
	if d.Hook == nil {
		r.errf(e.v, e.path, "needs hook: hear keeps the deliveries of a source, and this file has no hook key to receive any")
	} else if field != nil && h.Field != "" && !slices.Contains(d.Hook.Keys, h.Field) {
		r.errf(field.v, field.path, "%s is no key the envelope carries: hook.keys names %s", h.Field, or(strings.Join(d.Hook.Keys, ", "), "none"))
	}
	if !byIdentity {
		if d.Notify == nil {
			r.errf(e.v, e.path, "needs notify: hear keeps the deliveries from an address the notify key names, and this file has no notify key to name one")
		} else if env != nil && h.Address.Env != "" && h.Address.Env != d.Notify.AddressEnv {
			r.errf(env.v, env.path, "%s is not the address telling reads: notify.address.env names %s, and hear reads the same file", h.Address.Env, d.Notify.AddressEnv)
		}
	}
	if subscribe != nil && h.Field != "" {
		if _, ok := h.Subscribe[h.Field]; ok {
			r.errf(subscribe.v, join(subscribe.path, h.Field), "%s is the field the claim sets to the claimed address: subscribe names only the filter beside it", h.Field)
		}
	}
	return h
}
