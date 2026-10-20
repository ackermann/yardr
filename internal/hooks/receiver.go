package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// Store is what the receiver needs of the yard: the store itself, in the
// process that owns it. RecordHook is not on the socket, so only the
// receiver records deliveries.
type Store interface {
	GetHook(ctx context.Context, name string) (*store.Hook, error)
	GetSource(ctx context.Context, name string) (*store.Source, error)
	Users(ctx context.Context) ([]store.User, error)
	// PeerIdentities are the crew members of linked yards that senders
	// are mapped to (store.Peer.Identities), by the link's name.
	PeerIdentities(ctx context.Context) (map[string][]store.PeerIdentity, error)
	Crew(ctx context.Context) ([]store.CrewMember, error)
	Settings(ctx context.Context) (map[string]string, error)
	BlockHook(ctx context.Context, name, reason string) error
	RecordHook(ctx context.Context, name string, envelope map[string]any, payload []byte, dedupe []string) (seq uint64, duplicate bool, err error)
	RejectHook(ctx context.Context, name, reason string, status, count int, last time.Time) error
	// ClaimAddress offers the operator a claim for an address a
	// dropped delivery arrived from (store.ClaimAddress).
	ClaimAddress(ctx context.Context, sourceName, address, text string) (bool, error)
	// ClaimIdentity offers the operator a mapping card for the
	// sender a dropped delivery heard by identity arrived from
	// (store.ClaimIdentity).
	ClaimIdentity(ctx context.Context, sourceName, sender, channel, text string) (bool, error)
}

// A refused delivery is logged as a hook_rejected event, so that a hook
// whose deliveries are refused can be told from a source that sends nothing.
// Anyone can have the listener refuse them, so the events are bounded:
const (
	// RejectWindow is how long one name and reason log no second event. What
	// is refused meanwhile is counted, and said by the next event.
	RejectWindow = 10 * time.Minute
	// MaxRejectNames is how many names that are no hook's are told apart at
	// a time; a refusal under a further one is counted under OtherHooks. A
	// hook's own name is always its own: a scanner trying names cannot hide a
	// broken hook among them.
	MaxRejectNames = 32
	OtherHooks     = "(other)"
	// maxRejectName cuts the name asked for, which is the sender's to choose.
	maxRejectName = 64
)

// refusal is what refusals are counted by.
type refusal struct{ hook, reason string }

// tally is the refusals of one name and reason since their last event.
type tally struct {
	logged  time.Time // when the last event was logged
	pending int       // refused since, not yet in an event
	last    time.Time // the last of them
	status  int
	stray   bool // the name is no hook's (counts towards MaxRejectNames)
}

// Receiver serves POST /hooks/<name> and nothing else. It is meant for its
// own listener, apart from the store socket and BDP.
type Receiver struct {
	st   Store
	home string
	log  *slog.Logger
	now  func() time.Time

	mu       sync.Mutex
	rejected map[string]int
	refused  map[refusal]*tally
}

// NewReceiver serves the hooks of the yard whose home is home: a relative
// normalize program of a declared source runs from its copy under it.
func NewReceiver(st Store, home string, log *slog.Logger) *Receiver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Receiver{st: st, home: home, log: log, now: time.Now, rejected: map[string]int{}, refused: map[refusal]*tally{}}
}

// SetClock replaces the clock (tests).
func (rc *Receiver) SetClock(now func() time.Time) { rc.now = now }

// Rejected counts refused (and duplicate) deliveries by reason.
func (rc *Receiver) Rejected() map[string]int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make(map[string]int, len(rc.rejected))
	for k, v := range rc.rejected {
		out[k] = v
	}
	return out
}

func (rc *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hooks/{name}", rc.serve)
	return mux
}

func (rc *Receiver) serve(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h, err := rc.st.GetHook(r.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		rc.reject(w, nil, name, "unknown_hook", http.StatusNotFound, err)
		return
	} else if err != nil {
		rc.reject(w, nil, name, "store", http.StatusInternalServerError, err)
		return
	}
	ad, ok := rc.adapter(r.Context(), h.Source)
	if !ok {
		rc.reject(w, h, name, "unknown_source", http.StatusInternalServerError, errors.New("no adapter for source "+h.Source))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, store.MaxHookPayload))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		rc.reject(w, h, name, "too_large", http.StatusRequestEntityTooLarge, err)
		return
	} else if err != nil {
		rc.reject(w, h, name, "read", http.StatusBadRequest, err)
		return
	}
	// The file is checked on every delivery, not only at `hook add`: it may
	// have been loosened or replaced since. One that fails the rule is not
	// used to verify anything.
	secret, _, err := config.ReadSecretFile(h.SecretFile)
	if err != nil {
		rc.block(r.Context(), h, err.Error())
		rc.reject(w, h, name, "blocked", http.StatusServiceUnavailable, err)
		return
	}
	rc.block(r.Context(), h, "")
	if len(secret) == 0 {
		// An empty key would verify anyone's signature.
		rc.reject(w, h, name, "secret", http.StatusInternalServerError, errors.New("secret file is empty"))
		return
	}
	delivery, err := ad.Verify(r, body, secret, rc.now())
	if err != nil {
		code, reason := http.StatusUnauthorized, "unauthorized"
		if errors.Is(err, ErrBadRequest) {
			code, reason = http.StatusBadRequest, "bad_request"
		}
		rc.reject(w, h, name, reason, code, err)
		return
	}
	// A verification handshake is answered from the verified body at
	// once, before Normalize, dedupe or recording: it is the service
	// proving the endpoint is live, not a delivery. Verification came
	// first, so an unsigned handshake is refused above as any bad
	// signature is. A source with no challenge key has none of this.
	if dec, ok := ad.(Declared); ok {
		if answerName, answerValue, ok := dec.ChallengeAnswer(body); ok {
			writeJSON(w, http.StatusOK, map[string]any{answerName: answerValue})
			return
		}
	}
	env, err := ad.Normalize(r.Header, body)
	if err != nil {
		rc.reject(w, h, name, "bad_request", http.StatusBadRequest, err)
		return
	}
	env.Delivery = delivery
	deriveTrain(env)
	// Speaker is a core-derived field, so a normalize command cannot
	// claim who an event came from. The identity-hearing path below
	// stamps it only after matching the sender to a yard user or to a
	// crew member of a linked yard.
	delete(env.Fields, store.SpeakerKey)
	// A declared source that hears drops a delivery from a speaker it
	// does not hear after verification: answered 200, so the service
	// does not retry it, recorded nowhere, and counted in sums beside
	// refusals, so a stranger's burst is one bounded entry. By address
	// that is any address no user's env file sets; by identity it is
	// the hook's Unknown word that says what an unmapped sender is.
	if dec, ok := ad.(Declared); ok && dec.D.Hear != nil {
		if dec.D.Hear.ByIdentity() {
			speaker, mapped := rc.speaker(r.Context(), dec.D, env)
			switch {
			case rc.ownMessage(r.Context(), dec.D, env):
				// The yard's own post coming back, by a crew member's
				// identity in this source: dropped under any Unknown
				// word, and never a mapping card. The echo of a reply
				// is not a stranger, so it is counted as the yard's
				// own.
				rc.count("self")
				rc.refuse(h, name, "self", http.StatusOK)
				writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
				return
			case mapped:
				stampSpeaker(env, speaker)
			case rc.ignored(r.Context(), dec.D, env):
				// An ignored sender is dropped without a card under any
				// word: the operator has answered for them in
				// particular, so unknown offers nothing further and
				// allow hears nothing of them.
				rc.count("stranger")
				rc.refuse(h, name, "stranger", http.StatusOK)
				writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
				return
			case h.UnknownPolicy() == store.HookUnknownAllow:
				stampSpeaker(env, store.SpeakerOutside)
			default:
				rc.count("stranger")
				rc.refuse(h, name, "stranger", http.StatusOK)
				if h.UnknownPolicy() == store.HookUnknownUnknown {
					// Offer the operator a mapping card for the sender
					// it arrived from: who this sender is, for them to
					// map to a user of the yard. Where none can be
					// offered (no operator, one waiting for this
					// sender already) the drop stands as it was,
					// answered 200 either way. Under ignore no card
					// is offered at all, and under allow none is
					// needed: the sender is heard as outside.
					if values := env.Fields[dec.D.Hear.Field]; len(values) > 0 && values[0] != "" {
						channel := ""
						if cs := env.Fields["channel"]; len(cs) > 0 {
							channel = cs[0]
						}
						if _, err := rc.st.ClaimIdentity(r.Context(), dec.D.Name, values[0], channel, env.Text); err != nil {
							rc.log.Warn("mapping card not offered", "hook", name, "source", dec.D.Name, "err", err)
						}
					}
				}
				writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
				return
			}
		} else if !rc.hears(r.Context(), dec.D, env) {
			rc.count("stranger")
			rc.refuse(h, name, "stranger", http.StatusOK)
			// Offer the operator a claim for the address it arrived from:
			// the person wrote to the service, and confirming the card is
			// how the yard learns their address. A claim that cannot be
			// offered (no operator, one waiting already, an address set
			// since) leaves the drop as it was, answered 200 either way.
			if field, _, ok := dec.D.ClaimAddress(); ok {
				if values := env.Fields[field]; len(values) > 0 && values[0] != "" {
					if _, err := rc.st.ClaimAddress(r.Context(), dec.D.Name, values[0], env.Text); err != nil {
						rc.log.Warn("address claim not offered", "hook", name, "source", dec.D.Name, "err", err)
					}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"dropped": true})
			return
		}
	}
	// Dedupe after Verify, so only the source can claim a body or a
	// delivery id; the store checks and records the keys in the write that
	// records the delivery, so a concurrent redelivery cannot slip through.
	seq, dup, err := rc.st.RecordHook(r.Context(), name, env.Map(), body, DedupeKeys(name, h.Source, ad, body, delivery))
	if err != nil {
		rc.reject(w, h, name, "store", http.StatusInternalServerError, err)
		return
	}
	if dup {
		rc.count("duplicate")
		rc.log.Info("hook delivery dropped as duplicate", "hook", name, "delivery", delivery, "seq", seq)
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": true, "seq": seq})
		return
	}
	// A source treats any non-200 response as a failed delivery and
	// retries it. The event is already durable here, so acknowledge it
	// with 200.
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq})
}

// deriveTrain fills the envelope's train field from its head_branch when
// the branch is a yardr train branch and the envelope carries no train:
// the yard/<train> branch is yardr's convention, not the service's, so a
// normalize script must not restate it. An envelope whose script said the
// train keeps what it said.
func deriveTrain(env *Envelope) {
	if env == nil || len(env.Fields["train"]) > 0 {
		return
	}
	for _, branch := range env.Fields["head_branch"] {
		if train := trainOf(branch); train != "" {
			env.Fields["train"] = []string{train}
			return
		}
	}
}

// trainOf is the train a branch carries, when it is a yardr train branch
// (store.Branch): yard/<train> -> <train>.
func trainOf(branch string) string {
	id, ok := strings.CutPrefix(branch, store.Branch(""))
	if !ok {
		return ""
	}
	return id
}

// adapter is the adapter for source: the one built from the yard's
// declared source when it receives webhooks, else the builtin one.
// The store is read for every source: a name is either, never both.
func (rc *Receiver) adapter(ctx context.Context, name string) (Adapter, bool) {
	var d *source.Declaration
	if src, err := rc.st.GetSource(ctx, name); err == nil {
		d, _ = src.Declaration()
	}
	return AdapterFor(name, d, rc.home)
}

// stampSpeaker records who a kept delivery was heard from, as the
// core derives an envelope's train: fields.speaker holds the yard
// user's name, <crew>@<peer> for a crew member of a linked yard, or
// SpeakerOutside for an unmapped sender kept under allow. No normalize
// script sets it, and the envelope's actor is left as the source gave it.
func stampSpeaker(env *Envelope, speaker string) {
	if env.Fields == nil {
		env.Fields = map[string][]string{}
	}
	env.Fields[store.SpeakerKey] = []string{speaker}
}

// speaker is who a delivery heard by identity was heard from: the one
// user whose identity in this source the value of fields[field] is
// (User.IsIn read over every user), or else the one crew member of a
// linked yard it is mapped to (PeerIdentity.IsIn), named <crew>@<peer>.
// The two are read together, so an id that a user and a peer's crew member
// both hold names nobody. A link's mapping never gives a sender a user's
// word. mapped is false for a sender no one maps, for one whose id two
// hold already (an ambiguous id names nobody until one identity is
// removed), for a delivery with no field to read, and for a yard whose
// users or links are unreadable. Each is unmapped, and the hook's Unknown
// word says what happens then.
func (rc *Receiver) speaker(ctx context.Context, d *source.Declaration, env *Envelope) (speaker string, mapped bool) {
	values := env.Fields[d.Hear.Field]
	if len(values) == 0 {
		return "", false
	}
	users, err := rc.st.Users(ctx)
	if err != nil {
		return "", false
	}
	peers, err := rc.st.PeerIdentities(ctx)
	if err != nil {
		return "", false
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		var holders []string
		for _, u := range users {
			if u.IsIn(d.Name, v) && !slices.Contains(holders, u.Name) {
				holders = append(holders, u.Name)
			}
		}
		for peer, ids := range peers {
			for _, i := range ids {
				if s := i.Speaker(peer); i.IsIn(d.Name, v) && !slices.Contains(holders, s) {
					holders = append(holders, s)
				}
			}
		}
		switch len(holders) {
		case 1:
			return holders[0], true
		case 0:
			continue
		default:
			return "", false
		}
	}
	return "", false
}

// ownMessage reports whether the delivery's sender is who a crew member
// is in this source (CrewMember.IsIn over every crew member, by its id
// or its key): the yard's own post, sent back by the service. A crew
// that cannot be read is not the yard's: the Unknown word then says
// what happens, as any unmapped sender's does.
func (rc *Receiver) ownMessage(ctx context.Context, d *source.Declaration, env *Envelope) bool {
	values := env.Fields[d.Hear.Field]
	if len(values) == 0 {
		return false
	}
	crew, err := rc.st.Crew(ctx)
	if err != nil {
		return false
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		for _, r := range crew {
			if r.IsIn(d.Name, v) {
				return true
			}
		}
	}
	return false
}

// ignored reports whether the delivery's sender stands on the source's
// ignore list: one of fields[field] the operator answered nobody for.
// A source that cannot be read ignores nothing: the drop then offers a
// card, as any unmapped sender's does.
func (rc *Receiver) ignored(ctx context.Context, d *source.Declaration, env *Envelope) bool {
	values := env.Fields[d.Hear.Field]
	if len(values) == 0 {
		return false
	}
	src, err := rc.st.GetSource(ctx, d.Name)
	if err != nil {
		return false
	}
	for _, v := range values {
		if v != "" && slices.Contains(src.Ignored, v) {
			return true
		}
	}
	return false
}

// hears reports whether the delivery's envelope carries an address a
// user's env file sets: one of fields[field] equals what the file sets
// under hear.address.env, read as the daemon reads it when telling,
// whatever the user is told through. With from: operator only the
// operator's address hears. A user whose file is missing or unreadable
// hears nothing, as does a yard whose operator or users are unreadable: a
// stranger is dropped, never answered with an error the service would
// retry.
func (rc *Receiver) hears(ctx context.Context, d *source.Declaration, env *Envelope) bool {
	values := env.Fields[d.Hear.Field]
	if len(values) == 0 {
		return false
	}
	users, err := rc.st.Users(ctx)
	if err != nil {
		return false
	}
	operator := ""
	onlyOperator := d.Hear.From == source.HearFromOperator
	if onlyOperator {
		settings, err := rc.st.Settings(ctx)
		if err != nil {
			return false
		}
		operator = settings[store.OperatorSetting]
	}
	for _, u := range users {
		if onlyOperator && u.Name != operator {
			continue
		}
		if u.EnvFile == "" {
			continue
		}
		f, err := config.ReadEnvFile(u.EnvFile)
		if err != nil {
			continue
		}
		if addr := source.Lookup(f.Env, d.Hear.Address.Env); addr != "" && slices.Contains(values, addr) {
			return true
		}
	}
	return false
}

// block records that the hook's secret file fails the rule (reason), or
// passes it again (empty). The store is written only when that differs from
// what the hook says, so a healthy hook costs no write per delivery and a
// blocked one logs its event once.
func (rc *Receiver) block(ctx context.Context, h *store.Hook, reason string) {
	if h.Blocked == reason {
		return
	}
	if err := rc.st.BlockHook(ctx, h.Name, reason); err != nil {
		rc.log.Error("hook state not recorded", "hook", h.Name, "err", err)
		return
	}
	if reason == "" {
		rc.log.Info("hook unblocked", "hook", h.Name, "path", h.SecretFile)
	}
}

// reject answers a delivery that is not taken, and has it logged (refuse).
// h is the hook it was sent to, nil when the name is no hook's or the hook
// could not be read.
func (rc *Receiver) reject(w http.ResponseWriter, h *store.Hook, name, reason string, code int, err error) {
	rc.count(reason)
	rc.log.Warn("hook delivery rejected", "hook", name, "reason", reason, "status", code, "err", err)
	rc.refuse(h, name, reason, code)
	http.Error(w, http.StatusText(code), code)
}

// refuse counts a refusal and logs the hook_rejected event when its name
// and reason have had none within RejectWindow; otherwise the next event
// says it, by its count. The event carries the name, the reason and the
// status, never anything else of the request.
func (rc *Receiver) refuse(h *store.Hook, name, reason string, code int) {
	now := rc.now()
	rc.mu.Lock()
	key := refusal{rejectName(name), reason}
	t := rc.refused[key]
	first := false // of its name and reason: said at once
	if t == nil {
		if h == nil && key.hook != OtherHooks && rc.strays(key.hook) >= MaxRejectNames {
			key.hook = OtherHooks
			t = rc.refused[key]
		}
		if t == nil {
			t, first = &tally{stray: h == nil}, true
			rc.refused[key] = t
		}
	}
	t.pending++
	t.last, t.status = now, code
	var due []rejection
	if first || now.Sub(t.logged) >= RejectWindow {
		due = append(due, rc.take(key, t, now))
	}
	rc.mu.Unlock()
	rc.logRejections(due)
}

// strays counts the names that are no hook's being told apart, but for
// name itself. Called with rc.mu held.
func (rc *Receiver) strays(name string) int {
	names := map[string]bool{}
	for k, t := range rc.refused {
		if t.stray && k.hook != OtherHooks {
			names[k.hook] = true
		}
	}
	if names[name] {
		return 0 // already one of them, under another reason
	}
	return len(names)
}

// rejection is one hook_rejected event to log.
type rejection struct {
	refusal
	status, count int
	last          time.Time
}

// take empties t into the event that says it. Called with rc.mu held.
func (rc *Receiver) take(key refusal, t *tally, now time.Time) rejection {
	r := rejection{key, t.status, t.pending, t.last}
	t.logged, t.pending = now, 0
	return r
}

// logRejections writes the events, outside rc.mu: a slow store does not
// hold up the deliveries being refused meanwhile. One that is not written
// goes back on its tally, for the next event to say.
func (rc *Receiver) logRejections(due []rejection) {
	for _, r := range due {
		// Not the request's context: the sender going away does not unsay it.
		err := rc.st.RejectHook(context.Background(), r.hook, r.reason, r.status, r.count, r.last)
		if err == nil {
			continue
		}
		rc.log.Error("hook rejection not recorded", "hook", r.hook, "reason", r.reason, "count", r.count, "err", err)
		rc.mu.Lock()
		if t := rc.refused[r.refusal]; t != nil {
			t.pending += r.count
		}
		rc.mu.Unlock()
	}
}

// Flush logs what was refused within a window that has since ended, so a
// burst is said within RejectWindow of its start and not only when the next
// refusal comes, which may be never. With all it logs everything counted,
// as a receiver that stops does. A name and reason with nothing counted and
// a window ended are forgotten, which frees the name's place among
// MaxRejectNames.
func (rc *Receiver) Flush(all bool) {
	now := rc.now()
	rc.mu.Lock()
	var due []rejection
	for key, t := range rc.refused {
		switch {
		case !all && now.Sub(t.logged) < RejectWindow:
		case t.pending > 0:
			due = append(due, rc.take(key, t, now))
		default:
			delete(rc.refused, key)
		}
	}
	rc.mu.Unlock()
	rc.logRejections(due)
}

// Run flushes as windows end until ctx ends, and then everything counted:
// the server runs it beside the listener, and ends ctx while the store is
// still open.
func (rc *Receiver) Run(ctx context.Context) {
	tick := time.NewTicker(RejectWindow / 10)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			rc.Flush(true)
			return
		case <-tick.C:
			rc.Flush(false)
		}
	}
}

// rejectName is the name a refusal is logged under: what was asked for,
// cut to maxRejectName characters and with nothing a terminal would act on,
// since whoever sends the request chooses it.
func rejectName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(name, "?"))
	if rs := []rune(name); len(rs) > maxRejectName {
		name = string(rs[:maxRejectName])
	}
	return name
}

func (rc *Receiver) count(reason string) {
	rc.mu.Lock()
	rc.rejected[reason]++
	rc.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
