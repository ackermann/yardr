package standin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// Edge is the edge of design yardr-hosted-050n as far as a yard calls it,
// written from the design and not from the router, which is another
// repository's: it serves the key it signs with at GET /edge/key, takes a
// yard's enrolment at POST /edge/enrol (answering a code, and the handle once
// a person has approved the yard), and drops a yard's row at POST
// /edge/disconnect. A request is checked as a peer's is, signed by the yard
// whose key it names, to this edge's key, within yardkey.MaxSkew; every
// answer is signed with the key the stand-in is told to sign with.
//
// It is an http.Handler: a test serves it with httptest.NewTLSServer, whose
// client trusts its certificate, or httptest.NewServer for the loopback
// stand-in that plain http is for.
type Edge struct {
	key ed25519.PrivateKey

	mu    sync.Mutex
	b     EdgeBehaviour
	state EdgeState
	// asked counts the enrolling calls of each yard since it last asked to
	// be forgotten: a yard whose row is dropped is enrolled afresh.
	asked map[string]int
}

// EdgeBehaviour is what a test changes about an Edge, with Behave.
type EdgeBehaviour struct {
	// Served is the key GET /edge/key answers, and Signer the key that signs
	// the answers: the edge's own until a test serves or signs with another,
	// as an edge whose key changed would, or something that is not the edge.
	// ServedAfterApproval, when not nil, is the key served from the moment a
	// yard has been answered with its handle.
	Served, Signer, ServedAfterApproval ed25519.PrivateKey
	// ApproveAfter is how many enrolling calls of a yard get a code before
	// the person has approved it and a call is answered with its Handle and
	// Email. A yard the edge knows already is approved at once: zero.
	ApproveAfter  int
	Handle, Email string
	// Now is the time a request is judged by; nil is the clock.
	Now func() time.Time
	// Refuse, when not zero, is the status every enrolling call is refused
	// with, the reason being "expired"; DisconnectStatus is what a
	// disconnecting yard is answered, 200 when zero.
	Refuse, DisconnectStatus int
}

// EdgeEnrol is one enrolling call as the edge took it.
type EdgeEnrol struct {
	// Key and Name are the yard's, from the body; Time and Sig the
	// signature's own.
	Key, Name, Time, Sig string
}

// EdgeState is what the edge has been asked.
type EdgeState struct {
	KeyFetches int
	Approved   bool
	Enrols     []EdgeEnrol
	// Drops are the keys of the yards that asked to be forgotten.
	Drops []string
}

// NewEdge makes an edge with a key of its own, which approves a yard on its
// second enrolling call as mckean, and answers every call as it should.
func NewEdge() (*Edge, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Edge{key: key, asked: map[string]int{},
		b: EdgeBehaviour{Served: key, Signer: key, ApproveAfter: 1, Handle: "mckean", Email: "chris@mckean.co"}}, nil
}

// PublicKey is the edge's own key as yards write one.
func (e *Edge) PublicKey() string { return yardkey.Public(e.key) }

// Behave changes the edge's behaviour: f is given it to change, under the
// edge's lock.
func (e *Edge) Behave(f func(*EdgeBehaviour)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f(&e.b)
}

// State is a copy of what the edge has been asked so far.
func (e *Edge) State() EdgeState {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.state
	s.Enrols = append([]EdgeEnrol(nil), s.Enrols...)
	s.Drops = append([]string(nil), s.Drops...)
	return s
}

func (e *Edge) behaviour() EdgeBehaviour {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.b
}

func (e *Edge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
	case "GET /edge/key":
		e.mu.Lock()
		e.state.KeyFetches++
		served := e.b.Served
		if e.state.Approved && e.b.ServedAfterApproval != nil {
			served = e.b.ServedAfterApproval
		}
		e.mu.Unlock()
		fmt.Fprintln(w, yardkey.Public(served))
	case "POST /edge/enrol":
		e.enrol(w, r)
	case "POST /edge/disconnect":
		e.disconnect(w, r)
	default:
		http.NotFound(w, r)
	}
}

// signed reads a request and checks that the yard it names signed it to this
// edge; when it did not, the stand-in has answered 401 and ok is false.
func (e *Edge) signed(w http.ResponseWriter, r *http.Request) (body []byte, ok bool) {
	body, _ = io.ReadAll(io.LimitReader(r.Body, 4<<10))
	now := time.Now
	if b := e.behaviour(); b.Now != nil {
		now = b.Now
	}
	sig := r.Header.Get(yardkey.HeaderSig)
	err := yardkey.VerifyRequest(r.Header.Get(yardkey.HeaderKey), e.PublicKey(), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), sig, body, now())
	if err != nil {
		e.answer(w, r, http.StatusUnauthorized, map[string]string{"refused": err.Error()})
		return nil, false
	}
	return body, true
}

// answer writes v as the answer to r, signed as the stand-in is to sign.
func (e *Edge) answer(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set(yardkey.HeaderSig, yardkey.SignResponse(e.behaviour().Signer, status, r.Header.Get(yardkey.HeaderSig), body))
	w.WriteHeader(status)
	w.Write(body)
}

func (e *Edge) enrol(w http.ResponseWriter, r *http.Request) {
	body, ok := e.signed(w, r)
	if !ok {
		return
	}
	var in struct{ Key, Name string }
	if json.Unmarshal(body, &in) != nil || in.Key != r.Header.Get(yardkey.HeaderKey) || in.Name == "" {
		e.answer(w, r, http.StatusBadRequest, map[string]string{"refused": "an enrolment names the yard's key, the one that signed it, and its name"})
		return
	}
	e.mu.Lock()
	e.state.Enrols = append(e.state.Enrols, EdgeEnrol{in.Key, in.Name, r.Header.Get(yardkey.HeaderTime), r.Header.Get(yardkey.HeaderSig)})
	e.asked[in.Key]++
	calls, b := e.asked[in.Key], e.b
	e.mu.Unlock()
	switch {
	case b.Refuse != 0:
		e.answer(w, r, b.Refuse, map[string]string{"refused": "expired"})
	case calls > b.ApproveAfter:
		e.mu.Lock()
		e.state.Approved = true
		e.mu.Unlock()
		e.answer(w, r, http.StatusOK, map[string]string{"handle": b.Handle, "email": b.Email})
	default:
		e.answer(w, r, http.StatusOK, map[string]any{"code": "WQ4F-7K2M", "expires": time.Now().Add(10 * time.Minute)})
	}
}

func (e *Edge) disconnect(w http.ResponseWriter, r *http.Request) {
	if _, ok := e.signed(w, r); !ok {
		return
	}
	e.mu.Lock()
	e.state.Drops = append(e.state.Drops, r.Header.Get(yardkey.HeaderKey))
	delete(e.asked, r.Header.Get(yardkey.HeaderKey))
	status := e.b.DisconnectStatus
	e.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	e.answer(w, r, status, map[string]string{})
}
