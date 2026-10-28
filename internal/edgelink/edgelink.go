// Package edgelink is the yard's side of the edge (design
// yardr-hosted-050n): the service at https://my.yardr.dev that stands in
// front of yards. It holds what yardr edge connect does: fetch the edge's
// key, enrol this yard by a code a person approves in a browser, and ask
// the edge to forget the yard again; and the link the server keeps open to
// the edge once the yard is connected (Link, link.go). The edge's half is the router of the
// yardr-hosted repository; this package is its client and knows no more of
// it than the calls below.
//
// The yard has no key of the edge's until it has fetched one, and a request
// is signed to a named receiver key (yardkey.SignRequest), so the first
// step is the one that is not signed: GET /edge/key over HTTPS. That is
// trust on first use, as ssh has for a host key, with the web's
// certificates making the first use hard to intercept. Everything after it
// is signed with the yard's key and answered signed with the key that was
// fetched, so a different key at the same address, later, moves nothing.
//
// The calls, all under the edge's origin:
//
//	GET  /edge/key        the edge's public key as yardkey.Public writes it,
//	                      as plain text. Not signed.
//	POST /edge/enrol      {"key": <the yard's key>, "name": <its name>}.
//	                      Answered {"code", "expires"} (RFC 3339, shown to
//	                      the person and read as unknown if it is anything
//	                      else) while no one has
//	                      approved the yard at the edge's /connect page, and
//	                      {"handle", "name", "email"} once someone has: the
//	                      account's handle, this yard's name among its yards
//	                      (the person may have changed the proposal at the
//	                      page), and who approved. The yard
//	                      calls again, signed anew, until the second, and is
//	                      answered the same code while its code is open.
//	POST /edge/disconnect {}: the edge forgets the row that names the yard's
//	                      key, and answers 200, or 404 if it had none.
//	POST /mint/google {"account": <the Google account's address>}:
//	                      Answered {"token", "expires_at",
//	                      "granted_scopes"}: the access token, when it
//	                      expires, and the scopes the connection was
//	                      granted, which the yard checks its source
//	                      against before it prints the token.
//	POST /member/add    {"yard_handle", "yard_name", "email",
//	                      "yard_user", "invited_by", "yard_key"}: the
//	                      yard reports an approved invite; 200 lands it,
//	                      a signed 403/409/422 refuses it for good, and
//	                      a signed 503 (or no answer) leaves it owed.
//	POST /member/remove {"yard_handle", "yard_name", "email",
//	                      "yard_key"}: the yard reports an approved
//	                      removal; 200 lands it, 404 lands it too (the
//	                      edge holds no such membership), a signed
//	                      403/409/422 refuses it for good, and a signed
//	                      503 (or no answer) leaves it owed. The router
//	                      sets who reports from the signing key, so no
//	                      body field names the reporter.
//
// A request carries the yard's key, the time and the signature in the
// Yardr-Peer-* headers, as between peers. An answer carries its signature
// in Yardr-Peer-Signature, made with the edge's key over its status, the
// request's signature and its body (yardkey.SignResponse). An answer that
// is not so signed is anyone's, whatever it says. A refusal is a 4xx with
// {"refused": <reason>}, signed too.
//
// The edge answers each signature once. A request is stamped in whole
// seconds, so two identical calls made in the same second are one signature,
// and the second is refused before the edge acts on it: 401 with
// X-Edge-Refusal: replay and no signature, since whoever sends a signature
// twice is not proved to be the yard. The yard signs the call again a second
// on and makes it once more (Client.call).
package edgelink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// The edge's paths. They are fixed, which is why the edge's address is its
// origin alone (store.CheckEdgeURL): a signature covers the path.
const (
	KeyPath          = "/edge/key"
	EnrolPath        = "/edge/enrol"
	DisconnectPath   = "/edge/disconnect"
	MintGooglePath   = "/mint/google"
	MemberAddPath    = "/member/add"
	MemberRemovePath = "/member/remove"
)

const (
	// callTimeout bounds one call: the edge answers each at once, since the
	// wait for a person is the yard's calling again and not the edge's
	// holding a call.
	callTimeout = 15 * time.Second
	// DefaultPoll is how often a yard asks again while its code waits for
	// the person to approve it.
	DefaultPoll = 2 * time.Second
	// DefaultWait is how long a yard waits for the person, whatever the edge
	// says its code is good for: the edge's own refusal ends it sooner, and
	// the yard's clock is not trusted to read the edge's expiry.
	DefaultWait = 15 * time.Minute
	// maxAnswer bounds what is read of an answer: a key, a code or a handle
	// is a few dozen bytes.
	maxAnswer = 4 << 10
	// maxText bounds each word of the edge's that is shown to a person.
	maxText = 64
)

// ErrNotApproved ends a wait that ran out, with the person not having
// approved the yard.
var ErrNotApproved = errors.New("nobody approved the yard in time")

// ErrUnsigned is an answer that is not signed with the key this yard
// pinned: what it says is anyone's.
var ErrUnsigned = errors.New("not signed with the key this yard pinned")

// Refused is the edge's signed word that it does not take the yard. The
// reason is the edge's own text, signed, and cut to what a terminal shows.
type Refused struct {
	Status int
	Reason string
}

func (e *Refused) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("the edge refused this yard (%d)", e.Status)
	}
	return fmt.Sprintf("the edge refused this yard: %s (%d)", e.Reason, e.Status)
}

// Client calls the edge at URL.
type Client struct {
	// URL is the edge's origin (store.CheckEdgeURL).
	URL string
	// HTTP makes the calls; nil is a client with the system's roots. It is
	// copied to refuse a redirect: the address is the edge's own, and a
	// call that followed one would carry the yard's signature, or its trust,
	// somewhere else.
	HTTP *http.Client
	// Poll is how often Enrolment.Wait asks again, and WaitFor how long it
	// waits for the person; zero is DefaultPoll and DefaultWait.
	Poll, WaitFor time.Duration
	// Now is the clock a request is signed by; nil is time.Now. Each call is
	// signed at the moment it is made, since the edge refuses a signature
	// more than yardkey.MaxSkew old, and a wait is longer than that. A call
	// reads it once. A call the edge refuses as a replay is signed again a
	// second past that reading (see call).
	Now func() time.Time
}

func (c *Client) client() *http.Client {
	base := c.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	own := *base
	own.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &own
}

// transport says why a call did not get an answer. A certificate this
// machine does not trust is told apart: it is the one failure of the first
// step that means the address may not be the edge's.
func (c *Client) transport(err error) error {
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		return fmt.Errorf("the certificate of %s is not trusted by this machine: the edge's key is fetched over HTTPS and nothing was signed: %w", c.URL, err)
	}
	return err
}

// Key is the edge's public key, fetched over HTTPS, or over plain http from
// this machine alone, for a stand-in (store.CheckEdgeURL). It is the first
// trust step: nothing is signed before the key is in the yard's hands, and
// the caller shows it to the person and pins it (store.Edge).
func (c *Client) Key(ctx context.Context) (string, error) {
	if err := store.CheckEdgeURL(c.URL); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+KeyPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return "", c.transport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s%s answered %d, and not with a key: is it an edge, at the address its operator gave?", c.URL, KeyPath, resp.StatusCode)
	}
	text, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(text))
	if len(text) > maxAnswer {
		key = ""
	}
	if _, err := yardkey.ParsePublic(key); err != nil {
		return "", fmt.Errorf("what answers at %s%s is no edge's key: %w", c.URL, KeyPath, err)
	}
	return key, nil
}

// replayRefusal is what the edge's X-Edge-Refusal says of a signature it has
// answered already. It comes with no signature of its own, since whoever
// sends a signature twice is not proved to be the yard.
const replayRefusal = "replay"

// call makes one signed POST to path and returns the status and body of an
// answer the edge signed, with the key the yard pinned. Any other is an
// error whatever it says: anyone on the way can write a 200.
//
// The edge answers each signature once, and a stamp is whole seconds, so two
// identical calls made in the same second are one signature and the second
// is a replay to it. The edge refuses a replay before it acts on the call, so
// the call is signed again a second past the stamp it was refused at and made
// once more. That stamp is worked from the first, not read from Now again,
// because a new reading would usually fall in the same second and make the
// same bytes. Nothing waits for it: the edge takes a stamp that far ahead of
// its own clock (yardkey.MaxSkew). A replay again is the answer, since one
// more try is all the same-second case needs.
func (c *Client) call(ctx context.Context, edgeKey string, yard ed25519.PrivateKey, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	at, signedAgain := now(), false
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		ts, reqSig := yardkey.SignRequest(yard, edgeKey, http.MethodPost, path, at, body)
		req.Header.Set(yardkey.HeaderKey, yardkey.Public(yard))
		req.Header.Set(yardkey.HeaderTime, ts)
		req.Header.Set(yardkey.HeaderSig, reqSig)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client().Do(req)
		if err != nil {
			return 0, nil, c.transport(err)
		}
		answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
		// Closed here and not deferred, since the loop may make a second call.
		resp.Body.Close()
		if err != nil {
			return 0, nil, err
		}
		if len(answer) > maxAnswer {
			return 0, nil, fmt.Errorf("the answer of %s is larger than %d bytes", c.URL, maxAnswer)
		}
		if yardkey.VerifyResponse(edgeKey, resp.StatusCode, reqSig, resp.Header.Get(yardkey.HeaderSig), answer) == nil {
			return resp.StatusCode, answer, nil
		}
		// A signed answer went out above, whatever it names: replay-full is the
		// edge asking the yard to wait, not a spent signature. Only an unsigned
		// one is asked for its refusal.
		refusal := resp.Header.Get(RefusalHeader)
		if refusal == replayRefusal && !signedAgain {
			at, signedAgain = at.Add(time.Second), true
			continue
		}
		return 0, nil, c.unsigned(resp.StatusCode, refusal)
	}
}

// unsigned is the error for an answer the pinned key did not sign. What it
// says is anyone's text, so only its status is passed on, and the word it
// gives in X-Edge-Refusal, which tells a person more than the guess this falls
// back to. The word is quoted as the header's and not as the edge's, and cut
// to a line (text) because it reaches a terminal. A replay gets here only as
// the answer to the call's second try, since the first is signed again.
func (c *Client) unsigned(status int, refusal string) error {
	said := ""
	if word := text(refusal); word != "" {
		said = fmt.Sprintf(" (%s: %s)", RefusalHeader, word)
	}
	why := fmt.Sprintf("the clocks are more than %s apart, the edge's key is not the one this yard pinned, or something between the two answered", yardkey.MaxSkew)
	if refusal == replayRefusal {
		why = "the call was refused as a replay, and again when signed afresh a second later"
	}
	return fmt.Errorf("%s answered %d%s, %w: %s", c.URL, status, said, ErrUnsigned, why)
}

// answer is what the edge says to an enrolling call, and its refusals.
type answer struct {
	Code    string          `json:"code"`
	Expires json.RawMessage `json:"expires"`
	Handle  string          `json:"handle"`
	Name    string          `json:"name"`
	Email   string          `json:"email"`
	Refused string          `json:"refused"`
}

// expiry reads when a code stops being good, as RFC 3339. It is shown to the
// person and decides nothing, so a value that is not a time counts as
// unknown, and is no reason to refuse the enrolment.
func expiry(raw json.RawMessage) time.Time {
	var t time.Time
	if json.Unmarshal(raw, &t) != nil {
		return time.Time{}
	}
	return t
}

// Enrolment is a yard's enrolment with the edge, as far as the edge has
// taken it: Code is what the person types at the edge's /connect page,
// until Expires (the zero time when the edge did not say); Handle, Name and
// Email of whoever approved, once someone has. A yard the edge knows already
// is answered with its handle and name at once.
type Enrolment struct {
	Code    string
	Expires time.Time
	Handle  string
	Name    string
	Email   string

	c       *Client
	edgeKey string
	yard    ed25519.PrivateKey
	body    []byte
}

// Enrol begins the enrolment of the yard whose key is yard, which calls
// itself name, with the edge whose key is edgeKey: the first call, which the
// edge answers with a code. The caller shows the code and Waits.
func (c *Client) Enrol(ctx context.Context, edgeKey string, yard ed25519.PrivateKey, name string) (*Enrolment, error) {
	body, err := json.Marshal(map[string]string{"key": yardkey.Public(yard), "name": name})
	if err != nil {
		return nil, err
	}
	e := &Enrolment{c: c, edgeKey: edgeKey, yard: yard, body: body}
	if err := e.ask(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// ask makes the enrolling call once and takes what it is answered. The
// edge answers the same code to a yard that asks again while the code is
// open, so asking is the whole of waiting.
func (e *Enrolment) ask(ctx context.Context) error {
	status, body, err := e.c.call(ctx, e.edgeKey, e.yard, EnrolPath, e.body)
	if err != nil {
		return err
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return fmt.Errorf("the edge's answer to the enrolment is no JSON object (%d)", status)
	}
	if status != http.StatusOK {
		return &Refused{Status: status, Reason: text(a.Refused)}
	}
	switch {
	case a.Handle != "":
		if a.Name == "" {
			return errors.New("the edge's answer to the enrolment names the account but no yard")
		}
		e.Handle, e.Name, e.Email = a.Handle, a.Name, text(a.Email)
	case a.Code != "":
		// The person types the code as it is shown: one that a terminal
		// would not show whole is no code.
		if text(a.Code) != a.Code {
			return errors.New("the edge's code is not text a person can type")
		}
		e.Code, e.Expires = a.Code, expiry(a.Expires)
	default:
		return errors.New("the edge's answer to the enrolment holds neither a code nor a handle")
	}
	return nil
}

// Wait asks the edge, every Poll, until the person has approved the yard,
// which sets Handle; it ends with the edge's refusal (an expired code, a
// person who said no), with ctx, or with ErrNotApproved when WaitFor is up.
func (e *Enrolment) Wait(ctx context.Context) error {
	wait, poll := e.c.WaitFor, e.c.Poll
	if wait <= 0 {
		wait = DefaultWait
	}
	if poll <= 0 {
		poll = DefaultPoll
	}
	waiting, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for e.Handle == "" {
		select {
		case <-waiting.Done():
			// The caller's own end is the caller's to say; the wait's is
			// ours.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrNotApproved
		case <-time.After(poll):
		}
		if err := e.ask(waiting); err != nil {
			// The end of the wait, or the caller's, can land inside a call:
			// it is then the end, and not a failure of the call.
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case waiting.Err() != nil:
				return ErrNotApproved
			}
			return err
		}
	}
	return nil
}

// GoogleMint is the edge's answer to a mint call: the access token and
// when it expires, with the scopes the connection was granted, which the
// caller checks its source against before the token is printed.
type GoogleMint struct {
	Token         string
	ExpiresAt     string
	GrantedScopes []string
}

// mintAnswer is what the edge says to a mint call, and its refusals.
type mintAnswer struct {
	Token         string   `json:"token"`
	ExpiresAt     string   `json:"expires_at"`
	GrantedScopes []string `json:"granted_scopes"`
	Refused       string   `json:"refused"`
}

// MintGoogle asks the edge for a Google access token for account (design
// yardr-hosted-4dx0 3.4): POST /mint/google, signed with the yard's key to
// the edge's. A refusal is a *Refused in the edge's words, whole; an answer
// no signature of the edge's covers is ErrUnsigned, whatever it says.
func (c *Client) MintGoogle(ctx context.Context, edgeKey string, yard ed25519.PrivateKey, account string) (*GoogleMint, error) {
	body, err := json.Marshal(map[string]string{"account": account})
	if err != nil {
		return nil, err
	}
	status, answer, err := c.call(ctx, edgeKey, yard, MintGooglePath, body)
	if err != nil {
		return nil, err
	}
	var a mintAnswer
	if err := json.Unmarshal(answer, &a); err != nil {
		return nil, fmt.Errorf("the edge's answer to the mint is no JSON object (%d)", status)
	}
	if status != http.StatusOK {
		return nil, &Refused{Status: status, Reason: words(a.Refused)}
	}
	if a.Token == "" {
		return nil, errors.New("the edge's answer to the mint holds no token")
	}
	return &GoogleMint{Token: a.Token, ExpiresAt: a.ExpiresAt, GrantedScopes: a.GrantedScopes}, nil
}

// MemberAdd is one yard's invite report to the edge: the yard it names,
// the invited address, the yard user the membership acts as and the
// approving operator who invited. YardKey is the reporting yard's key as
// yardkey.Public writes it, a second wall behind the signature.
type MemberAdd struct {
	YardHandle string `json:"yard_handle"`
	YardName   string `json:"yard_name"`
	Email      string `json:"email"`
	YardUser   string `json:"yard_user"`
	InvitedBy  string `json:"invited_by"`
	YardKey    string `json:"yard_key"`
}

// MemberRemove is one yard's removal report to the edge: the yard it
// names and the address whose membership goes.
type MemberRemove struct {
	YardHandle string `json:"yard_handle"`
	YardName   string `json:"yard_name"`
	Email      string `json:"email"`
	YardKey    string `json:"yard_key"`
}

// memberAnswer is what the edge says to a member report, and its
// refusals: the edge's own door writes "refused", the provisioner's
// socket writes "error"; both are signed, so both are read.
type memberAnswer struct {
	Refused string `json:"refused"`
	Error   string `json:"error"`
}

func (a memberAnswer) reason() string {
	if a.Refused != "" {
		return a.Refused
	}
	return a.Error
}

// MemberAdd reports an approved invite to the edge, signed with the
// yard's key to the edge's, as MintGoogle is. A 200 says it landed; any
// other signed answer is a *Refused the caller reads by status (503
// leaves the report owed; 403, 409 and 422 refuse it for good), and an
// answer no signature covers is ErrUnsigned.
func (c *Client) MemberAdd(ctx context.Context, edgeKey string, yard ed25519.PrivateKey, m MemberAdd) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	status, answer, err := c.call(ctx, edgeKey, yard, MemberAddPath, body)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	var a memberAnswer
	_ = json.Unmarshal(answer, &a)
	return &Refused{Status: status, Reason: words(a.reason())}
}

// MemberRemove reports an approved removal to the edge, signed as
// MemberAdd is. A 200 and a 404 both say it landed: 404 is the edge
// holding no such membership, which is what a removal that was already
// reported (and its answer lost) hears again. Any other signed answer
// is a *Refused the caller reads by status, as MemberAdd's is.
func (c *Client) MemberRemove(ctx context.Context, edgeKey string, yard ed25519.PrivateKey, m MemberRemove) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	status, answer, err := c.call(ctx, edgeKey, yard, MemberRemovePath, body)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNotFound {
		return nil
	}
	var a memberAnswer
	_ = json.Unmarshal(answer, &a)
	return &Refused{Status: status, Reason: words(a.reason())}
}

// Drop asks the edge to forget the yard whose key is yard: the row that
// names that key. The edge answering that it has none is the same to a yard
// that wants to be gone.
func (c *Client) Drop(ctx context.Context, edgeKey string, yard ed25519.PrivateKey) error {
	status, body, err := c.call(ctx, edgeKey, yard, DisconnectPath, []byte("{}"))
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNotFound {
		return nil
	}
	var a answer
	_ = json.Unmarshal(body, &a)
	return &Refused{Status: status, Reason: text(a.Refused)}
}

// text is s as one line a terminal shows as it is, cut short: the edge's
// words reach a person's screen, and a control character in them could
// rewrite it.
func text(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		if b.Len() >= maxText {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// words is s as a terminal shows it, whole: a mint's refusal names the page
// to open, past the line text is cut to, so it is not cut. Control
// characters still go, since they could rewrite the screen.
func words(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
