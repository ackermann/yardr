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
	"yardr.dev/yardr/internal/hooks"
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
//	                      (a live request: the head, and the whole answer
//	                      when its handler never flushed)
//	yard → edge           {"type":"response","id","status","header",
//	                      "stream":true}   (a live answer that will not fit
//	                      one frame: the head with an empty body, the body
//	                      in data pieces, then end)
//	yard → edge           {"type":"data","id","body"}   (each piece of a
//	                      live answer its handler flushed, in order)
//	yard → edge           {"type":"end","id"}   (a live answer is over: its
//	                      handler returned)
//	edge → yard           {"type":"end","id"}   (a stream's caller went
//	                      away: the request with that id is cancelled)
//	yard → edge           {"type":"ack","id"}   (a queued request, once
//	                      served: the edge deletes it on the ack)
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
	// from. It is never loopback and never router_addr, so the listener can
	// distinguish edge traffic from a local client or the hosted router.
	// Calls restricted to those origins, such as secret entry, stay refused
	// over this link.
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
	// maxFrame bounds one frame to the edge: the edge's own read limit
	// (maxFrame in the router's link package). A frame that encodes larger
	// is never sent, since the edge closes the link on one. An answer that
	// will not fit one frame goes as a stream instead, the head marked with
	// stream and an empty body, then the body in data frames that fit; only
	// a head that alone will not fit is answered 413.
	maxFrame = 1 << 20
	// maxDataBody is the most bytes one data frame carries: base64 grows a
	// body by four thirds, with no character JSON escapes, so this with
	// its frame's own bytes stays under maxFrame.
	maxDataBody = (maxFrame - 1024) / 4 * 3
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
	// Role selects the edge's receiver. Empty keeps the default yard link
	// and sends no role header; "web" carries the web view.
	Role string
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
	// live is each request being served, by its id, with the cancel of its
	// context: the edge's end for an id cancels that request. An id is held
	// only between serve's reading of its frame and its handler's return.
	liveMu sync.Mutex
	live   map[string]context.CancelFunc
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
	// Stream marks the head of an answer that will not fit one frame: its
	// body is empty, and the body follows in data frames with the same id
	// until end.
	Stream bool `json:"stream,omitempty"`
}

type dataFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Body []byte `json:"body"`
}

type endFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type ackFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
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
	if l.Role != "" {
		h.Set("Yardr-Link-Role", l.Role)
	}
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
	// The hello keeps a budget: before serve starts no heard timer bounds a
	// socket that takes nothing, and here an expired budget fails the dial,
	// which the link dials again, instead of misjudging a live link.
	hctx, hcancel := context.WithTimeout(ctx, or(l.HelloWithin, linkHello))
	herr := l.write(hctx, c, map[string]string{"type": "hello", "sig": yardkey.SignLink(l.Key, yardNonce, edgeNonce)})
	hcancel()
	if herr != nil {
		return 0, DownClosed, "the hello was not sent: " + herr.Error()
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
			// The request's own context, held by its id before its handler
			// starts, so the edge's end for it cancels it however soon it
			// comes.
			reqCtx, cancel := context.WithCancel(lctx)
			l.track(f.ID, cancel)
			request := f
			served.Go(func() {
				defer cancel()
				l.answer(lctx, c, request, reqCtx)
			})
		case "end":
			l.cancel(f.ID)
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

// track holds cancel as the cancel of the request with id, until untrack.
func (l *Link) track(id string, cancel context.CancelFunc) {
	l.liveMu.Lock()
	defer l.liveMu.Unlock()
	if l.live == nil {
		l.live = map[string]context.CancelFunc{}
	}
	l.live[id] = cancel
}

// cancel ends the request with id, if it is still served: the edge's end
// for it, whose caller went away. An id nothing serves is the edge
// answering an end already sent, and is nothing.
func (l *Link) cancel(id string) {
	l.liveMu.Lock()
	cancel := l.live[id]
	l.liveMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *Link) untrack(id string) {
	l.liveMu.Lock()
	defer l.liveMu.Unlock()
	delete(l.live, id)
}

// answer serves one request with the listener's handler and sends its
// answer back. A queued request is acked once the handler is done, so it is
// acked after it is recorded or refused, and nothing else is sent for it. A
// live request is answered as a stream: the head goes as a response frame
// on the handler's first flush, or whole when it never flushed; each flushed
// piece goes as data, and end goes when the handler returns. The end goes
// after every live answer, flushed or whole: the edge drops one nothing
// waits on, and a stream's head without one would leave its device waiting.
// The request is the sender's as the edge signed it, from RemoteAddr; reqCtx
// ends it with the link, or when the edge ends the request.
func (l *Link) answer(lctx context.Context, c *websocket.Conn, f frameIn, reqCtx context.Context) {
	defer l.untrack(f.ID)
	if f.Queued {
		w := &answerWriter{header: http.Header{}}
		l.serveOne(w, f, reqCtx, func() { w.status = http.StatusInternalServerError })
		if err := l.write(lctx, c, ackFrame{Type: "ack", ID: f.ID}); err != nil && lctx.Err() == nil {
			l.Log.Warn("edge link: an ack not sent", "id", text(f.ID), "err", err)
		}
		return
	}
	w := &streamWriter{l: l, c: c, ctx: lctx, id: f.ID, header: http.Header{}}
	l.serveOne(w, f, reqCtx, w.fail)
	w.finish()
}

// serveOne builds f's request and serves it into w: a frame that is no
// request's is a 400, and a handler that panics is logged and failed.
func (l *Link) serveOne(w http.ResponseWriter, f frameIn, reqCtx context.Context, failed func()) {
	defer func() {
		if p := recover(); p != nil && p != http.ErrAbortHandler {
			l.Log.Error("edge link: a request's handler panicked", "path", f.Path, "panic", fmt.Sprint(p))
			failed()
		}
	}()
	req, err := l.request(reqCtx, f)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	l.Handler.ServeHTTP(w, req)
}

// request is the frame as the request a handler serves. Its headers are
// put in Go's form, so a handler finds a name however the sender wrote it;
// what was signed was the frame's own. A queued frame's request is marked
// with the edge-signed time the edge received it, which a declared
// source's window is checked against instead of the yard's clock; a queued
// frame whose received is no time is served unmarked, as a live one is.
func (l *Link) request(ctx context.Context, f frameIn) (*http.Request, error) {
	u, err := url.ParseRequestURI(f.Path)
	if err != nil || !strings.HasPrefix(f.Path, "/") {
		return nil, fmt.Errorf("path %q is no request's", text(f.Path))
	}
	if f.Queued {
		if received, err := time.Parse(time.RFC3339, f.Received); err == nil {
			ctx = hooks.WithEdgeReceived(ctx, received)
		} else {
			l.Log.Warn("edge link: a queued frame whose received is no time, served as live", "id", text(f.ID), "received", text(f.Received))
		}
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

// answerWriter keeps a queued request's status and headers while its
// handler runs; its body is discarded because the ack carries no answer and
// the edge answered its sender long ago.
type answerWriter struct {
	header http.Header
	status int
}

func (w *answerWriter) Header() http.Header { return w.header }

func (w *answerWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *answerWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return len(b), nil
}

// streamWriter is a live request's answer as the edge takes a stream: the
// head goes as a response, each flushed piece as data, and end when the
// handler returns (finish). A handler that never flushes is answered whole.
// Writes go under the link's context, so they outlive the request's own
// cancel.
type streamWriter struct {
	l   *Link
	c   *websocket.Conn
	ctx context.Context
	id  string

	header  http.Header
	status  int
	since   []byte // written since the last flush
	head    bool   // the response went
	quiet   bool   // a send failed: the link is going, and later sends are skipped
	refused bool   // the head alone was too large; its 413 carries no body
	failed  bool   // the handler panicked: the tail is dropped
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *streamWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if !w.quiet && !w.refused {
		w.since = append(w.since, b...)
	}
	return len(b), nil
}

// Flush sends the head on the first call, then what the handler wrote since
// the last flush as data. A flush with nothing new sends the head alone, so
// the edge has it while the stream is still quiet.
func (w *streamWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.quiet {
		w.since = nil
		return
	}
	if !w.head {
		w.head = true
		w.refused = !w.sendResponse(w.status, w.header, nil)
	}
	if len(w.since) > 0 && !w.refused {
		w.sendData(w.since)
	}
	w.since = nil
}

// fail drops a panicked handler's tail; when the head never went, finish
// answers 500 for it.
func (w *streamWriter) fail() {
	if !w.head {
		w.status = http.StatusInternalServerError
	}
	w.failed = true
}

// finish sends what the handler's return leaves: the whole answer when it
// never flushed, as a stream when it will not fit one frame, any piece it
// wrote since its last flush, and the end.
func (w *streamWriter) finish() {
	if w.quiet {
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if !w.head {
		body := w.since
		if w.failed {
			body = nil
		}
		w.sendResponse(w.status, w.header, body)
	} else if len(w.since) > 0 && !w.failed && !w.refused {
		w.sendData(w.since)
	}
	if w.quiet {
		return
	}
	if err := w.l.write(w.ctx, w.c, endFrame{Type: "end", ID: w.id}); err != nil && w.ctx.Err() == nil {
		w.l.Log.Warn("edge link: an end not sent", "id", text(w.id), "err", err)
	}
}

// sendResponse sends the head and says whether its frame is the whole
// answer. An answer that encodes past maxFrame is never sent in one frame:
// it goes as a stream instead, the head marked with stream and an empty
// body, then the body in data frames, so one large answer crosses the link
// instead of closing it. Only a head that alone is too large, its headers,
// is answered 413, with a short error. A stream and a 413 both report
// false, so the caller sends no more of the answer after either.
func (w *streamWriter) sendResponse(status int, header http.Header, body []byte) bool {
	b, err := json.Marshal(responseFrame{Type: "response", ID: w.id, Status: status, Header: header, Body: body})
	if err != nil {
		w.quiet = true
		w.l.Log.Warn("edge link: an answer that encodes to nothing, dropped", "id", text(w.id), "err", err)
		return false
	}
	if len(b) <= maxFrame {
		return w.writeResponse(b)
	}
	head, herr := json.Marshal(responseFrame{Type: "response", ID: w.id, Status: status, Header: header, Stream: true})
	if herr != nil || len(head) > maxFrame {
		w.l.Log.Warn("edge link: an answer whose head alone is larger than one frame carries, answered 413", "id", text(w.id), "bytes", len(b))
		b, err = json.Marshal(responseFrame{Type: "response", ID: w.id, Status: http.StatusRequestEntityTooLarge,
			Header: http.Header{}, Body: []byte("the answer's head is larger than one frame of the link carries")})
		if err != nil || len(b) > maxFrame {
			// The id alone fills a frame: nothing truthful fits in one.
			w.quiet = true
			return false
		}
		// A 413 carries no body: report it unwhole, as a stream, so the
		// caller sends no data after it.
		w.writeResponse(b)
		return false
	}
	if !w.writeResponse(head) {
		return false
	}
	w.sendData(body)
	return false
}

// writeResponse sends b as the response frame it already encodes.
func (w *streamWriter) writeResponse(b []byte) bool {
	if err := w.l.writeBytes(w.ctx, w.c, b); err != nil {
		w.quiet = true
		if w.ctx.Err() == nil {
			w.l.Log.Warn("edge link: an answer not sent", "id", text(w.id), "err", err)
		}
		return false
	}
	return true
}

// sendData sends one flushed piece as data frames, split where one frame
// would not carry it.
func (w *streamWriter) sendData(piece []byte) {
	for len(piece) > 0 {
		n := min(len(piece), maxDataBody)
		var b []byte
		for {
			if n == 0 {
				// The id alone fills a frame: nothing of the piece fits in one.
				w.quiet = true
				return
			}
			var err error
			b, err = json.Marshal(dataFrame{Type: "data", ID: w.id, Body: piece[:n]})
			if err == nil && len(b) <= maxFrame {
				break
			}
			n /= 2
		}
		if err := w.l.writeBytes(w.ctx, w.c, b); err != nil {
			w.quiet = true
			if w.ctx.Err() == nil {
				w.l.Log.Warn("edge link: a stream's piece not sent", "id", text(w.id), "err", err)
			}
			return
		}
		piece = piece[n:]
	}
}

func (l *Link) read(ctx context.Context, c *websocket.Conn, v any) error {
	_, b, err := c.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// write sends one frame. It waits on the link's context, with no budget of
// its own: the socket library closes the link when a write's context
// expires, so a budget would end the link whenever the machine is too
// loaded to finish a write in time, and serve would read that death as a
// closed socket instead of unanswered pings. The heard timer alone judges
// the link dead; a socket that truly takes nothing goes quiet with it, its
// pings unanswered.
func (l *Link) write(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return l.writeBytes(ctx, c, b)
}

// writeBytes sends b as the one frame it already encodes.
func (l *Link) writeBytes(ctx context.Context, c *websocket.Conn, b []byte) error {
	return c.Write(ctx, websocket.MessageText, b)
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
