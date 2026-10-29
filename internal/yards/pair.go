package yards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// A pairing's three requests (internal/pairing, docs/peers.md) arrive
// beside the peers' messages, from a yard that is no peer yet. The first
// two are not signed: what they are trusted for is the code, and then the
// check the two operators compare. The third is signed with the key the
// calling yard revealed, and answered signed with this yard's, which is
// what shows that each holds the private key it gave.

// maxPair bounds a pairing request and its answer: a few short fields.
const maxPair = 4 << 10

// PairStore is the host's side of a pairing, in the process that owns the
// store. None of it is on the socket.
type PairStore interface {
	PairHello(ctx context.Context, h pairing.Hello) (*pairing.Offer, error)
	PairReveal(ctx context.Context, r pairing.Reveal) (session string, err error)
	PairCaller(ctx context.Context, session string) (key string, err error)
	PairConfirmed(ctx context.Context, session string, yes bool) (*pairing.Outcome, error)
}

// refusal is the body of an answer that takes nothing.
type refusal struct {
	Refused string `json:"refused"`
}

// refusalStatus is the status a refusal is answered with. The reason is in
// the body; the status tells a caller of another build what kind it is.
func refusalStatus(reason string) int {
	switch reason {
	case pairing.WrongCode:
		return http.StatusForbidden
	case pairing.NoPairing:
		return http.StatusNotFound
	case pairing.TimedOut:
		return http.StatusGone
	}
	return http.StatusConflict
}

// pairBody reads a pairing request's body into v.
func (rc *Receiver) pairBody(w http.ResponseWriter, r *http.Request, v any) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPair))
	if err != nil {
		rc.reject(w, "", "pair_read", http.StatusBadRequest, err)
		return nil, false
	}
	if err := json.Unmarshal(body, v); err != nil {
		rc.reject(w, "", "pair_bad_request", http.StatusBadRequest, err)
		return nil, false
	}
	return body, true
}

// pairFailed answers a pairing request the store did not take: a refusal
// with its reason, or 400 for what is no message of a pairing. Neither the
// answer nor the log has anything of the code: the store's errors do not.
func (rc *Receiver) pairFailed(w http.ResponseWriter, err error) {
	var no *pairing.Refused
	if !errors.As(err, &no) {
		rc.reject(w, "", "pair_bad_request", http.StatusBadRequest, err)
		return
	}
	rc.log.Warn("pairing call refused", "reason", no.Reason)
	writeJSON(w, refusalStatus(no.Reason), refusal{Refused: no.Reason})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (rc *Receiver) pairHello(w http.ResponseWriter, r *http.Request) {
	var h pairing.Hello
	if _, ok := rc.pairBody(w, r, &h); !ok {
		return
	}
	offer, err := rc.pair.PairHello(r.Context(), h)
	if err != nil {
		rc.pairFailed(w, err)
		return
	}
	writeJSON(w, http.StatusOK, offer)
}

func (rc *Receiver) pairReveal(w http.ResponseWriter, r *http.Request) {
	var rv pairing.Reveal
	if _, ok := rc.pairBody(w, r, &rv); !ok {
		return
	}
	session, err := rc.pair.PairReveal(r.Context(), rv)
	if err != nil {
		rc.pairFailed(w, err)
		return
	}
	rc.log.Info("pairing: a yard called with the code", "calls", rv.Name)
	writeJSON(w, http.StatusOK, map[string]string{"session": session})
}

// pairConfirm takes the calling yard's word on the check. It is signed as a
// request between peers is, with the key the pairing was called with: a
// word that is not signed so is no one's, and answered 401 as any request
// that is no peer's.
func (rc *Receiver) pairConfirm(w http.ResponseWriter, r *http.Request) {
	var a pairing.Answer
	body, ok := rc.pairBody(w, r, &a)
	if !ok {
		return
	}
	key, err := yardkey.Load(rc.key)
	if err != nil {
		rc.reject(w, "", "no_key", http.StatusUnauthorized, err)
		return
	}
	caller, err := rc.pair.PairCaller(r.Context(), a.Session)
	if err != nil {
		rc.pairFailed(w, err)
		return
	}
	sig := r.Header.Get(yardkey.HeaderSig)
	if r.Header.Get(yardkey.HeaderKey) != caller {
		err = fmt.Errorf("%w: not the key the pairing was called with", yardkey.ErrSignature)
	} else {
		err = yardkey.VerifyRequest(caller, yardkey.Public(key), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), sig, body, rc.now())
	}
	if err != nil {
		rc.reject(w, "", "pair_unauthorized", http.StatusUnauthorized, err)
		return
	}
	out, err := rc.pair.PairConfirmed(r.Context(), a.Session, a.Confirm)
	if err != nil {
		rc.pairFailed(w, err)
		return
	}
	rc.answer(w, &from{key: key, sig: sig}, http.StatusOK, out)
}

// Called is a pairing as the calling yard has it once the other has
// answered: everything that crossed, from which it computes the check.
type Called struct {
	pairing.Transcript
	// URL is where the other yard was called, which is where the
	// confirmations go and where the link then calls it.
	URL string `json:"url"`
}

// pairPost posts v to a yard that is no peer yet, and reads its answer
// into out. A refusal is a *pairing.Refused.
func (c *Caller) pairPost(ctx context.Context, url string, v, out any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// No connection is kept, as for a peer's own requests (do).
	req.Close = true
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxPair))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return pairRefusal(resp.StatusCode, answer)
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return errors.New("what answered there is not a yard that pairs")
	}
	return nil
}

// pairRefusal reads an answer that is not a yes. The reason is the other
// yard's text: a caller says only the ones it knows, in its own words.
func pairRefusal(status int, answer []byte) error {
	var no refusal
	if json.Unmarshal(answer, &no) == nil && no.Refused != "" {
		return &pairing.Refused{Reason: no.Refused}
	}
	if status == http.StatusNotFound {
		// A yardr from before pairing, or something that is no yard.
		return fmt.Errorf("nothing there takes a pairing (it answered %d): is it a yard's listener, and its yardr one that pairs?", status)
	}
	return fmt.Errorf("the other yard answered %d", status)
}

func (c *Caller) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// ErrNotTheCode is an answer to a pairing's first message that was not
// made with the code. The yard itself refuses a wrong code in so many
// words; this is something else answering in its place.
var ErrNotTheCode = errors.New("the answer was not made with the code: something other than the yard that shows it answered")

// PairCall calls the yard at url with the code its operator read out, as
// the yard named name: the two messages of a pairing before anyone is
// asked. It returns what crossed. Nothing is saved on either side yet: the
// operators compare Check first, and PairAnswer says what this one said.
//
// accept, when given, is asked between the two, with what the other yard
// said it is: an error ends the call there, as accept's, before this yard
// has revealed itself. The other yard's pairing then stays open, and its
// operator has been asked nothing.
func (c *Caller) PairCall(ctx context.Context, url, code, name string, accept func(host pairing.Party) error) (*Called, error) {
	me := pairing.Party{Nonce: pairing.NewNonce(), Name: name, Key: yardkey.Public(c.Key)}
	if err := me.Valid(); err != nil {
		return nil, err
	}
	t := pairing.Transcript{Commit: pairing.Commit(me), Joiner: me}
	key := pairing.Key(code, t.Commit)
	var offer pairing.Offer
	if err := c.pairPost(ctx, url+pairing.PathHello, pairing.NewHello(key, me), &offer); err != nil {
		return nil, err
	}
	if knows, err := offer.Check(key, t.Commit); err != nil {
		return nil, err
	} else if !knows {
		return nil, ErrNotTheCode
	}
	t.Host = offer.Party
	if accept != nil {
		if err := accept(t.Host); err != nil {
			return nil, err
		}
	}
	var took struct {
		Session string `json:"session"`
	}
	if err := c.pairPost(ctx, url+pairing.PathReveal, pairing.NewReveal(key, t), &took); err != nil {
		return nil, err
	}
	return &Called{Transcript: t, URL: url}, nil
}

// PairAnswer tells the yard that was called what this yard's operator said
// of the check, signed with this yard's key, and returns how the pairing
// stands there, by an answer signed with the key that yard gave. With yes
// it is asked again until the state is no longer pairing.Waiting; this
// yard saves the other only on pairing.Confirmed.
func (c *Caller) PairAnswer(ctx context.Context, p *Called, yes bool) (*pairing.Outcome, error) {
	body, err := json.Marshal(pairing.Answer{Session: p.Session(), Confirm: yes})
	if err != nil {
		return nil, err
	}
	host := store.Peer{Name: p.Host.Name, Key: p.Host.Key, URL: p.URL}
	status, answer, _, _, err := c.do(ctx, host, http.MethodPost, pairing.PathConfirm, body)
	if errors.Is(err, ErrUnsigned) {
		// do's words are a peer's; these two are not peers yet.
		return nil, fmt.Errorf("%s's answer was %w, the one it gave in the pairing: the pairing is gone there, the clocks are more than %s apart, or something between the two answered",
			host.Name, ErrUnsigned, yardkey.MaxSkew)
	} else if err != nil {
		return nil, err
	}
	var out pairing.Outcome
	if status != http.StatusOK || json.Unmarshal(answer, &out) != nil {
		return nil, fmt.Errorf("the other yard answered %d", status)
	}
	return &out, nil
}

// PairWait asks the yard that was called, every interval, until both
// operators have confirmed or the pairing has ended, and returns which.
func (c *Caller) PairWait(ctx context.Context, p *Called, interval time.Duration) (*pairing.Outcome, error) {
	for {
		out, err := c.PairAnswer(ctx, p, true)
		if err != nil || out.State != pairing.Waiting {
			return out, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
