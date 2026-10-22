package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// AdapterFor is the adapter for source: the one built from d, the
// yard's declared source of that name, when it receives webhooks (a
// hook key), else the builtin one of that name. When the yard declares
// the same name, that declaration replaces the builtin even if it lacks
// a hook key. d is nil when the yard declares no such source.
// home is the yard's home, which a relative command program runs from;
// it is only read from the adapter returned, so a check for whether a
// source has an adapter passes "". ok is false for a source yardr has
// nothing for.
func AdapterFor(name string, d *source.Declaration, home string) (Adapter, bool) {
	if dec, ok := declared(name, d, home, ""); ok {
		if dec.D.Hook != nil {
			return dec, true
		}
		return nil, false
	}
	if ad, ok := Adapters[name]; ok {
		return ad, true
	}
	return nil, false
}

// declared is the adapter built from d, the yard's declared source of that
// name. It is the one way every lookup finds a declared source: AdapterFor
// and each of ReplierFor, OpenerFor, StaterFor and ForwarderFor add the
// keys their interface takes, and a declared source without them lacks the
// interface, as an adapter in Go may.
func declared(name string, d *source.Declaration, home, mint string) (Declared, bool) {
	if d == nil || d.Name != name {
		return Declared{}, false
	}
	return Declared{D: d, Home: home, MintCommand: mint}, true
}

// declaredName reports whether d names the source, whether or not its
// declaration carries the capability a lookup is asking for. A declared
// source takes precedence over a compiled adapter of the same name.
func declaredName(name string, d *source.Declaration) bool {
	return d != nil && d.Name == name
}

// What a declared source answers in its threads with, and what each
// interface takes of it: the commands of its thread key, the variable of
// its credential, and its link. An interface is a unit: a Stater is a
// Replier, so one without reply keys is none.
func (d Declared) repliable() bool {
	t := d.D.Thread
	return d.D.Credential != nil && t != nil && len(t.Reply) > 0 && len(t.Posted) > 0 && len(t.Read) > 0
}

func (d Declared) fileable() bool {
	t := d.D.Thread
	return d.repliable() && t.Files
}

func (d Declared) openable() bool {
	t := d.D.Thread
	return d.repliable() && len(t.Open) > 0 && len(t.Opened) > 0 && t.Place != ""
}

func (d Declared) stateable() bool {
	t := d.D.Thread
	return d.repliable() && len(t.State) > 0 && len(t.States) > 0
}

func (d Declared) forwardable() bool {
	return d.stateable() && d.D.Link != nil
}

// A declared source with the keys speaks in its threads: each method by
// the thread command of the same name, the link by its data.
var (
	_ Replier   = Declared{}
	_ Filer     = Declared{}
	_ Opener    = Declared{}
	_ Stater    = Declared{}
	_ Forwarder = Declared{}
	_ Linker    = Declared{}
	_ Puller    = Declared{}
	_ Repoer    = Declared{}
)

// A delivery that came down the yard's link to its edge after the edge
// kept it while the yard was away is checked against when the edge
// received it, not against the yard's clock: the sender's timestamp would
// otherwise be outside the window for every delivery that waited (design
// yardr-hosted-050n, 3.4). The link marks the request with the frame's
// edge-signed received time, whose signature it checked first; a request
// on the listener carries no mark, so it is checked against now as before.
// The mark is the door's, on the request's context, never a header a
// sender could set.
type edgeReceivedKey struct{}

// WithEdgeReceived marks ctx with received, when the edge received the
// delivery the request carries, as its signed frame said it. It is what
// the link sets for a queued frame, and nothing else sets.
func WithEdgeReceived(ctx context.Context, received time.Time) context.Context {
	return context.WithValue(ctx, edgeReceivedKey{}, received)
}

func edgeReceived(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(edgeReceivedKey{}).(time.Time)
	return at, ok
}

// Declared is the adapter for a declared source: one adapter in Go for
// every declared source, built from its declaration. Verify checks the
// delivery as hook.verify says, without running anything: the signing
// secret reaches no command. Normalize runs hook.normalize, which is
// handed the verified body and no credential. Reply, Open and SetState run
// the thread command of the same name, handed the credential the core was
// called with; Issue and IssueThread read the link and run nothing.
type Declared struct {
	// D is the source's declaration; D.Hook is what receives.
	D *source.Declaration
	// Home is the yard's home: a relative normalize program runs from
	// its copy under Home/sources/<name>/.
	Home string
	// MintCommand is the source's mint_command setting, the operator's
	// command that mints a short-lived token, as one line. A thread
	// command is handed what it mints; "" when the operator set none.
	MintCommand string
}

func (d Declared) Verify(r *http.Request, body, secret []byte, now time.Time) (string, error) {
	v := d.D.Hook.Verify
	if v.Scheme == source.HookSecret {
		// The header holds the secret itself, compared as it arrives,
		// in constant time, as the token adapter compares its Bearer [REDACTED]
		// A missing header compares as "": it never equals the secret,
		// which the receiver refused empty before this runs.
		text := strings.TrimSpace(r.Header.Get(v.Signature.Header))
		if subtle.ConstantTimeCompare([]byte(text), secret) != 1 {
			return "", unauthorized("bad or missing %s", v.Signature.Header)
		}
	} else {
		key, ok := hmacKey(v.Key, secret)
		if !ok {
			return "", unauthorized("the hook's secret is not a key as verify.key writes it")
		}
		sigs := signatures(v.Signature, r.Header.Get(v.Signature.Header))
		if len(sigs) == 0 {
			return "", unauthorized("missing or malformed %s", v.Signature.Header)
		}
		mac := hmac.New(sha256.New, key)
		mac.Write(signedData(v, r, body))
		want := mac.Sum(nil)
		// Each entry's decoded bytes are compared with hmac.Equal, in
		// constant time, never as text. The first match ends the search,
		// which tells the sender nothing: it already held that signature.
		if !slices.ContainsFunc(sigs, func(sig []byte) bool { return hmac.Equal(sig, want) }) {
			return "", unauthorized("%s does not match", v.Signature.Header)
		}
	}
	// The timestamp is checked only after the delivery is verified, so
	// it is one the sender sent.
	if t := v.Timestamp; t != nil {
		at, err := hookTime(t, r.Header, body)
		if err != nil {
			return "", err
		}
		window, _ := time.ParseDuration(t.Window) // the reader refused anything else
		against, clock := now, "now"
		if received, ok := edgeReceived(r.Context()); ok {
			against, clock = received, "when the edge received it"
		}
		// Compare against each boundary instead of subtracting: Time.Sub
		// saturates at the largest duration, so a far-away timestamp could
		// otherwise pass when window is that same largest duration.
		if at.After(against) && at.After(against.Add(window)) || at.Before(against) && against.After(at.Add(window)) {
			dd := against.Sub(at)
			return "", unauthorized("its timestamp is %s away from %s", dd.Round(time.Second), clock)
		}
	}
	return d.deliveryID(r, body), nil
}

// signedData is what a signature covers, built from the declaration's
// signed template: the word body is the raw body alone, and a template
// is its literal text with {timestamp}, {body} and {delivery} filled in.
// The timestamp and the delivery id are the raw strings the sender sent,
// the header's value as it arrived or, for the timestamp, the body's value
// as written. Neither is a re-rendered integer or re-serialised JSON, so
// basestring and signature agree byte for byte. Both are read here, before
// anything is trusted, and the signature covers them. The age of the
// timestamp is checked after, in Verify, as it was when signed covered the
// body alone. One pass fills the template, so a value that was filled in
// is not read for placeholders in its turn: a body that spells
// {timestamp} is signed as written.
func signedData(v source.HookVerify, r *http.Request, body []byte) []byte {
	if v.Signed == "" || v.Signed == source.HookSigned {
		return body
	}
	return []byte(strings.NewReplacer(
		"{body}", string(body),
		"{timestamp}", signedTimestamp(v, r, body),
		"{delivery}", signedDelivery(v, r),
	).Replace(v.Signed))
}

// signedDelivery is the delivery id's raw string, for a signed template
// that names it: the header's value as it arrived. "" where the header is
// missing: the signature then does not match, as a missing timestamp's
// does not. verify.delivery names a header whenever a template names
// {delivery} (the reader refuses it otherwise); a delivery id in the body
// is never signed from here.
func signedDelivery(v source.HookVerify, r *http.Request) string {
	if v.Delivery.Header == "" {
		return ""
	}
	return r.Header.Get(v.Delivery.Header)
}

// hmacKey is the key the hook's secret makes: its own bytes, or, where
// verify.key says the service writes it, what follows the prefix in the
// secret, decoded. ok is false for a secret that is not so written and for
// one that decodes to nothing: an empty key would verify anyone's
// signature, and the receiver's refusal of an empty secret cannot see it.
func hmacKey(k *source.HookKey, secret []byte) (key []byte, ok bool) {
	if k == nil {
		return secret, true
	}
	text, ok := strings.CutPrefix(string(secret), k.Prefix)
	if !ok {
		return nil, false
	}
	key, err := decode(k.Encoding, text, false)
	if err != nil || len(key) == 0 {
		return nil, false
	}
	return key, true
}

// signatures are the signatures the header carries, decoded: the one it
// holds or, with verify.signature.list, one for each entry between the
// separators. An entry without the prefix is no signature of this scheme
// (a service that signs more than one way sends them side by side, and
// the other ways are not ours to check), and one that does not decode, or
// decodes to nothing, is none at all: both are left out, so a header with
// no signature left is malformed.
func signatures(s source.HookSignature, header string) [][]byte {
	entries := []string{header}
	if s.List != "" {
		entries = strings.Split(header, s.List)
	}
	var sigs [][]byte
	for _, entry := range entries {
		text, ok := strings.CutPrefix(strings.TrimSpace(entry), s.Prefix)
		if !ok {
			continue
		}
		if sig, err := decode(s.Encoding, text, true); err == nil && len(sig) > 0 {
			sigs = append(sigs, sig)
		}
	}
	return sigs
}

// decode reads text written in encoding, hex or base64. A signature is read
// strictly: base64's last character can hold bits that belong to no byte,
// and text whose spare bits are not zero is a second spelling of a
// signature the sender wrote one way. A key is read as the senders' own
// libraries read it, which ignore those bits.
func decode(encoding, text string, strict bool) ([]byte, error) {
	if encoding != source.HookBase64 {
		return hex.DecodeString(text)
	}
	if strict {
		return base64.StdEncoding.Strict().DecodeString(text)
	}
	// Some Standard Webhooks implementations accept the secret without
	// base64's trailing padding, so keys try both spellings. Signatures stay
	// strict: their header spelling is what the sender signed and emitted.
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(text)
}

// signedTimestamp is the timestamp's raw string, for a signed template
// that names it: the header's value as it arrived where verify.timestamp
// names a header, or the value at its dot path in the body as written
// (a JSON number keeps its digits). "" where the body is no JSON or
// carries none: the signature then does not match, as a missing header's
// does.
func signedTimestamp(v source.HookVerify, r *http.Request, body []byte) string {
	t := v.Timestamp
	if t == nil {
		return ""
	}
	if t.Header != "" {
		return r.Header.Get(t.Header)
	}
	if t.Body == "" {
		return ""
	}
	parsed, err := hookJSON(body)
	if err != nil {
		return ""
	}
	at, ok := hookAt(parsed, t.Body)
	if !ok {
		return ""
	}
	return hookScalar(at)
}

// ChallengeAnswer answers a service's verification handshake from a
// verified body: where the declaration's hook.challenge names a body
// whose field at Body holds Value, it returns the Answer field's name
// as written and its value, for the receiver to answer with at once.
// ok is false for a source with no challenge, a body that is no JSON,
// and a verified body that is no handshake: each is normalized and
// recorded as any other delivery. An answer field that is no scalar
// (an object, an array, null) is no answer either.
func (d Declared) ChallengeAnswer(body []byte) (name, value string, ok bool) {
	if d.D == nil || d.D.Hook == nil || d.D.Hook.Challenge == nil {
		return "", "", false
	}
	c := d.D.Hook.Challenge
	parsed, err := hookJSON(body)
	if err != nil {
		return "", "", false
	}
	field, found := hookAt(parsed, c.Body)
	if !found || hookScalar(field) != c.Value {
		return "", "", false
	}
	answer, found := hookAt(parsed, c.Answer)
	if !found {
		return "", "", false
	}
	switch answer.(type) {
	case string, json.Number, bool:
		return c.Answer, hookScalar(answer), true
	}
	return "", "", false
}

// deliveryID is the service's id of the delivery: the header the
// declaration names, or the value at its dot path in the body. "" when the
// declaration names none, or the body carries none: an empty id yields no
// dedupe key, as a service that sends none.
func (d Declared) deliveryID(r *http.Request, body []byte) string {
	dlv := d.D.Hook.Verify.Delivery
	if dlv.Header != "" {
		return r.Header.Get(dlv.Header)
	}
	if dlv.Body == "" {
		return ""
	}
	v, err := hookJSON(body)
	if err != nil {
		return ""
	}
	v, ok := hookAt(v, dlv.Body)
	if !ok {
		return ""
	}
	return hookScalar(v)
}

// hookJSON decodes a body while keeping JSON numbers exact. A delivery id
// is an identifier, so adjacent integers above float64's exact range must
// not collapse to the same key.
func hookJSON(body []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return v, nil
}

// hookAt is the value at the dot path in v, decoded from a body.
func hookAt(v any, path string) (any, bool) {
	for _, seg := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return v, true
}

// hookScalar is v as the string a delivery id is: a string as it is, a
// number as written in JSON, true or false. "" for anything else (an
// object, an array, null): no id, and no dedupe key for it.
func hookScalar(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case json.Number:
		return n.String()
	case bool:
		return strconv.FormatBool(n)
	default:
		return ""
	}
}

// hookTime is when the delivery was sent, from the timestamp the
// declaration names: in the body at the dot path, or in a header, in ms or
// s. Errors wrap ErrUnauthorized, or ErrBadRequest for a body that is no
// JSON.
func hookTime(t *source.HookTimestamp, h http.Header, body []byte) (time.Time, error) {
	var n int64
	if t.Body != "" {
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			return time.Time{}, bad("body is not JSON: %v", err)
		}
		v, ok := hookAt(v, t.Body)
		if !ok {
			return time.Time{}, unauthorized("missing timestamp %s", t.Body)
		}
		var err error
		if n, err = hookInt(v); err != nil {
			return time.Time{}, unauthorized("timestamp %s is no integer", t.Body)
		}
	} else {
		s := strings.TrimSpace(h.Get(t.Header))
		if s == "" {
			return time.Time{}, unauthorized("missing timestamp header %s", t.Header)
		}
		var err error
		if n, err = hookInt(s); err != nil {
			return time.Time{}, unauthorized("timestamp header %s is no integer", t.Header)
		}
	}
	if t.Unit == source.HookS {
		return time.Unix(n, 0), nil
	}
	return time.UnixMilli(n), nil
}

// hookInt is v as an integer: a JSON number, or one in a string.
func hookInt(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || n > 9e18 || n < -9e18 {
			return 0, fmt.Errorf("no integer")
		}
		return int64(n), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(n), 10, 64)
	default:
		return 0, fmt.Errorf("no integer")
	}
}

func (d Declared) Bodies() BodyScope {
	switch d.D.Hook.Bodies {
	case source.BodiesRepeat:
		return BodiesRepeat
	case source.BodiesUniquePerHook:
		return BodiesUniquePerHook
	default:
		return BodiesUniquePerSource
	}
}

func (d Declared) Normalize(h http.Header, body []byte) (*Envelope, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		// Not JSON: the command is handed the body as text, and
		// decides what of it is an envelope.
		v = string(body)
	}
	// With scheme secret the header verify names holds the hook's secret
	// itself, and the secret reaches no command: the command is handed
	// the other headers.
	if hv := d.D.Hook.Verify; hv.Scheme == source.HookSecret {
		h = h.Clone()
		h.Del(hv.Signature.Header)
	}
	in := map[string]any{"headers": map[string][]string(h), "body": v}
	cmd := source.Command{Key: "hook.normalize", Argv: d.D.Hook.Normalize}
	argv := slices.Clone(d.D.Hook.Normalize)
	argv[0] = cmd.Program(d.Home, d.D.Name)
	what := "hook.normalize of source " + d.D.Name
	var env *Envelope
	// The interface carries no context; the command is still killed at
	// the timeout, with everything it started.
	err := source.Run(context.Background(), what, argv, in, d.D.CommandEnv(""), d.Home, 0, func(out []byte) error {
		var e Envelope
		if err := json.Unmarshal(out, &e); err != nil {
			return fmt.Errorf("its answer is no envelope: %v", err)
		}
		if e.Type == "" {
			return fmt.Errorf("its envelope has no type")
		}
		env = &e
		return nil
	})
	if err != nil {
		return nil, bad("%v", err)
	}
	// The source and the delivery are the core's to say, not the
	// command's: the receiver sets the delivery after this returns.
	env.Source = d.D.Name
	env.Text = truncate(env.Text)
	if env.Mentions == nil {
		env.Mentions = []string{}
	}
	return env, nil
}

// runThread runs the thread command argv (key, e.g. "thread.reply") of d:
// in is the JSON object on its stdin, and it is handed the credential's
// value cred under the variable credential.env names, with
// YARDR_SOURCE_BASE from the declaration and nothing else of the yard.
// cred is what the core was called with: the adapter looks nothing up
// itself. read says in the caller's words what the answer lacks.
func (d Declared) runThread(ctx context.Context, key string, argv []string, in map[string]any, cred string, read func(out []byte) error) error {
	token, err := d.threadCredential(ctx, in, cred)
	if err != nil {
		return err
	}
	full := slices.Clone(argv)
	full[0] = source.Command{Key: key, Argv: argv}.Program(d.Home, d.D.Name)
	return source.Run(ctx, key+" of source "+d.D.Name, full, in, d.D.CommandEnv(token), d.Home, 0, read)
}

// threadCredential is the credential the thread command is handed under
// credential.env: the static one as it was read, or the token a minted one
// mints with the caller's scope, the thread, or the place for an open.
func (d Declared) threadCredential(ctx context.Context, in map[string]any, cred string) (string, error) {
	if !d.D.Credential.Minted() {
		return cred, nil
	}
	inputs, err := source.UnmintRef(cred, d.D.Credential.Mint)
	if err != nil || inputs == nil {
		return cred, err
	}
	scope := ""
	if thread, _ := in["thread"].(string); thread != "" {
		scope = thread
	} else if place, _ := in["in"].(string); place != "" {
		scope = place
	}
	token, _, err := source.Mint(ctx, d.D, d.D.MintArgv(d.Home, d.MintCommand), inputs, scope)
	if err != nil {
		return "", err
	}
	return token, nil
}
