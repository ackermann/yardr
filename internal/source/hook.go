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
	// HookScheme is a scheme of hook.verify: HMAC-SHA256 over the body.
	HookScheme = "hmac-sha256"
	// HookSecret is a scheme of hook.verify: the header holds the hook's
	// secret itself, compared as it arrives.
	HookSecret = "secret"
	// HookSigned is what the signature covers: the raw body.
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
	// Normalize turns a verified body into the envelope: in {headers, body},
	// out the envelope. It is handed no credential.
	Normalize []string `json:"normalize"`
	// Keys are the keys its events' fields may carry, which a
	// subscription's --where and a pack's says may then name beside
	// store.WhereKeys.
	Keys []string `json:"keys,omitempty"`
}

// HookVerify is how the core proves a delivery came from the source, from
// the request's headers and the raw body, using the hook's secret. The
// signing secret reaches no command, and an unverified body reaches none.
type HookVerify struct {
	// Scheme is how a delivery is verified: hmac-sha256, or secret, the
	// header holding the hook's secret itself. Signed is what the
	// signature covers: body, for hmac-sha256 alone.
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
	var verify, bodies, normalize *entry
	var deliveryNamed bool
	for _, f := range r.entries(e.v, e.path, "verify", "bodies", "normalize", "keys") {
		switch f.key {
		case "verify":
			verify = &f
			h.Verify, deliveryNamed = r.hookVerify(f)
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
	return h
}

// hookVerify reads how the core proves a delivery came from the source:
// HMAC-SHA256 over the body, or the secret itself in a header. Signed,
// encoding and prefix are for the signature alone; the delivery id is in a
// header or in the body, and a service that sends none says no delivery
// with scheme secret. It reports whether a delivery was named, for the
// bodies check in hook.
func (r *reader) hookVerify(e entry) (HookVerify, bool) {
	v := HookVerify{}
	var scheme, signed, signature, delivery *entry
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
	} else if s, ok := r.str(signed.v, signed.path); ok && s != HookSigned {
		r.errf(signed.v, signed.path, "%q: the signature covers %s", s, HookSigned)
	} else if ok {
		v.Signed = s
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
