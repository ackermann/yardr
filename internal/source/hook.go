package source

import (
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// The bodies of a hook key: how far a body identifies a delivery.
const (
	// BodiesUnique: no two deliveries from the source share a body
	// (a source that signs a timestamp into each).
	BodiesUnique = "unique"
	// BodiesUniquePerHook: no two deliveries to one hook share a body,
	// but one event's body goes to every hook that asked for it.
	BodiesUniquePerHook = "unique-per-hook"
	// BodiesRepeat: a body may come again as a new delivery.
	BodiesRepeat = "repeat"
)

// bodiesKinds are the values a hook's bodies takes, in the order the
// reader names them.
var bodiesKinds = []string{BodiesUnique, BodiesUniquePerHook, BodiesRepeat}

// The ways the core verifies a delivery of a declared source.
const (
	// HookScheme is a scheme of hook.verify: HMAC-SHA256 over what
	// signed names.
	HookScheme = "hmac-sha256"
	// HookSecret is a scheme of hook.verify: the header holds the hook's
	// secret itself, compared as it arrives.
	HookSecret = "secret"
	// HookSigned is what the signature covers when it covers the body
	// alone: the word body.
	HookSigned = "body"
	// HookEncoding is how the signature arrives: hex.
	HookEncoding = "hex"
)

// hookSchemes are the values hook.verify.scheme takes, in the order the
// reader names them.
var hookSchemes = []string{HookScheme, HookSecret}

// The units of hook.verify.timestamp: milliseconds or seconds since the
// epoch, as the service counts them.
const (
	HookMs = "ms"
	HookS  = "s"
)

// Hook is what the source sends in: the data the core verifies each
// delivery from, the command that turns a verified body into the envelope,
// and the keys its events' fields may carry.
type Hook struct {
	Verify HookVerify `json:"verify"`
	// Bodies is how far a body identifies a delivery: BodiesUnique,
	// BodiesUniquePerHook or BodiesRepeat.
	Bodies string `json:"bodies"`
	// Challenge is the service's verification handshake, answered by the
	// receiver from a verified body before it normalizes or records
	// anything, nil for a service that sends none.
	Challenge *HookChallenge `json:"challenge,omitempty"`
	// Normalize turns a verified body into the envelope: in {headers, body},
	// out the envelope. It is handed no credential.
	Normalize []string `json:"normalize"`
	// Keys are the keys its events' fields may carry, which a
	// subscription's --where and a pack's says may then name beside
	// store.WhereKeys.
	Keys []string `json:"keys,omitempty"`
	// Register is the call the yard makes to tell the service where to
	// send deliveries, nil for a source the operator registers by hand.
	// It is used only with scheme secret for now: the yard makes the
	// secret, and a signature the service makes is a later bead.
	Register *HookRegister `json:"register,omitempty"`
}

// RegistersSecret reports whether the yard registers this hook itself
// with a secret it makes: a register call with scheme secret. With a
// signature the service makes the operator still gives the secret, and
// registering is a later bead, so this is false there.
func (h *Hook) RegistersSecret() bool {
	return h != nil && h.Register != nil && h.Verify.Scheme == HookSecret
}

// HookRegister is how the yard registers a hook with the service: the
// update call it makes, and which of that call's parameters take the
// webhook's public URL and, with scheme secret, the secret the yard
// makes. Any other parameter of the call is fixed in its body or
// optional, as the file says.
type HookRegister struct {
	Call string `json:"call"`
	// URL is the parameter that takes the webhook's public URL
	// (hook_public_url plus the hook's name).
	URL string `json:"url"`
	// Secret is the parameter that takes the hook's secret, with
	// scheme secret; "" for a call that takes none.
	Secret string `json:"secret,omitempty"`
}

// HookChallenge is a service's verification handshake, as data: a
// verified body whose field at Body holds Value is answered at once with
// a JSON object naming its Answer field and that field's value, before
// normalize, dedupe or recording. The three name what the service sends
// and expects back, in its own words: for Slack, body type, value
// url_verification, answer challenge.
type HookChallenge struct {
	// Body is the dot path into the body of the field that names the
	// handshake (type).
	Body string `json:"body"`
	// Value is what that field holds for a handshake (url_verification).
	Value string `json:"value"`
	// Answer is the dot path into the body of the field the answer names
	// and carries (challenge).
	Answer string `json:"answer"`
}

// HookVerify is how the core proves a delivery came from the source, from
// the request's headers and the raw body, using the hook's secret. The
// signing secret reaches no command, and an unverified body reaches none.
type HookVerify struct {
	// Scheme is how a delivery is verified: hmac-sha256, or secret, the
	// header holding the hook's secret itself. Signed is what the
	// signature covers, for hmac-sha256 alone: the word body for the raw
	// body alone, or a template over {timestamp} and {body}, each named
	// once, with the literal text around them the service's
	// (v0:{timestamp}:{body}).
	Scheme string `json:"scheme"`
	Signed string `json:"signed"`
	// Signature is the header the signature arrives in, in hex, past the
	// prefix ("sha256=", or none); with scheme secret it is the header
	// the secret arrives in, compared as it is.
	Signature HookSignature `json:"signature"`
	// Delivery is where the service's id of the delivery arrives: in a
	// header, or in the body at the dot path. Empty with scheme secret
	// for a service that sends none.
	Delivery HookDelivery `json:"delivery"`
	// Timestamp is when the delivery was sent, checked after the
	// signature, so it is one the sender signed. Nil for a service that
	// sends none, whose deliveries cannot be told from a replay by age.
	Timestamp *HookTimestamp `json:"timestamp,omitempty"`
}

// HookSignature is where a delivery's signature arrives.
type HookSignature struct {
	Header string `json:"header"`
	// Encoding is how it arrives: hex. For hmac-sha256 alone.
	Encoding string `json:"encoding"`
	// Prefix is what stands before it ("sha256="), "" for none. For
	// hmac-sha256 alone.
	Prefix string `json:"prefix,omitempty"`
}

// HookDelivery is where the service's id of a delivery arrives: in a
// header, or in the body at the dot path. One of the two; both empty for
// a service that sends none, with scheme secret.
type HookDelivery struct {
	Header string `json:"header,omitempty"`
	Body   string `json:"body,omitempty"`
}

// HookTimestamp is when a delivery was sent: in the body at the dot path
// (webhookTimestamp), or in a header, in ms or s, within window of the
// yard's clock.
type HookTimestamp struct {
	Body   string `json:"body,omitempty"`
	Header string `json:"header,omitempty"`
	Unit   string `json:"unit"`
	Window string `json:"window"`
}

// Events is what the source sends in about pull requests and CI, and the
// commands that read them back. Runs are the envelope types that are CI
// runs, judged against the pull request's head; HeadFrom the types whose
// head_sha becomes that head. PR reads the pull request live, for the
// head no event has told; CI reads the CI state of one commit of it.
type Events struct {
	// Runs are the envelope types that are CI runs, whatever became of
	// them. A run counts for the pull request's head only.
	Runs []string `json:"runs,omitempty"`
	// HeadFrom are the envelope types that tell the pull request's head
	// commit: an event about the pull request itself, or a review of it.
	HeadFrom []string `json:"head_from,omitempty"`
	// PR reads the pull request live: in {repo, number, url}, out
	// {head, base, state}. It is handed the credential under
	// credential.env, as a thread command is.
	PR []string `json:"pr,omitempty"`
	// CI reads CI state: in {repo, number, sha}, out {head, base, state,
	// required, checks}, the checks in the tongue the core judges
	// (docs/integrations.md). Handed the credential as PR is.
	CI []string `json:"ci,omitempty"`
}

// hook reads what the source sends in: how the core verifies its
// webhooks, the command that turns a verified body into the envelope, and
// the keys its events' fields may carry. Refused is what would fail at
// every delivery: no way to verify one, a command that is no list of
// strings or has no program, a timestamp in two places or none, a delivery
// id in two places, and bodies that may come again with no delivery id to
// tell a retry by.
func (r *reader) hook(e entry) *Hook {
	h := &Hook{}
	var verify, bodies, normalize, register *entry
	var deliveryNamed bool
	for _, f := range r.entries(e.v, e.path, "verify", "bodies", "challenge", "normalize", "keys", "register") {
		switch f.key {
		case "verify":
			verify = &f
			h.Verify, deliveryNamed = r.hookVerify(f)
		case "challenge":
			h.Challenge = r.hookChallenge(f)
		case "bodies":
			bodies = &f
			if s, ok := r.str(f.v, f.path); ok && !slices.Contains(bodiesKinds, s) {
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(bodiesKinds, ", "))
			} else if ok {
				h.Bodies = s
			}
		case "normalize":
			normalize = &f
			h.Normalize = r.command(f, "linear/normalize")
		case "keys":
			for _, s := range r.strs(f.v, f.path) {
				if !hookKey.MatchString(s) {
					r.errf(f.v, f.path, "%q: a key is lowercase letters, digits and _, starting with a letter or digit (team)", s)
					continue
				}
				h.Keys = append(h.Keys, s)
			}
		case "register":
			register = &f
			h.Register = r.hookRegister(f)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return h
	}
	if verify == nil {
		r.errf(e.v, join(e.path, "verify"), "missing: how the core verifies a delivery (scheme, signature, delivery)")
	}
	if bodies == nil {
		r.errf(e.v, join(e.path, "bodies"), "missing: %s", strings.Join(bodiesKinds, ", "))
	}
	if normalize == nil {
		r.errf(e.v, join(e.path, "normalize"), "missing: the command that turns a verified body into the envelope")
	}
	// A body that may come again is told from its retry by the service's
	// id alone. With hmac-sha256 a delivery is always named, so this is
	// for scheme secret, whose delivery is optional; a delivery that is
	// named but wrong is its own error, not this one.
	if bodies != nil && h.Bodies == BodiesRepeat && h.Verify.Scheme == HookSecret && !deliveryNamed {
		r.errf(bodies.v, bodies.path, "repeat: needs verify.delivery: without the service's id of the delivery a retry is a new delivery")
	}
	if register != nil && h.Register != nil && h.Verify.Scheme == HookSecret && h.Register.Secret == "" {
		r.errf(register.v, join(register.path, "secret"), "missing: the parameter that takes the hook's secret, which the yard makes with scheme %s", HookSecret)
	}
	if register != nil && h.Register != nil && h.Verify.Scheme != HookSecret {
		r.errf(register.v, register.path, "registering a hook is supported only with verify.scheme %s; a service-made signature still takes a typed secret", HookSecret)
	}
	return h
}

// hookRegister reads how the yard registers a hook: the call it makes
// and which of its parameters take the public URL and the secret. That
// the call is there and is an update, and that the parameters are its
// own, is checked with the calls read (hookRegisterCheck in
// declaration.go), since hook may stand before calls in the file.
func (r *reader) hookRegister(e entry) *HookRegister {
	reg := &HookRegister{}
	var call, urlParam *entry
	for _, f := range r.entries(e.v, e.path, "call", "url", "secret") {
		switch f.key {
		case "call":
			call = &f
			if s, ok := r.str(f.v, f.path); ok {
				reg.Call = s
			}
		case "url":
			urlParam = &f
			if s, ok := r.str(f.v, f.path); ok {
				reg.URL = s
			}
		case "secret":
			if s, ok := r.str(f.v, f.path); ok {
				reg.Secret = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return reg
	}
	if call == nil {
		r.errf(e.v, join(e.path, "call"), "missing: the call that registers the webhook")
	}
	if urlParam == nil {
		r.errf(e.v, join(e.path, "url"), "missing: the parameter that takes the webhook's public URL")
	}
	return reg
}

// hookVerify reads how the core proves a delivery came from the source:
// HMAC-SHA256 over the body, or the secret itself in a header. Signed,
// encoding and prefix are for the signature alone; the delivery id is in a
// header or in the body, and a service that sends none says no delivery
// with scheme secret. It reports whether a delivery was named, for the
// bodies check in hook.
func (r *reader) hookVerify(e entry) (HookVerify, bool) {
	v := HookVerify{}
	var scheme, signed, signature, delivery, timestamp *entry
	for _, f := range r.entries(e.v, e.path, "scheme", "signed", "signature", "delivery", "timestamp") {
		switch f.key {
		case "scheme":
			scheme = &f
			if s, ok := r.str(f.v, f.path); ok && !slices.Contains(hookSchemes, s) {
				r.errf(f.v, f.path, "%q: want %s", s, strings.Join(hookSchemes, ", "))
			} else if ok {
				v.Scheme = s
			}
		case "signed":
			signed = &f
		case "signature":
			signature = &f
		case "delivery":
			delivery = &f
		case "timestamp":
			timestamp = &f
			v.Timestamp = r.hookTimestamp(f)
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return v, delivery != nil
	}
	// Signed, signature and delivery are read after the scheme is known,
	// since what they may say depends on it.
	secret := v.Scheme == HookSecret
	if scheme == nil {
		r.errf(e.v, join(e.path, "scheme"), "missing: how a delivery is verified (%s)", strings.Join(hookSchemes, ", "))
	}
	if signed == nil {
		if !secret {
			r.errf(e.v, join(e.path, "signed"), "missing: what the signature covers (%s)", HookSigned)
		}
	} else if secret {
		r.errf(signed.v, signed.path, "is for scheme %s: with %s the header holds the secret itself", HookScheme, HookSecret)
	} else if s, ok := r.str(signed.v, signed.path); ok {
		if signed, ok := r.signedTemplate(*signed, s, timestamp != nil); ok {
			v.Signed = signed
		}
	}
	if signature == nil {
		if secret {
			r.errf(e.v, join(e.path, "signature"), "missing: the header the secret arrives in ({ header })")
		} else {
			r.errf(e.v, join(e.path, "signature"), "missing: the header the signature arrives in ({ header, encoding, prefix })")
		}
	} else {
		v.Signature = r.hookSignature(*signature, secret)
	}
	if delivery == nil {
		if !secret {
			r.errf(e.v, join(e.path, "delivery"), "missing: where the service's id of the delivery arrives (header: its name, or body: a dot path into it)")
		}
	} else {
		v.Delivery = r.hookDelivery(*delivery)
	}
	return v, delivery != nil
}

// signedTemplate reads what a signature covers: the word body for the
// body alone, or a template over {timestamp} and {body} with the
// service's literal text around them (v0:{timestamp}:{body}). A
// template names {timestamp} and {body} once each, names no other
// placeholder, and names {timestamp} only where verify names a
// timestamp to read it from. One mistake is one line, as elsewhere.
func (r *reader) signedTemplate(e entry, s string, hasTimestamp bool) (string, bool) {
	if s == HookSigned {
		return s, true
	}
	counts := map[string]int{}
	for rest := s; rest != ""; {
		open := strings.Index(rest, "{")
		stray := strings.Index(rest, "}")
		if open == -1 {
			if stray != -1 {
				r.errf(e.v, e.path, "%q: unknown placeholder %q: a signed template names {body} and {timestamp} alone", s, rest[stray:])
				return "", false
			}
			break
		}
		if stray != -1 && stray < open {
			r.errf(e.v, e.path, "%q: unknown placeholder %q: a signed template names {body} and {timestamp} alone", s, rest[stray:open])
			return "", false
		}
		end := strings.Index(rest[open:], "}")
		if end == -1 {
			r.errf(e.v, e.path, "%q: unknown placeholder %q: a signed template names {body} and {timestamp} alone", s, rest[open:])
			return "", false
		}
		name := rest[open+1 : open+end]
		if name != "body" && name != "timestamp" {
			r.errf(e.v, e.path, "%q: unknown placeholder {%s}: a signed template names {body} and {timestamp} alone", s, name)
			return "", false
		}
		counts[name]++
		rest = rest[open+end+1:]
	}
	switch {
	case counts["body"] > 1:
		r.errf(e.v, e.path, "%q: repeated {body}: a signed template names {timestamp} and {body} once each", s)
		return "", false
	case counts["timestamp"] > 1:
		r.errf(e.v, e.path, "%q: repeated {timestamp}: a signed template names {timestamp} and {body} once each", s)
		return "", false
	case counts["body"] == 0:
		r.errf(e.v, e.path, "%q: missing {body}: a signed template names {timestamp} and {body} once each", s)
		return "", false
	case counts["timestamp"] == 0:
		r.errf(e.v, e.path, "%q: missing {timestamp}: a signed template names {timestamp} and {body} once each", s)
		return "", false
	case counts["timestamp"] == 1 && !hasTimestamp:
		r.errf(e.v, e.path, "%q: {timestamp} with no timestamp: a signed template naming {timestamp} needs verify.timestamp", s)
		return "", false
	}
	return s, true
}

// hookChallenge reads a service's verification handshake: the field of
// the body that names it, the value that field holds for it, and the
// field the answer names and carries. Each is what would fail at every
// handshake when missing or no dot path into the body.
func (r *reader) hookChallenge(e entry) *HookChallenge {
	c := &HookChallenge{}
	var body, value, answer *entry
	for _, f := range r.entries(e.v, e.path, "body", "value", "answer") {
		switch f.key {
		case "body":
			body = &f
			if s, ok := r.str(f.v, f.path); ok && !hookPath(s) {
				r.errf(f.v, f.path, "%q: a dot path into the body (type)", s)
			} else if ok {
				c.Body = s
			}
		case "value":
			value = &f
			if s, ok := r.str(f.v, f.path); ok {
				c.Value = s
			}
		case "answer":
			answer = &f
			if s, ok := r.str(f.v, f.path); ok && !hookPath(s) {
				r.errf(f.v, f.path, "%q: a dot path into the body (challenge)", s)
			} else if ok {
				c.Answer = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return c
	}
	if body == nil {
		r.errf(e.v, join(e.path, "body"), "missing: the field of the body that names a challenge (type)")
	}
	if value == nil {
		r.errf(e.v, join(e.path, "value"), "missing: the value that field holds for a challenge (url_verification)")
	}
	if answer == nil {
		r.errf(e.v, join(e.path, "answer"), "missing: the field of the body the answer names (challenge)")
	}
	return c
}

// hookSignature reads where a delivery's signature arrives: the header, in
// hex, past the prefix. With scheme secret the header holds the secret
// itself, and encoding and prefix do not apply.
func (r *reader) hookSignature(e entry, secret bool) HookSignature {
	s := HookSignature{}
	var header, encoding *entry
	for _, f := range r.entries(e.v, e.path, "header", "encoding", "prefix") {
		switch f.key {
		case "header":
			header = &f
			if name, ok := r.str(f.v, f.path); ok && !headerName.MatchString(name) {
				r.errf(f.v, f.path, "%q is no name of a header: this is the header the %s arrives in", name, signedWhat(secret))
			} else if ok {
				s.Header = name
			}
		case "encoding":
			encoding = &f
			if secret {
				r.errf(f.v, f.path, "is for scheme %s: with %s the header holds the secret itself", HookScheme, HookSecret)
			} else if enc, ok := r.str(f.v, f.path); ok && enc != HookEncoding {
				r.errf(f.v, f.path, "%q: a signature arrives %s", enc, HookEncoding)
			} else if ok {
				s.Encoding = enc
			}
		case "prefix":
			if secret {
				r.errf(f.v, f.path, "is for scheme %s: with %s the header holds the secret itself", HookScheme, HookSecret)
			} else if f.v.Kind == yaml.ScalarNode && f.v.Tag == "!!str" {
				// An explicit "" is no prefix, as leaving it out is.
				s.Prefix = f.v.Value
			} else {
				r.errf(f.v, f.path, "want a string, got %s", describe(f.v))
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return s
	}
	if header == nil {
		if secret {
			r.errf(e.v, join(e.path, "header"), "missing: the header the secret arrives in")
		} else {
			r.errf(e.v, join(e.path, "header"), "missing: the header the signature arrives in (Linear-Signature)")
		}
	}
	if encoding == nil && !secret {
		r.errf(e.v, join(e.path, "encoding"), "missing: how the signature arrives (%s)", HookEncoding)
	}
	return s
}

// signedWhat is what the signature header carries under the scheme: the
// signature, or the secret itself.
func signedWhat(secret bool) string {
	if secret {
		return "secret"
	}
	return "signature (Linear-Signature)"
}

// hookDelivery reads where the service's id of a delivery arrives: in a
// header, or in the body at the dot path.
func (r *reader) hookDelivery(e entry) HookDelivery {
	d := HookDelivery{}
	var header, body *entry
	for _, f := range r.entries(e.v, e.path, "header", "body") {
		switch f.key {
		case "header":
			header = &f
			if name, ok := r.str(f.v, f.path); ok && !headerName.MatchString(name) {
				r.errf(f.v, f.path, "%q is no name of a header: this is the header the service's id of the delivery arrives in (Linear-Delivery)", name)
			} else if ok {
				d.Header = name
			}
		case "body":
			body = &f
			if s, ok := r.str(f.v, f.path); ok && !hookPath(s) {
				r.errf(f.v, f.path, "%q: a dot path into the body (update_id)", s)
			} else if ok {
				d.Body = s
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return d
	}
	if header != nil && body != nil {
		r.errf(header.v, header.path, "a delivery id is in a header or in the body, not both")
	}
	if header == nil && body == nil {
		r.errf(e.v, join(e.path, "header"), "missing: where the service's id of the delivery arrives (header: its name, or body: a dot path into it)")
	}
	return d
}

// hookTimestamp reads when a delivery was sent: in the body at the dot
// path, or in a header, in ms or s, within window of the yard's clock.
func (r *reader) hookTimestamp(e entry) *HookTimestamp {
	t := &HookTimestamp{}
	var body, header, unit, window *entry
	for _, f := range r.entries(e.v, e.path, "body", "header", "unit", "window") {
		switch f.key {
		case "body":
			body = &f
			if s, ok := r.str(f.v, f.path); ok && !hookPath(s) {
				r.errf(f.v, f.path, "%q: a dot path into the body (webhookTimestamp)", s)
			} else if ok {
				t.Body = s
			}
		case "header":
			header = &f
			if name, ok := r.str(f.v, f.path); ok && !headerName.MatchString(name) {
				r.errf(f.v, f.path, "%q is no name of a header: this is the header the timestamp arrives in", name)
			} else if ok {
				t.Header = name
			}
		case "unit":
			unit = &f
			if s, ok := r.str(f.v, f.path); ok && s != HookMs && s != HookS {
				r.errf(f.v, f.path, "%q: want %s or %s", s, HookMs, HookS)
			} else if ok {
				t.Unit = s
			}
		case "window":
			window = &f
			if s, ok := r.str(f.v, f.path); ok {
				if d, err := time.ParseDuration(s); err != nil || d <= 0 {
					r.errf(f.v, f.path, "%q: a window is a positive duration (1m)", s)
				} else {
					t.Window = s
				}
			}
		}
	}
	if e.v.Kind != yaml.MappingNode {
		return t
	}
	if body != nil && header != nil {
		r.errf(header.v, header.path, "a timestamp is in the body or in a header, not both")
	}
	if body == nil && header == nil {
		r.errf(e.v, join(e.path, "body"), "missing: where the timestamp arrives (body: a dot path into it, or header: its name)")
	}
	if unit == nil {
		r.errf(e.v, join(e.path, "unit"), "missing: %s or %s", HookMs, HookS)
	}
	if window == nil {
		r.errf(e.v, join(e.path, "window"), "missing: how far the timestamp may be from now (1m)")
	}
	return t
}

// events reads the keys that say what the source sends in about pull
// requests and CI, and the commands that read them back. Each stands
// alone: runs and head_from name envelope types without any command, and
// either command reads without the other. Refused is what would fail at
// every read: a type list that is no list of strings, a command that is
// no list of strings or has no program.
func (r *reader) events(e entry) *Events {
	ev := &Events{}
	for _, f := range r.entries(e.v, e.path, "runs", "head_from", "pr", "ci") {
		switch f.key {
		case "runs":
			ev.Runs = r.strs(f.v, f.path)
		case "head_from":
			ev.HeadFrom = r.strs(f.v, f.path)
		case "pr":
			ev.PR = r.command(f, "events/pr")
		case "ci":
			ev.CI = r.command(f, "events/ci")
		}
	}
	return ev
}

// hookPath reports whether s is a dot path into a JSON body: segments with
// something between the dots.
func hookPath(s string) bool {
	segs := strings.Split(s, ".")
	for _, seg := range segs {
		if seg == "" {
			return false
		}
	}
	return len(segs) > 0
}
