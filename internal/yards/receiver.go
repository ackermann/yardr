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
	"strconv"
	"sync"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// Path is where a yard takes its peers' requests: POST for a message, GET
// with ?after=<seq> for what waits for the asker.
const Path = "/peers/messages"

// maxRequest bounds a posted message: its body, and room for the rest.
const maxRequest = store.MaxPeerBody + 4<<10

// Store is what the receiver needs of the yard: the store itself, in the
// process that owns it. None of the three is on the socket.
type Store interface {
	PeerByKey(ctx context.Context, key string) (*store.Peer, error)
	PeerReceive(ctx context.Context, name string, msgs []store.PeerMessage) ([]store.PeerTaken, error)
	PeerHandOver(ctx context.Context, name string, after uint64) (*store.PeerPage, error)
}

// Receiver serves Path and nothing else.
type Receiver struct {
	st  Store
	key string // the file of this yard's key
	log *slog.Logger
	now func() time.Time

	mu       sync.Mutex
	rejected map[string]int
}

// NewReceiver serves the peers of the yard whose key is in the file
// keyFile. The key is read on every request, as a hook's secret is.
func NewReceiver(st Store, keyFile string, log *slog.Logger) *Receiver {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Receiver{st: st, key: keyFile, log: log, now: time.Now, rejected: map[string]int{}}
}

// SetClock replaces the clock (tests).
func (rc *Receiver) SetClock(now func() time.Time) { rc.now = now }

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
	return &from{peer: p, key: key, sig: sig, body: body}, true
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
	taken, err := rc.st.PeerReceive(r.Context(), f.peer.Name, []store.PeerMessage{m})
	switch {
	case errors.Is(err, store.ErrNotFound): // removed since verify
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
	page, err := rc.st.PeerHandOver(r.Context(), f.peer.Name, after)
	if errors.Is(err, store.ErrNotFound) {
		rc.reject(w, f.peer.Name, "unknown_peer", http.StatusUnauthorized, err)
		return
	} else if err != nil {
		rc.reject(w, f.peer.Name, "store", http.StatusInternalServerError, err)
		return
	}
	rc.answer(w, f, "", http.StatusOK, page)
}

// answer writes a signed answer to a peer's request, and counts it under
// reason when it is not a plain yes. Only an answer to a peer is signed:
// who is not one gets reject's.
func (rc *Receiver) answer(w http.ResponseWriter, f *from, reason string, code int, v any) {
	if reason != "" {
		rc.count(reason)
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
