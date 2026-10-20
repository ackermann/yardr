package edgelink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// The link (design yardr-hosted-050n, 3.2) is the one WebSocket a yard keeps
// open to its edge, so that what reaches the edge for the yard comes down it
// and needs no port of the yard's own. Its wire, as the edge defines it
// (yardr-hosted, router/README.md, "The link"):
//
//	GET /edge/link        signed as an enrolling call is, with an empty body,
//	                      and Yardr-Link-Nonce: 32 random bytes of the
//	                      yard's, unpadded base64url. Refused 401, 400 or 403
//	                      with X-Edge-Refusal before anything is upgraded.
//	edge → yard           {"type":"challenge","nonce":<the edge's 32 bytes>,
//	                      "sig":<SignLink by the edge over both nonces>}
//	yard → edge           {"type":"hello","sig":<SignLink by the yard>}
//	edge → yard           {"type":"welcome"}
//	edge → yard           {"type":"request","id","seq","method","path",
//	                      "header","body","received","queued","sig"}
//	yard → edge           {"type":"response","id","status","header","body"}
//	either way            {"type":"ping"} and {"type":"pong"}
//
// The yard checks the challenge against the key it pinned when it
// connected, so a socket that is not the edge's is never answered, and a
// request frame against the same key over the link's session, its seq and
// the request as the sender made it (yardkey.VerifyFrame).
const (
	LinkPath = "/edge/link"
	// NonceHeader carries the yard's nonce on the upgrade.
	NonceHeader = "Yardr-Link-Nonce"
	// RefusalHeader is where the edge names what it refused.
	RefusalHeader = "X-Edge-Refusal"
	// RemoteAddr is the address a request that came down the link is served
	// from. It is never loopback and never router_addr, so whatever the
	// listener's handlers let in only from the yard's own machine or from
	// the hosted router (a secret, an approval) is refused over the link.
	RemoteAddr = "edge:0"
)

// The link's timing, as 3.2 has it. Each side pings every linkPing and
// counts the socket dead after linkDead with nothing heard: under the 125
// seconds Cloudflare waits on a silent origin. A dial that fails waits
// linkBackoff, doubling to linkBackoffMax, with jitter, and the wait starts
// again from linkBackoff after a link that stayed up for linkSteady.
const (
	linkPing       = 30 * time.Second
	linkDead       = 75 * time.Second
	linkHello      = 10 * time.Second
	linkBackoff    = time.Second
	linkBackoffMax = 60 * time.Second
	linkSteady     = time.Minute
)

const (
	// maxLinkFrame bounds one frame from the edge: a MaxHookPayload body in
	// base64 and headers that fit the router's 1 MiB HTTP header budget.
	maxLinkFrame = 3 << 20
	// MaxAnswer is the largest body of an answer the edge passes on. A larger
	// one is no answer at the edge, so it is sent cut one byte past this:
	// the edge refuses it all the same, and the frame stays under what the
	// edge reads of one.
	MaxAnswer = 64 << 10
)

// The reasons a link is down, as the edge_link_down event and the link's
// state name them: words a crew member can be told and act on.
const (
	DownClosed     = "closed"
	DownPings      = "unanswered pings"
	DownRefusedKey = "refused key"
	DownRefused    = "refused"
	DownUnreached  = "unreachable"
	DownStopped    = "stopped"
)

// Link keeps one yard's link to its edge: it dials, answers the challenge,
// serves what comes down, and dials again when the socket dies.
type Link struct {
	// Edge is the edge the yard is connected to, its key the one pinned.
	Edge store.Edge
	// Key is the yard's own key.
	Key ed25519.PrivateKey
	// Handler serves each request that comes down the link: the listener's
	// own mux, in process, with RemoteAddr set to RemoteAddr.
	Handler http.Handler
	// HTTP dials; nil is a client with the system's roots. Redirects are
	// refused, as for every call to the edge.
	HTTP *http.Client
	Log  *slog.Logger
	// Told is given the link's state each time it changes; it is called from
	// the link's own goroutine, one change at a time.
	Told func(State)

	// The timing of 3.2; tests shorten it. Zero is the design's figure.
	PingEvery, DeadAfter, HelloWithin time.Duration
	Backoff, BackoffMax, Steady       time.Duration

	mu    sync.Mutex
	state State
}

// State is what a link is now, which yardr status and yardr doctor show.
type State struct {
	URL    string `json:"url"`
	Handle string `json:"handle"`
	// Up is whether the link is the yard's: its challenge answered, its
	// socket alive. Since is when it came up, or went down.
	Up    bool      `json:"up"`
	Since time.Time `json:"since"`
	// Reason is why it is down, one of the Down words, and Detail what was
	// seen, for a person; both empty while it is up.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Ups is how many times the link has come up since the server started.
	Ups int `json:"ups"`
	// PID is the server that keeps the link, as the state's file says it.
	PID int `json:"pid,omitempty"`
}

// Reconnects is how many times the link came up again after its first.
func (s State) Reconnects() int { return max(s.Ups-1, 0) }

// State is the link as it is now.
func (l *Link) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

func (l *Link) set(s State) {
	s.URL, s.Handle = l.Edge.URL, l.Edge.Handle
	l.mu.Lock()
	l.state = s
	l.mu.Unlock()
	if l.Told != nil {
		l.Told(s)
	}
}

func or(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run keeps the link until ctx ends: dial, serve, and dial again after the
// backoff when it ends. Each change of the link is told, a link that came
// down always and a dial that failed when its reason is not the one last
// told, so an edge that refuses the yard every minute is told once.
func (l *Link) Run(ctx context.Context) {
	if l.Log == nil {
		l.Log = slog.New(slog.DiscardHandler)
	}
	base, top, steady := or(l.Backoff, linkBackoff), or(l.BackoffMax, linkBackoffMax), or(l.Steady, linkSteady)
	backoff := base
	told := ""
	for {
		upFor, reason, detail := l.once(ctx)
		if ctx.Err() != nil {
			if l.State().Up {
				l.set(State{Since: time.Now(), Reason: DownStopped, Ups: l.State().Ups})
			}
			return
		}
		if upFor > 0 || reason != told {
			l.set(State{Since: time.Now(), Reason: reason, Detail: detail, Ups: l.State().Ups})
			told = reason
		}
		if upFor >= steady {
			backoff = base
		}
		l.Log.Info("edge link down", "edge", l.Edge.URL, "reason", reason, "detail", detail, "again_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
		backoff = min(backoff*2, top)
	}
}

// jitter is d less up to half of it, at random: yards that lost the edge
// together do not all dial it again in the same second.
func jitter(d time.Duration) time.Duration {
	half := int64(d / 2)
	if half <= 0 {
		return d
	}
	n, err := rand.Int(rand.Reader, big.NewInt(half))
	if err != nil {
		return d
	}
	return d - time.Duration(n.Int64())
}

func (l *Link) client() *http.Client {
	base := l.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	own := *base
	own.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &own
}

// The frames of the link. Body is base64, as encoding/json writes bytes.
type frameIn struct {
	Type     string      `json:"type"`
	Nonce    string      `json:"nonce"`
	Sig      string      `json:"sig"`
	ID       string      `json:"id"`
	Seq      uint64      `json:"seq"`
	Method   string      `json:"method"`
	Path     string      `json:"path"`
	Header   http.Header `json:"header"`
	Body     []byte      `json:"body"`
	Received string      `json:"received"`
	Queued   bool        `json:"queued"`
}

type responseFrame struct {
	Type   string      `json:"type"`
	ID     string      `json:"id"`
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}

// once dials the link and serves it until it ends. It returns how long the
// link was up (zero when it never came up) and why it ended.
func (l *Link) once(ctx context.Context) (upFor time.Duration, reason, detail string) {
	yardNonce := make([]byte, yardkey.NonceSize)
	if _, err := rand.Read(yardNonce); err != nil {
		return 0, DownUnreached, "no nonce: " + err.Error()
	}
	ts, sig := yardkey.SignRequest(l.Key, l.Edge.Key, http.MethodGet, LinkPath, time.Now(), nil)
	h := http.Header{}
	h.Set(yardkey.HeaderKey, yardkey.Public(l.Key))
	h.Set(yardkey.HeaderTime, ts)
	h.Set(yardkey.HeaderSig, sig)
	h.Set(NonceHeader, yardkey.EncodeNonce(yardNonce))
	dctx, cancel := context.WithTimeout(ctx, callTimeout)
	c, resp, err := websocket.Dial(dctx, l.Edge.URL+LinkPath, &websocket.DialOptions{
		HTTPClient: l.client(), HTTPHeader: h, CompressionMode: websocket.CompressionDisabled})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			// The edge's refusal names what it refused; its text is not
			// signed, so only the word is kept, cut to what a terminal shows.
			return 0, DownRefused, fmt.Sprintf("the edge answered %d (%s)", resp.StatusCode, orText(text(resp.Header.Get(RefusalHeader)), "no reason"))
		}
		return 0, DownUnreached, err.Error()
	}
	defer c.CloseNow()
	c.SetReadLimit(maxLinkFrame)

	// The challenge, checked against the pinned key before anything is
	// signed in answer: a socket that is not the edge's gets nothing.
	edgeNonce, reason, why := l.challenge(ctx, c, yardNonce)
	if why != "" {
		c.Close(websocket.StatusPolicyViolation, "the challenge is not the edge's")
		return 0, reason, why
	}
	if err := l.write(ctx, c, map[string]string{"type": "hello", "sig": yardkey.SignLink(l.Key, yardNonce, edgeNonce)}); err != nil {
		return 0, DownClosed, "the hello was not sent: " + err.Error()
	}
	wctx, cancel := context.WithTimeout(ctx, or(l.HelloWithin, linkHello))
	var welcome frameIn
	err = l.read(wctx, c, &welcome)
	cancel()
	if err != nil || welcome.Type != "welcome" {
		// The edge checks the hello against the key it holds for this
		// yard, and closes a socket whose hello it does not take.
		return 0, DownRefused, "the edge did not welcome the hello: " + closeText(err, welcome.Type)
	}
	up := time.Now()
	l.set(State{Up: true, Since: up, Ups: l.State().Ups + 1})
	l.Log.Info("edge link up", "edge", l.Edge.URL, "handle", l.Edge.Handle)
	reason, detail = l.serve(ctx, c, yardkey.Session(yardNonce, edgeNonce))
	return max(time.Since(up), time.Nanosecond), reason, detail
}

// challenge reads the edge's challenge and checks it: the edge's key, over
// the yard's own fresh nonce and the edge's. It returns the edge's nonce,
// or the reason the link is down and why: a socket that ended first is
// closed, and a challenge that is no edge's a refused key.
func (l *Link) challenge(ctx context.Context, c *websocket.Conn, yardNonce []byte) (edgeNonce []byte, reason, why string) {
	cctx, cancel := context.WithTimeout(ctx, or(l.HelloWithin, linkHello))
	defer cancel()
	var f frameIn
	if err := l.read(cctx, c, &f); err != nil {
		return nil, DownClosed, "no challenge: " + closeText(err, "")
	}
	if f.Type != "challenge" {
		return nil, DownRefusedKey, fmt.Sprintf("a %q frame where the challenge was due", text(f.Type))
	}
	edgeNonce, err := yardkey.DecodeNonce(f.Nonce)
	if err != nil {
		return nil, DownRefusedKey, "the challenge names no nonce of 32 bytes"
	}
	if err := yardkey.VerifyLink(l.Edge.Key, yardNonce, edgeNonce, f.Sig); err != nil {
		return nil, DownRefusedKey, "the challenge is not signed by the edge's key this yard pinned, over this link's nonces"
	}
	return edgeNonce, "", ""
}

// serve reads the frames of a live link until it ends, and says why. A
// request is checked here, in the order the frames came, and served in a
// goroutine of its own, so a slow one holds up no other.
func (l *Link) serve(ctx context.Context, c *websocket.Conn, session string) (reason, detail string) {
	lctx, cancel := context.WithCancel(ctx)
	dead := or(l.DeadAfter, linkDead)
	var silent atomic.Bool
	heard := time.AfterFunc(dead, func() {
		silent.Store(true)
		c.CloseNow()
	})
	go func() {
		t := time.NewTicker(or(l.PingEvery, linkPing))
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				if l.write(lctx, c, map[string]string{"type": "ping"}) != nil {
					return
				}
			}
		}
	}()
	var served sync.WaitGroup
	defer func() {
		heard.Stop()
		cancel()
		served.Wait()
	}()
	var last uint64 // the seq of the last request accepted on this socket
	for {
		var f frameIn
		err := l.read(lctx, c, &f)
		if err != nil {
			var syntax *json.SyntaxError
			var typ *json.UnmarshalTypeError
			if errors.As(err, &syntax) || errors.As(err, &typ) {
				heard.Reset(dead)
				l.Log.Warn("edge link: a frame that is not the link's, dropped", "err", err)
				continue
			}
			switch {
			case ctx.Err() != nil:
				return DownStopped, ""
			case silent.Load():
				return DownPings, fmt.Sprintf("nothing heard from the edge for %s", dead)
			}
			return DownClosed, closeText(err, "")
		}
		heard.Reset(dead)
		switch f.Type {
		case "request":
			if why := l.check(session, last, f); why != "" {
				l.Log.Warn("edge link: a request refused", "id", text(f.ID), "seq", f.Seq, "why", why)
				continue
			}
			last = f.Seq
			served.Go(func() { l.answer(lctx, c, f) })
		case "ping":
			_ = l.write(lctx, c, map[string]string{"type": "pong"})
		case "pong":
		default:
			l.Log.Warn("edge link: a frame of no type the yard takes, dropped", "type", text(f.Type))
		}
	}
}

// check says why a request frame is refused, "" when it is the edge's: signed
// with the pinned key over this socket's session and the request as it came,
// and numbered past the last one accepted on the socket, which the edge's
// own frames always are, since a socket delivers in order.
func (l *Link) check(session string, last uint64, f frameIn) string {
	err := yardkey.VerifyFrame(l.Edge.Key, yardkey.Frame{
		Session: session, Seq: f.Seq, ID: f.ID, Method: f.Method, Path: f.Path,
		Header: f.Header, Body: f.Body, Received: f.Received, Queued: f.Queued,
	}, f.Sig)
	switch {
	case err != nil:
		return "not signed by the edge's key over this link: " + err.Error()
	case f.Seq <= last:
		return fmt.Sprintf("seq %d is not past %d, the last taken on this link: a frame sent again", f.Seq, last)
	}
	return ""
}

// answer serves one request with the listener's handler and sends its
// answer back. The request is the sender's as the edge signed it, from
// RemoteAddr; its context ends with the link.
func (l *Link) answer(ctx context.Context, c *websocket.Conn, f frameIn) {
	w := &answerWriter{header: http.Header{}}
	func() {
		defer func() {
			if p := recover(); p != nil && p != http.ErrAbortHandler {
				l.Log.Error("edge link: a request's handler panicked", "path", f.Path, "panic", fmt.Sprint(p))
				w.status, w.body = http.StatusInternalServerError, nil
			}
		}()
		req, err := l.request(ctx, f)
		if err != nil {
			w.status = http.StatusBadRequest
			return
		}
		l.Handler.ServeHTTP(w, req)
	}()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if err := l.write(ctx, c, responseFrame{Type: "response", ID: f.ID, Status: w.status, Header: w.header, Body: w.body}); err != nil && ctx.Err() == nil {
		l.Log.Warn("edge link: an answer not sent", "id", text(f.ID), "err", err)
	}
}

// request is the frame as the request a handler serves. Its headers are
// put in Go's form, so a handler finds a name however the sender wrote it;
// what was signed was the frame's own.
func (l *Link) request(ctx context.Context, f frameIn) (*http.Request, error) {
	u, err := url.ParseRequestURI(f.Path)
	if err != nil || !strings.HasPrefix(f.Path, "/") {
		return nil, fmt.Errorf("path %q is no request's", text(f.Path))
	}
	req, err := http.NewRequestWithContext(ctx, f.Method, u.String(), bytes.NewReader(f.Body))
	if err != nil {
		return nil, err
	}
	req.URL, req.RequestURI = u, f.Path
	if host, err := url.Parse(l.Edge.URL); err == nil {
		req.Host = host.Host
	}
	req.RemoteAddr = RemoteAddr
	req.Header = http.Header{}
	for name, values := range f.Header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	return req, nil
}

// answerWriter keeps a handler's answer for the response frame: its status,
// headers and body, the body cut one byte past MaxAnswer.
type answerWriter struct {
	header http.Header
	status int
	body   []byte
}

func (w *answerWriter) Header() http.Header { return w.header }

func (w *answerWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *answerWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if room := MaxAnswer + 1 - len(w.body); room > 0 {
		w.body = append(w.body, b[:min(len(b), room)]...)
	}
	return len(b), nil
}

func (l *Link) read(ctx context.Context, c *websocket.Conn, v any) error {
	_, b, err := c.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// write sends one frame. A write that does not finish within the ping
// interval is a socket that takes nothing, and ends with it.
func (l *Link) write(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, or(l.PingEvery, linkPing))
	defer cancel()
	return c.Write(wctx, websocket.MessageText, b)
}

// closeText says how a socket ended: the edge's close and its reason, cut to
// what a terminal shows, or the error itself.
func closeText(err error, got string) string {
	if err == nil {
		return fmt.Sprintf("a %q frame", text(got))
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return fmt.Sprintf("the edge closed the link (%d %s)", ce.Code, orText(text(ce.Reason), "no reason"))
	}
	if errors.Is(err, io.EOF) {
		return "the socket ended"
	}
	return err.Error()
}

func orText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// WriteState records s as the link of this process's server, by renaming a
// finished file over the last one, as the receiver's state is written: the
// link lives in the server, and status and doctor are other processes.
func WriteState(c *config.Config, s State) error {
	s.PID = os.Getpid()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := c.EdgeState() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.EdgeState()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ReadState is the link as the running server last said it is; false when
// it has said nothing: no link was kept, or the state is that of a server
// that has gone (the pidfile names another).
func ReadState(c *config.Config) (State, bool) {
	var s State
	b, err := os.ReadFile(c.EdgeState())
	if err != nil || json.Unmarshal(b, &s) != nil {
		return State{}, false
	}
	pid, err := os.ReadFile(c.PIDFile())
	if err != nil || strings.TrimSpace(string(pid)) != strconv.Itoa(s.PID) {
		return State{}, false
	}
	return s, true
}
