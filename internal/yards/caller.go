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
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// maxAnswer bounds what is read of a peer's answer: a page of messages at
// its largest, with room for the rest.
const maxAnswer = 2 << 20

// ErrRefused is the peer's signed word that it does not take the message:
// its link does not carry the kind in, or it is mail the yard refuses
// (store.PeerRefusal). Trying again changes nothing.
var ErrRefused = errors.New("refused by the peer")

// ErrUnsigned is an answer that is not signed with the key of the yard that
// was asked: what it says is anyone's.
var ErrUnsigned = errors.New("not signed with its key")

// Caller makes this yard's requests to its peers, signed with its key.
type Caller struct {
	Key  ed25519.PrivateKey
	HTTP *http.Client     // nil: http.DefaultClient
	Now  func() time.Time // nil: time.Now
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
	req.Header.Set(yardkey.HeaderKey, yardkey.Public(c.Key))
	req.Header.Set(yardkey.HeaderTime, ts)
	req.Header.Set(yardkey.HeaderSig, reqSig)
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
	sig = resp.Header.Get(yardkey.HeaderSig)
	if err := yardkey.VerifyResponse(p.Key, resp.StatusCode, reqSig, sig, answer); err != nil {
		// What an unsigned answer says is anyone's text: only its status
		// is passed on.
		return 0, nil, "", "", fmt.Errorf("peer %s answered %d, %w: this yard is not a peer of it there, the clocks are more than %s apart, or something between the two answered",
			p.Name, resp.StatusCode, ErrUnsigned, yardkey.MaxSkew)
	}
	return resp.StatusCode, answer, reqSig, sig, nil
}

// Push posts m to p. nil means p has it (taken now, or before: the same id
// twice is taken once). ErrRefused means p will not take it; any other
// error, that it is worth another try.
func (c *Caller) Push(ctx context.Context, p store.Peer, m store.PeerMessage) error {
	m.Seq = 0
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	status, answer, _, _, err := c.do(ctx, p, http.MethodPost, Path, body)
	switch {
	case err != nil:
		return err
	case status == http.StatusOK:
		return nil
	case status == http.StatusForbidden:
		// The reason is the peer's text: only one this yard knows is said.
		var no struct {
			Refused string `json:"refused"`
		}
		_ = json.Unmarshal(answer, &no)
		return fmt.Errorf("peer %s: %w: %s", p.Name, ErrRefused, store.PeerRefusal(m.Kind, no.Refused))
	case status == http.StatusBadRequest:
		return fmt.Errorf("peer %s: %w: it does not read the message", p.Name, ErrRefused)
	}
	return fmt.Errorf("peer %s answered %d", p.Name, status)
}

// Pull asks p for what waits there for this yard after sequence number
// after, which also tells p that everything up to it has arrived, and
// which of it this yard did not take (p.Refused). The answer is returned
// as p signed it, for the store to check and take (store.PeerPulled).
func (c *Caller) Pull(ctx context.Context, p store.Peer, after uint64) (store.PeerBatch, error) {
	path := Path + "?after=" + strconv.FormatUint(after, 10)
	told := slices.Sorted(maps.Keys(p.Refused))
	told = told[:min(len(told), maxRefusals)]
	for i, seq := range told {
		path += map[bool]string{true: "&refused=", false: ","}[i == 0] + strconv.FormatUint(seq, 10) + ":" + url.QueryEscape(p.Refused[seq])
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
