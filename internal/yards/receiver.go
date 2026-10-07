// Package yards carries messages between this yard and the yards it is
// linked with (docs/peers.md): the Receiver takes what a peer posts and
// answers what it asks for, on the server's hook listener under /peers/;
// the Caller is the other end, which the daemon posts and asks with.
//
// Every request is signed with the sender's key and every answer with the
// receiver's (internal/yardkey). That proves who sent it and that it is
// whole. It does not hide it.
package yards

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/internal/yardkey"
)

// Path is where a yard takes its peers' requests: POST for a message, GET
// with ?after=<seq> for what waits for the asker. A GET may add
// &refused=<seq>:<reason>,... for what the asker got and did not take, and
// &wait=<seconds> to have the answer held until there is something to hand
// over (see hold).
//
// Each request, and each signed answer, also says what the build of the
// yard it is from understands (store.PeerSays): "says" beside a posted
// message's fields and in an answer's, &understands=<kind>,...&yardr=<build>
// on a GET. Each is under the signature, and each is one more field or
// parameter, which a yardr from before it was said passes by.
const Path = "/peers/messages"

// Says is what this build states of itself to a peer: the kinds it
// understands, and the release it is with its revision, or the one of the
// two it has.
func Says() *store.PeerSays {
	build := version.Tag()
	if rev := version.Current(); build == "" {
		build = rev
	} else if rev != "" {
		build += "+" + rev
	}
	return store.Says(build)
}

// maxRefusals bounds the refusals one question carries; the rest go with
// the next.
const maxRefusals = 32

const (
	// defaultWait is how long a pull that asks to be held, and does not
	// say for how long in a number, is held; maxWait is the longest any is.
	defaultWait = 30 * time.Second
	maxWait     = 60 * time.Second
	// maxHeld bounds the pulls held at once, over all peers: each is a
	// connection and a goroutine kept. A pull past it is answered at once,
	// which its asker takes as a yard that holds none.
	maxHeld = 256
	// answerTime is what a held pull is given beyond its wait to be
	// answered in, as the listener gives any request.
	answerTime = 30 * time.Second
)

// maxRequest bounds a posted message: its body, and room for the rest.
const maxRequest = store.MaxPeerBody + 4<<10

// Store is what the receiver needs of the yard: the store itself, in the
// process that owns it. None of the four is on the socket. Those that take
// something of a request are given the key its signature was checked
// against, with the name PeerByKey found for it: the name alone may be
// another yard's by then.
type Store interface {
	PeerByKey(ctx context.Context, key string) (*store.Peer, error)
	PeerHeard(ctx context.Context, name, key string, says *store.PeerSays) error
	PeerReceive(ctx context.Context, name, key string, msgs []store.PeerMessage) ([]store.PeerTaken, error)
	PeerHandOver(ctx context.Context, name, key string, after uint64, refused map[uint64]string) (*store.PeerPage, error)
	// PeerWake's channel receives when a message was put in the outbox of
	// the peer with key, and closes with ctx.
	PeerWake(ctx context.Context, key string) (<-chan struct{}, error)
}

// Receiver serves Path and nothing else.
type Receiver struct {
	st Store
	// pair is the yard's side of a pairing; nil for a yard that takes none.
	pair PairStore
	key  string // the file of this yard's key
	log  *slog.Logger
	now  func() time.Time

	mu       sync.Mutex
	rejected map[string]int
	// held is peer key -> the pull that peer holds open, closed to end it,
	// and maxHeld how many there may be.
	held    map[string]chan struct{}
	maxHeld int
}

// NewReceiver serves the peers of the yard whose key is in the file
// keyFile. The key is read on every request, as a hook's secret is.
func NewReceiver(st Store, keyFile string, log *slog.Logger) *Receiver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	rc := &Receiver{st: st, key: keyFile, log: log, now: time.Now, rejected: map[string]int{}, held: map[string]chan struct{}{}, maxHeld: maxHeld}
	// The store is both; a test's stand-in for its messages may be only one.
	rc.pair, _ = st.(PairStore)
	return rc
}

// SetClock replaces the clock (tests).
func (rc *Receiver) SetClock(now func() time.Time) { rc.now = now }

// SetMaxHeld replaces how many pulls are held at once (tests). With none,
// the receiver answers every pull at once, as a yardr from before pulls
// were held does.
func (rc *Receiver) SetMaxHeld(n int) {
	rc.mu.Lock()
	rc.maxHeld = n
	rc.mu.Unlock()
}

// Rejected counts refused (and duplicate) requests by reason, as the hook
// receiver does.
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
	mux.HandleFunc("POST "+Path, rc.post)
	mux.HandleFunc("GET "+Path, rc.get)
	if rc.pair != nil {
		mux.HandleFunc("POST "+pairing.PathHello, rc.pairHello)
		mux.HandleFunc("POST "+pairing.PathReveal, rc.pairReveal)
		mux.HandleFunc("POST "+pairing.PathConfirm, rc.pairConfirm)
	}
	return mux
}

// from is a request whose signature held: the peer it is from, and what
// its answer is signed with and for.
type from struct {
	peer *store.Peer
	key  ed25519.PrivateKey
	sig  string
	body []byte
}

// verify reads the request and tells whose it is. A request that is no
// peer's is answered here, 401 whatever was wrong with it: a key no link
// has, a signature that does not hold, a timestamp too far off. The log and
// the counts say which; the sender is not told.
func (rc *Receiver) verify(w http.ResponseWriter, r *http.Request) (*from, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		rc.reject(w, "", "too_large", http.StatusRequestEntityTooLarge, err)
		return nil, false
	} else if err != nil {
		rc.reject(w, "", "read", http.StatusBadRequest, err)
		return nil, false
	}
	unauthorized := func(peer, reason string, err error) (*from, bool) {
		rc.reject(w, peer, reason, http.StatusUnauthorized, err)
		return nil, false
	}
	// Without a key of its own the yard can sign no answer, and has given
	// no one a key to add it by.
	key, err := yardkey.Load(rc.key)
	if err != nil {
		return unauthorized("", "no_key", err)
	}
	p, err := rc.st.PeerByKey(r.Context(), r.Header.Get(yardkey.HeaderKey))
	if errors.Is(err, store.ErrNotFound) {
		return unauthorized("", "unknown_peer", err)
	} else if err != nil {
		rc.reject(w, "", "store", http.StatusInternalServerError, err)
		return nil, false
	}
	sig := r.Header.Get(yardkey.HeaderSig)
	err = yardkey.VerifyRequest(p.Key, yardkey.Public(key), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), sig, body, rc.now())
	if err != nil {
		return unauthorized(p.Name, "unauthorized", err)
	}
	// What the peer says of its build is kept whatever becomes of the
	// request: it is the peer's word either way. Not kept, it costs the
	// request nothing.
	if says, ok := stated(r, body); ok {
		if err := rc.st.PeerHeard(r.Context(), p.Name, p.Key, says); err != nil && !errors.Is(err, store.ErrNotFound) {
			rc.log.Warn("peer: keeping what it says it understands", "peer", p.Name, "err", err)
		}
	}
	return &from{peer: p, key: key, sig: sig, body: body}, true
}

// stated reads what a request says of its sender's build: nil for one that
// says nothing, an older yardr. Not ok for a post that is no JSON at all,
// which says neither.
func stated(r *http.Request, body []byte) (says *store.PeerSays, ok bool) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if !q.Has("understands") {
			return nil, true
		}
		says = &store.PeerSays{Understands: []string{}, Yardr: q.Get("yardr")}
		if kinds := q.Get("understands"); kinds != "" {
			says.Understands = strings.Split(kinds, ",")
		}
		return says, true
	}
	var m struct {
		Says *store.PeerSays `json:"says"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false
	}
	return m.Says, true
}

func (rc *Receiver) post(w http.ResponseWriter, r *http.Request) {
	f, ok := rc.verify(w, r)
	if !ok {
		return
	}
	var m store.PeerMessage
	if err := json.Unmarshal(f.body, &m); err != nil {
		rc.answer(w, f, "bad_request", http.StatusBadRequest, map[string]any{"error": "not a message"})
		return
	}
	m.Seq = 0 // the sender's own; a posted message has no place here
	taken, err := rc.st.PeerReceive(r.Context(), f.peer.Name, f.peer.Key, []store.PeerMessage{m})
	switch {
	case errors.Is(err, store.ErrNotFound): // removed since verify, or its name another yard's
		rc.reject(w, f.peer.Name, "unknown_peer", http.StatusUnauthorized, err)
	case err != nil:
		// The store refuses only what is no message; its words name
		// nothing of the yard.
		rc.log.Warn("peer message rejected", "peer", f.peer.Name, "err", err)
		rc.answer(w, f, "bad_request", http.StatusBadRequest, map[string]any{"error": "not a message"})
	case taken[0].Refused != "":
		rc.log.Warn("peer message refused", "peer", f.peer.Name, "kind", m.Kind, "id", m.ID, "reason", taken[0].Refused)
		rc.answer(w, f, "refused", http.StatusForbidden, map[string]any{"refused": taken[0].Refused})
	case taken[0].Duplicate:
		rc.log.Info("peer message dropped as duplicate", "peer", f.peer.Name, "id", m.ID)
		rc.answer(w, f, "duplicate", http.StatusOK, map[string]any{"duplicate": true})
	default:
		rc.answer(w, f, "", http.StatusOK, map[string]any{"taken": true})
	}
}

func (rc *Receiver) get(w http.ResponseWriter, r *http.Request) {
	f, ok := rc.verify(w, r)
	if !ok {
		return
	}
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		rc.answer(w, f, "bad_request", http.StatusBadRequest, map[string]any{"error": "after wants a sequence number"})
		return
	}
	q := r.URL.Query()
	// A pull that asks to wait is held open while nothing waits for its
	// asker, so a yard that cannot be called hears of a message when it is
	// queued, and asks twice a minute, not every few seconds. The wake is
	// asked for before the outbox is read: a message queued between the two
	// is then in the page or wakes the hold, never neither.
	var wake <-chan struct{}
	var ended chan struct{}
	var timeout <-chan time.Time
	if wait := waitOf(q); wait > 0 {
		if ended = rc.hold(f.peer.Key); ended != nil {
			defer rc.release(f.peer.Key, ended)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			if wake, err = rc.st.PeerWake(ctx, f.peer.Key); err != nil {
				rc.reject(w, f.peer.Name, "store", http.StatusInternalServerError, err)
				return
			}
			t := time.NewTimer(wait)
			defer t.Stop()
			timeout = t.C
			// The listener bounds a request by the time it gives any, less
			// than a hold may take: this one is given its wait on top. The
			// read's bound too, or the connection would count as lost
			// while it is held. A writer that cannot be told (a test's
			// recorder) has no bound to move.
			by := time.Now().Add(wait + answerTime)
			ctl := http.NewResponseController(w)
			_, _ = ctl.SetReadDeadline(by), ctl.SetWriteDeadline(by)
		}
	}
	for {
		page, err := rc.st.PeerHandOver(r.Context(), f.peer.Name, f.peer.Key, after, refusals(q.Get("refused")))
		if errors.Is(err, store.ErrNotFound) {
			rc.reject(w, f.peer.Name, "unknown_peer", http.StatusUnauthorized, err)
			return
		} else if err != nil {
			rc.reject(w, f.peer.Name, "store", http.StatusInternalServerError, err)
			return
		}
		// A peer this yard posts to is not kept waiting for what will
		// never be handed over.
		held := ended != nil && !page.Push && len(page.Messages) == 0
		if held {
			select {
			case _, open := <-wake:
				if open {
					continue
				}
				// The store closed: answered as it stands.
			case <-ended: // the same peer asked again: this one is over
			case <-timeout:
			case <-r.Context().Done():
				// The asker went away, and with it whom to answer.
				return
			}
		}
		page.Says = Says()
		rc.answer(w, f, "", http.StatusOK, page)
		return
	}
}

// waitOf is how long a pull asks to be held: nothing without wait, which
// is every pull of a yardr from before there was one, and of a pass that
// only wants what is there.
func waitOf(q url.Values) time.Duration {
	if !q.Has("wait") {
		return 0
	}
	secs, err := strconv.Atoi(q.Get("wait"))
	if err != nil {
		return defaultWait
	}
	return min(max(time.Duration(secs)*time.Second, 0), maxWait)
}

// hold makes the pull of the peer with key the one it holds open, and ends
// the one it held before: a peer has one, and what it asked last is what
// it waits on. nil when maxHeld pulls are held already.
func (rc *Receiver) hold(key string) chan struct{} {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if before, ok := rc.held[key]; ok {
		close(before)
	} else if len(rc.held) >= rc.maxHeld {
		return nil
	}
	ended := make(chan struct{})
	rc.held[key] = ended
	return ended
}

// release forgets a held pull that is over, unless the peer's next has
// taken its place.
func (rc *Receiver) release(key string, ended chan struct{}) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.held[key] == ended {
		delete(rc.held, key)
	}
}

// Held is how many pulls are held open now.
func (rc *Receiver) Held() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.held)
}

// refusals reads what a question says its asker did not take. What is not
// a sequence number with a reason is passed by: the message it meant is
// then ended as sent, as before there were refusals.
func refusals(q string) map[uint64]string {
	var out map[uint64]string
	for _, part := range strings.Split(q, ",") {
		seq, reason, ok := strings.Cut(part, ":")
		n, err := strconv.ParseUint(seq, 10, 64)
		if !ok || err != nil || len(out) >= maxRefusals {
			continue
		}
		if out == nil {
			out = map[uint64]string{}
		}
		out[n] = reason
	}
	return out
}

// answer writes a signed answer to a peer's request, and counts it under
// reason when it is not a plain yes. Only an answer to a peer is signed:
// who is not one gets reject's. An answer that is fields of its own says
// what this build understands too, as a page does.
func (rc *Receiver) answer(w http.ResponseWriter, f *from, reason string, code int, v any) {
	if reason != "" {
		rc.count(reason)
	}
	if fields, ok := v.(map[string]any); ok {
		fields["says"] = Says()
	}
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(yardkey.HeaderSig, yardkey.SignResponse(f.key, code, f.sig, body))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func (rc *Receiver) reject(w http.ResponseWriter, peer, reason string, code int, err error) {
	rc.count(reason)
	rc.log.Warn("peer request rejected", "peer", peer, "reason", reason, "status", code, "err", err)
	http.Error(w, http.StatusText(code), code)
}

func (rc *Receiver) count(reason string) {
	rc.mu.Lock()
	rc.rejected[reason]++
	rc.mu.Unlock()
}
