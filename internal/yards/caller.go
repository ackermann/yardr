package yards

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// maxAnswer bounds what is read of a peer's answer: a page of messages at
// its largest, which is one with a single file as large as any yard sends,
// with room for the rest.
const maxAnswer = store.MaxPeerFileBody + 1<<20

// ErrRefused is the peer's signed word that it does not take the message:
// its link does not carry the kind in, or it is mail the yard refuses
// (store.PeerRefusal). Trying again changes nothing.
var ErrRefused = errors.New("refused by the peer")

// Refused is the peer's signed word that it takes no more mail this hour:
// its refusal over the link's limit, with what the link takes and when the
// hour ends. Trying again changes nothing; the sender is told both with it.
type Refused struct {
	Peer  string
	Limit int
	Until time.Time
}

func (e *Refused) Error() string {
	return fmt.Sprintf("peer %s: %s: %s", e.Peer, ErrRefused, store.PeerRefusalRate(e.Limit, e.Until))
}

func (e *Refused) Is(target error) bool { return target == ErrRefused }

// ErrUnsigned is an answer that is not signed with the key of the yard that
// was asked: what it says is anyone's.
var ErrUnsigned = errors.New("not signed with its key")

// HoldHeader is the edge's word on a peer's pull it ended itself: 204 with
// this header set to "capped" and no body, when the yard held the pull past
// the edge's wait (yardr-hosted's HoldHeader). No body means no signature
// surface: the puller asks again rather than crying hostile.
const HoldHeader = "X-Edge-Hold"

// ErrHoldCapped is a pull the edge ended itself (HoldHeader): no answer was
// lost, and the puller asks again.
var ErrHoldCapped = errors.New("the edge ended the hold")

// Caller makes this yard's requests to its peers, signed with its key.
type Caller struct {
	Key  ed25519.PrivateKey
	HTTP *http.Client     // nil: http.DefaultClient
	Now  func() time.Time // nil: time.Now
	// Heard keeps what a peer says in a signed POST answer. Pull answers
	// are handed to the store with their signature in PeerBatch instead.
	Heard func(context.Context, store.Peer, store.PeerAnswer)
}

// do makes one signed request to p and returns the status and body of an
// answer p signed, with the request's signature. An answer that is not
// signed by p's key is an error whatever it says: anyone on the way can
// write a 200.
func (c *Caller) do(ctx context.Context, p store.Peer, method, path string, body []byte) (status int, answer []byte, reqSig string, sig string, err error) {
	if p.URL == "" {
		return 0, nil, "", "", fmt.Errorf("peer %s has no URL: it is not called, it calls", p.Name)
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	req, err := http.NewRequestWithContext(ctx, method, p.URL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, "", "", err
	}
	ts, reqSig := yardkey.SignRequest(c.Key, p.Key, method, path, now(), body)
	req.Header.Set(yardkey.HeaderKey, store.Public(c.Key))
	req.Header.Set(yardkey.HeaderTime, ts)
	req.Header.Set(yardkey.HeaderSig, reqSig)
	// No connection is kept for the next request: a pooled one reused after
	// the machine slept carries a signature from before it, which the peer
	// answers 401, and over the edge the same reuse would hit the router.
	// The peer equivalent of a declared source's DisableKeepAlives
	// (internal/source).
	req.Close = true
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", "", err
	}
	defer resp.Body.Close()
	answer, err = io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return 0, nil, "", "", err
	}
	if len(answer) > maxAnswer {
		return 0, nil, "", "", fmt.Errorf("peer %s: its answer is larger than %d bytes", p.Name, maxAnswer)
	}
	if resp.StatusCode == http.StatusNoContent && resp.Header.Get(HoldHeader) == "capped" {
		// The edge ended a held pull itself: no answer was lost, so it is
		// said as its own error rather than as anyone's unsigned text.
		// Nothing of it is kept: it carries no body and no signature.
		return 0, nil, "", "", fmt.Errorf("peer %s: %w", p.Name, ErrHoldCapped)
	}
	sig = resp.Header.Get(yardkey.HeaderSig)
	if err := store.VerifyResponse(p.Key, resp.StatusCode, reqSig, sig, answer); err != nil {
		// What an unsigned answer says is anyone's text: only its status
		// is passed on.
		return 0, nil, "", "", fmt.Errorf("peer %s answered %d, %w: this yard is not a peer of it there, the clocks are more than %s apart, or something between the two answered",
			p.Name, resp.StatusCode, ErrUnsigned, yardkey.MaxSkew)
	}
	return resp.StatusCode, answer, reqSig, sig, nil
}

// Push posts m to p. nil means p has it (taken now, or before: the same id
// twice is taken once). ErrRefused means p will not take it; any other
// error, that it is worth another try: p not reached, or p's 500 for a
// message it could not keep.
func (c *Caller) Push(ctx context.Context, p store.Peer, m store.PeerMessage) error {
	m.Seq = 0
	// With what this yard states to p beside the message's own fields.
	body, err := json.Marshal(struct {
		store.PeerMessage
		Says *store.PeerSays `json:"says"`
	}{m, SaysTo(p)})
	if err != nil {
		return err
	}
	status, answer, reqSig, sig, err := c.do(ctx, p, http.MethodPost, Path, body)
	if err == nil && c.Heard != nil {
		// The store checks the signed answer before keeping what it says.
		// An older yard's valid answer has no says, and clears a stale
		// statement just like an older request does.
		c.Heard(ctx, p, store.PeerAnswer{Status: status, RequestSig: reqSig, Signature: sig, Body: answer})
	}
	switch {
	case err != nil:
		return err
	case status == http.StatusOK:
		return nil
	case status == http.StatusForbidden:
		// The reason is the peer's text: only one this yard knows is said.
		// Mail refused over the hour carries its limit and when it ends.
		var no struct {
			Refused string     `json:"refused"`
			Limit   int        `json:"limit"`
			Until   *time.Time `json:"until"`
		}
		_ = json.Unmarshal(answer, &no)
		if m.Kind == store.PeerMail && no.Refused == store.RefusedRate && no.Limit > 0 && no.Until != nil {
			return &Refused{Peer: p.Name, Limit: no.Limit, Until: *no.Until}
		}
		return fmt.Errorf("peer %s: %w: %s", p.Name, ErrRefused, store.PeerRefusal(p.Name, m.Kind, no.Refused))
	case status == http.StatusBadRequest:
		return fmt.Errorf("peer %s: %w: it does not read the message", p.Name, ErrRefused)
	}
	return fmt.Errorf("peer %s answered %d", p.Name, status)
}

// Pull asks p for what waits there for this yard after sequence number
// after, which also tells p that everything up to it has arrived, and
// which of it this yard did not take (p.Refused). The answer is returned
// as p signed it, for the store to check and take (store.PeerPulled). The
// question says what this build understands and whom of this yard p may
// write to, and the answer what p's does.
//
// With a wait, p is asked to hold its answer for up to that long while it
// has nothing to hand over (whole seconds: less than one asks for no
// hold). A yardr from before pulls were held passes that by and answers at
// once, as p does with too many held already; ctx has to outlast the wait.
func (c *Caller) Pull(ctx context.Context, p store.Peer, after uint64, wait time.Duration) (store.PeerBatch, error) {
	says := SaysTo(p)
	path := Path + "?after=" + strconv.FormatUint(after, 10) + "&understands=" + strings.Join(says.Understands, ",")
	if secs := int(wait / time.Second); secs > 0 {
		path += "&wait=" + strconv.Itoa(secs)
	}
	if says.Yardr != "" {
		path += "&yardr=" + url.QueryEscape(says.Yardr)
	}
	if len(says.Roster) > 0 {
		roster, err := json.Marshal(says.Roster)
		if err != nil {
			return store.PeerBatch{}, err
		}
		path += "&roster=" + url.QueryEscape(string(roster))
	}
	told := slices.Sorted(maps.Keys(p.Refused))
	told = told[:min(len(told), maxRefusals)]
	for i, seq := range told {
		no := p.Refused[seq]
		refused := url.QueryEscape(no.Reason)
		if no.Limit > 0 && no.Until != nil {
			refused += fmt.Sprintf(":%d:%d", no.Limit, no.Until.UnixNano())
		}
		path += map[bool]string{true: "&refused=", false: ","}[i == 0] + strconv.FormatUint(seq, 10) + ":" + refused
	}
	status, answer, reqSig, sig, err := c.do(ctx, p, http.MethodGet, path, nil)
	if err != nil {
		return store.PeerBatch{}, err
	}
	if status != http.StatusOK {
		return store.PeerBatch{}, fmt.Errorf("peer %s answered %d", p.Name, status)
	}
	return store.PeerBatch{RequestSig: reqSig, Signature: sig, Body: answer, Told: told}, nil
}
