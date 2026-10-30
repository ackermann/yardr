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
	"bytes"
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
// &refused=<seq>:<reason>,... for what the asker got and did not take (a
// refusal over the hour adds its limit and when it ends, as unix nanoseconds:
// <seq>:rate_limited:<limit>:<until>), and &wait=<seconds> to have the answer
// held until there is something to hand over (see hold).
//
// Each request, and each signed answer, also says what the build of the
// yard it is from understands (store.PeerSays): "says" beside a posted
// message's fields and in an answer's, &understands=<kind>,...&yardr=<build>
// on a GET. With it goes the roster, whom of the yard the other may see
// and write to: "roster" in says, &roster=<the same JSON> on a GET. Each is
// under the signature, and each is one more field or parameter, which a
// yardr from before it was said passes by.
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

// SaysTo is what this yard states to the peer p: what its build says to
// any (Says), and the roster of those it exposes to p, which the store
// filled in on the peer it handed out.
func SaysTo(p store.Peer) *store.PeerSays {
	says := Says()
	says.Roster = p.Roster
	return says
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
// maxFileRequest bounds one from a yard that names a peer's key, which may
// be a file: as large as any yard sends one (store.MaxPeerFile), so that a
// file this yard does not take is read and refused with its reason, signed,
// and the sender gives it up. A file cut off unread would be posted again
// for most of a day.
//
// The grant is on the key the request claims, before its signature is
// checked (the signature covers the body's sha256, so checking it needs the
// whole body): a stranger who knows a peer's key can make the receiver read
// megabytes it will then refuse. That read is accepted, and bounded below:
// a body past maxRequest is read only behind one of maxLargeReads slots.
// maxLargeReads bounds the bodies past maxRequest read at once, over all
// peers: four concurrent ~43MB reads hold ~172MB at most inside the
// listener's timeouts, and a refused large one is reposted by its sender.
const (
	maxRequest     = store.MaxPeerBody + 4<<10
	maxFileRequest = store.MaxPeerFileBody + 4<<10
	maxLargeReads  = 4
)

// Store is what the receiver needs of the yard: the store itself, in the
// process that owns it. None of the four is on the socket. Those that take
// something of a request are given the key its signature was checked
// against, with the name PeerByKey found for it: the name alone may be
// another yard's by then.
type Store interface {
	PeerByKey(ctx context.Context, key string) (*store.Peer, error)
	PeerHeard(ctx context.Context, name, key string, says *store.PeerSays) error
	PeerReceive(ctx context.Context, name, key string, msgs []store.PeerMessage) ([]store.PeerTaken, error)
	PeerHandOver(ctx context.Context, name, key string, after uint64, refused map[uint64]store.Refusal) (*store.PeerPage, error)
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

	mu sync.Mutex
	// held is peer key -> the pull that peer holds open, closed to end it,
	// and maxHeld how many there may be.
	held    map[string]chan struct{}
	maxHeld int
	// largeReads is how many large request bodies are being read now, and
	// maxLargeReads how many may be at once.
	largeReads    int
	maxLargeReads int
}

// NewReceiver serves the peers of the yard whose key is in the file
// keyFile. The key is read on every request, as a hook's secret is.
func NewReceiver(st Store, keyFile string, log *slog.Logger) *Receiver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	rc := &Receiver{st: st, key: keyFile, log: log, now: time.Now, held: map[string]chan struct{}{}, maxHeld: maxHeld, maxLargeReads: maxLargeReads}
	// The store is both; a test's stand-in for its messages may be only one.
	rc.pair, _ = st.(PairStore)
	return rc
}

// SetClock replaces the clock. Tests set it: the daemon signs with the
// test's clock, and the receiver checks against it.
func (rc *Receiver) SetClock(now func() time.Time) { rc.now = now }

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
	// Whose the request says it is decides only how much of it is read:
	// megabytes for a key a link has, and not for anyone who can reach the
	// listener. Whether it is that yard's is the signature's to say, below.
	p, known := rc.st.PeerByKey(r.Context(), r.Header.Get(yardkey.HeaderKey))
	var body []byte
	if known == nil {
		var ok bool
		if body, ok = rc.readLarge(w, r); !ok {
			return nil, false
		}
	} else {
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, int64(maxRequest)))
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			rc.reject(w, "", "too_large", http.StatusRequestEntityTooLarge, err)
			return nil, false
		} else if err != nil {
			rc.reject(w, "", "read", http.StatusBadRequest, err)
			return nil, false
		}
	}
	unauthorized := func(peer, reason string, err error) (*from, bool) {
		rc.reject(w, peer, reason, http.StatusUnauthorized, err)
		return nil, false
	}
	// Without a key of its own the yard can sign no answer, and has given
	// no one a key to add it by.
	key, err := store.Load(rc.key)
	if err != nil {
		return unauthorized("", "no_key", err)
	}
	if err = known; errors.Is(err, store.ErrNotFound) {
		return unauthorized("", "unknown_peer", err)
	} else if err != nil {
		rc.reject(w, "", "store", http.StatusInternalServerError, err)
		return nil, false
	}
	sig := r.Header.Get(yardkey.HeaderSig)
	err = yardkey.VerifyRequest(p.Key, store.Public(key), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), sig, body, rc.now())
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

// readLarge reads a request that names a peer's key, which may be a file:
// up to maxFileRequest, in two steps. The first maxRequest+1 bytes are read
// with no slot, so small traffic goes exactly as before; only a body past
// maxRequest takes one of the large-read slots before its tail is read. With
// no slot free the request is refused 503, so its sender keeps it and posts
// it again. False means the request was answered here.
func (rc *Receiver) readLarge(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	head := make([]byte, maxRequest+1)
	n, err := io.ReadFull(r.Body, head)
	switch err {
	case nil:
		// Past maxRequest: the tail goes behind the bound, below.
	case io.EOF, io.ErrUnexpectedEOF:
		return head[:n], true
	default:
		rc.reject(w, "", "read", http.StatusBadRequest, err)
		return nil, false
	}
	if !rc.takeLarge() {
		rc.reject(w, "", "too_many_large", http.StatusServiceUnavailable, errors.New("too many large requests in flight"))
		return nil, false
	}
	defer rc.releaseLarge()
	body := bytes.NewBuffer(head)
	if r.ContentLength > int64(body.Len()) && r.ContentLength <= int64(maxFileRequest) {
		body.Grow(int(r.ContentLength) - body.Len())
	}
	_, err = io.Copy(body, http.MaxBytesReader(w, r.Body, int64(maxFileRequest-body.Len())))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		rc.reject(w, "", "too_large", http.StatusRequestEntityTooLarge, err)
		return nil, false
	} else if err != nil {
		rc.reject(w, "", "read", http.StatusBadRequest, err)
		return nil, false
	}
	return body.Bytes(), true
}

// takeLarge takes a large-read slot, false when none is free. releaseLarge
// gives one back when its read ends.
func (rc *Receiver) takeLarge() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.largeReads >= rc.maxLargeReads {
		return false
	}
	rc.largeReads++
	return true
}

func (rc *Receiver) releaseLarge() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.largeReads--
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
		// A roster that does not read is none: what it says is kept or
		// not, and the question is answered either way.
		if roster := q.Get("roster"); roster != "" {
			_ = json.Unmarshal([]byte(roster), &says.Roster)
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
		rc.answer(w, f, http.StatusBadRequest, map[string]any{"error": "not a message"})
		return
	}
	m.Seq = 0 // the sender's own; a posted message has no place here
	taken, err := rc.st.PeerReceive(r.Context(), f.peer.Name, f.peer.Key, []store.PeerMessage{m})
	switch {
	case errors.Is(err, store.ErrPeerNotFound): // removed since verify, or its name another yard's
		rc.reject(w, f.peer.Name, "unknown_peer", http.StatusUnauthorized, err)
	case errors.Is(err, store.ErrNotMessage):
		// Its words name nothing of the yard.
		rc.log.Warn("peer message rejected", "peer", f.peer.Name, "err", err)
		rc.answer(w, f, http.StatusBadRequest, map[string]any{"error": "not a message"})
	case err != nil:
		// No fault of the message: this yard could not keep it (a full
		// disk, a write that failed). Signed, so the sender knows it is
		// this yard's word, and 500, so it keeps the message and posts it
		// again: a 400 it would take as refused for good.
		rc.log.Warn("peer message not kept", "peer", f.peer.Name, "kind", m.Kind, "id", m.ID, "err", err)
		rc.answer(w, f, http.StatusInternalServerError, map[string]any{"error": "not kept"})
	case taken[0].Refused != "":
		rc.log.Warn("peer message refused", "peer", f.peer.Name, "kind", m.Kind, "id", m.ID, "reason", taken[0].Refused)
		body := map[string]any{"refused": taken[0].Refused}
		if taken[0].Limit > 0 && taken[0].Until != nil {
			body["limit"], body["until"] = taken[0].Limit, taken[0].Until
		}
		rc.answer(w, f, http.StatusForbidden, body)
	case taken[0].Duplicate:
		rc.log.Info("peer message dropped as duplicate", "peer", f.peer.Name, "id", m.ID)
		rc.answer(w, f, http.StatusOK, map[string]any{"duplicate": true})
	default:
		rc.answer(w, f, http.StatusOK, map[string]any{"taken": true})
	}
}

func (rc *Receiver) get(w http.ResponseWriter, r *http.Request) {
	f, ok := rc.verify(w, r)
	if !ok {
		return
	}
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if err != nil {
		rc.answer(w, f, http.StatusBadRequest, map[string]any{"error": "after wants a sequence number"})
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
	} else {
		// A peer's next question ends the one it held open even when the
		// new question asks for no hold (as an older caller does).
		rc.endHeld(f.peer.Key)
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
		if ended != nil {
			select {
			case <-ended:
				page.Messages = nil
			default:
			}
		}
		// A peer this yard posts to is not kept waiting for what will
		// never be handed over.
		held := ended != nil && !page.Push && len(page.Messages) == 0
		if held {
			select {
			case _, open := <-wake:
				if open {
					// If a new question ended this one at the same time as
					// an outbox write, the superseded question still answers
					// empty.
					select {
					case <-ended:
						page.Messages = nil
						page.Says = SaysTo(*f.peer)
						rc.answer(w, f, http.StatusOK, page)
						return
					default:
					}
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
		page.Says = SaysTo(*f.peer)
		rc.answer(w, f, http.StatusOK, page)
		return
	}
}

// endHeld ends and forgets the previous held pull for key. Its handler
// owns the response and will send its already-empty page before returning.
func (rc *Receiver) endHeld(key string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if before, ok := rc.held[key]; ok {
		delete(rc.held, key)
		close(before)
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
	if secs <= 0 {
		return 0
	}
	if secs >= int(maxWait/time.Second) {
		return maxWait
	}
	return time.Duration(secs) * time.Second
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

// refusals reads what a question says its asker did not take. What is not
// a sequence number with a reason is passed by: the message it meant is
// then ended as sent, as before there were refusals.
func refusals(q string) map[uint64]store.Refusal {
	var out map[uint64]store.Refusal
	for _, part := range strings.Split(q, ",") {
		seq, rest, ok := strings.Cut(part, ":")
		n, err := strconv.ParseUint(seq, 10, 64)
		if !ok || err != nil || len(out) >= maxRefusals {
			continue
		}
		if out == nil {
			out = map[uint64]store.Refusal{}
		}
		out[n] = parseRefusal(rest)
	}
	return out
}

// parseRefusal reads one refusal: the reason, with the hour's limit and end
// after it for one over the hour (`rate_limited:30:1790000000000000000`, the
// end as unix nanoseconds). If a valid limit and end do not follow the rate
// refusal, the whole rest is the reason, as before.
func parseRefusal(rest string) store.Refusal {
	no := store.Refusal{Reason: rest}
	reason, fields, ok := strings.Cut(rest, ":")
	if !ok || reason != store.RefusedRate {
		return no
	}
	limitText, untilText, ok := strings.Cut(fields, ":")
	if !ok || strings.Contains(untilText, ":") {
		return no
	}
	limit, err := strconv.Atoi(limitText)
	until, err2 := strconv.ParseInt(untilText, 10, 64)
	if err != nil || err2 != nil || limit <= 0 || until <= 0 {
		return no
	}
	no.Reason, no.Limit = reason, limit
	at := time.Unix(0, until)
	no.Until = &at
	return no
}

// answer writes a signed answer to a peer's request. Only an answer to a
// peer is signed: who is not one gets reject's. An answer that is fields
// of its own says what this yard states to the peer too (SaysTo), as a
// page does.
func (rc *Receiver) answer(w http.ResponseWriter, f *from, code int, v any) {
	if fields, ok := v.(map[string]any); ok {
		fields["says"] = SaysTo(*f.peer)
	}
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(yardkey.HeaderSig, store.SignResponse(f.key, code, f.sig, body))
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func (rc *Receiver) reject(w http.ResponseWriter, peer, reason string, code int, err error) {
	rc.log.Warn("peer request rejected", "peer", peer, "reason", reason, "status", code, "err", err)
	http.Error(w, http.StatusText(code), code)
}
