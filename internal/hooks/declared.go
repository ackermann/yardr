package hooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// AdapterFor is the adapter for source: the builtin one of that name, or
// the one built from d, the yard's declared source of that name, when it
// receives webhooks (a hook key). d is nil when the yard declares no such
// source. home is the yard's home, which a relative command program runs
// from; it is only read from the adapter returned, so a check for whether
// a source has an adapter passes "". ok is false for a source yardr has
// nothing for.
func AdapterFor(name string, d *source.Declaration, home string) (Adapter, bool) {
	if ad, ok := Adapters[name]; ok {
		return ad, true
	}
	dec, ok := declared(name, d, home)
	if !ok || dec.D.Hook == nil {
		return nil, false
	}
	return dec, true
}

// declared is the adapter built from d, the yard's declared source of that
// name. It is the one way every lookup finds a declared source: AdapterFor
// and each of ReplierFor, OpenerFor, StaterFor and ForwarderFor add the
// keys their interface takes, and a declared source without them lacks the
// interface, as an adapter in Go may.
func declared(name string, d *source.Declaration, home string) (Declared, bool) {
	if d == nil || d.Name != name {
		return Declared{}, false
	}
	return Declared{D: d, Home: home}, true
}

// What a declared source answers in its threads with, and what each
// interface takes of it: the commands of its thread key, the variable of
// its credential, and its link. An interface is a unit: a Stater is a
// Replier, so one without reply keys is none.
func (d Declared) repliable() bool {
	t := d.D.Thread
	return d.D.Credential != nil && t != nil && len(t.Reply) > 0 && len(t.Posted) > 0 && len(t.Read) > 0
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

func (d Declared) linkable() bool { return d.D.Link != nil }

// A declared source with the keys speaks in its threads: each method by
// the thread command of the same name, the link by its data.
var (
	_ Replier   = Declared{}
	_ Opener    = Declared{}
	_ Stater    = Declared{}
	_ Forwarder = Declared{}
	_ Linker    = Declared{}
)

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
}

func (d Declared) Verify(r *http.Request, body, secret []byte, now time.Time) (string, error) {
	v := d.D.Hook.Verify
	text := strings.TrimSpace(r.Header.Get(v.Signature.Header))
	if v.Signature.Prefix != "" {
		var ok bool
		text, ok = strings.CutPrefix(text, v.Signature.Prefix)
		if !ok {
			return "", unauthorized("missing or malformed %s", v.Signature.Header)
		}
	}
	sig, err := hex.DecodeString(text)
	if err != nil || len(sig) == 0 {
		return "", unauthorized("missing or malformed %s", v.Signature.Header)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", unauthorized("%s does not match", v.Signature.Header)
	}
	// The timestamp is checked only after the signature, so it is one the
	// sender signed.
	if t := v.Timestamp; t != nil {
		at, err := hookTime(t, r.Header, body)
		if err != nil {
			return "", err
		}
		window, _ := time.ParseDuration(t.Window) // the reader refused anything else
		// Compare against each boundary instead of subtracting: Time.Sub
		// saturates at the largest duration, so a far-away timestamp could
		// otherwise pass when window is that same largest duration.
		if at.After(now) && at.After(now.Add(window)) || at.Before(now) && now.After(at.Add(window)) {
			dd := now.Sub(at)
			return "", unauthorized("its timestamp is %s away from now", dd.Round(time.Second))
		}
	}
	return r.Header.Get(v.Delivery.Header), nil
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
		for _, seg := range strings.Split(t.Body, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				return time.Time{}, unauthorized("missing timestamp %s", t.Body)
			}
			v, ok = m[seg]
			if !ok {
				return time.Time{}, unauthorized("missing timestamp %s", t.Body)
			}
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
	if d.D.Hook.Bodies == source.BodiesRepeat {
		return BodiesRepeat
	}
	return BodiesUniquePerSource
}

func (d Declared) Normalize(h http.Header, body []byte) (*Envelope, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		// Not JSON: the command is handed the body as text, and
		// decides what of it is an envelope.
		v = string(body)
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
	full := slices.Clone(argv)
	full[0] = source.Command{Key: key, Argv: argv}.Program(d.Home, d.D.Name)
	return source.Run(ctx, key+" of source "+d.D.Name, full, in, d.D.CommandEnv(cred), d.Home, 0, read)
}
