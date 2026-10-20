package standin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"yardr.dev/yardr/internal/yardkey"
)

// The edge's half of the link (design yardr-hosted-050n, 3.2), as the
// router of yardr-hosted serves it at GET /edge/link (router/README.md, "The
// link"): the signed upgrade with the yard's nonce, a challenge signed over
// both nonces, the hello checked against the key the upgrade named, a
// welcome, then request frames numbered from 1 and signed over the session.
// A test holds each link the stand-in welcomed (Links) and sends down it
// what the router would, or what the router never would: a frame changed
// after signing, one sent twice, one from another link.

// EdgeFrame is a request frame as it travels down the link.
type EdgeFrame struct {
	Type     string      `json:"type"`
	ID       string      `json:"id"`
	Seq      uint64      `json:"seq"`
	Method   string      `json:"method"`
	Path     string      `json:"path"`
	Header   http.Header `json:"header"`
	Body     []byte      `json:"body"`
	Received string      `json:"received"`
	Queued   bool        `json:"queued"`
	Sig      string      `json:"sig"`
}

// EdgeAnswer is a yard's response frame.
type EdgeAnswer struct {
	ID     string      `json:"id"`
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}

// EdgeData is one piece of a yard's streamed answer: a data frame's id and
// body, in the order the yard flushed them.
type EdgeData struct {
	ID   string
	Body []byte
}

// EdgeLink is one link the stand-in welcomed.
type EdgeLink struct {
	e       *Edge
	c       *websocket.Conn
	session string
	done    chan struct{}

	mu      sync.Mutex
	seq     uint64
	ids     int
	waiting map[string]chan EdgeAnswer
	answers []EdgeAnswer
	acks    []string
	ackCh   chan string
	data    []EdgeData
	dataCh  chan EdgeData
	ends    []string
	endCh   chan string
	silent  bool
	pongs   int
	pongCh  chan struct{}
}

// Links gives each link the stand-in welcomed, as it is welcomed.
func (e *Edge) Links() <-chan *EdgeLink { return e.linkCh() }

func (e *Edge) linkCh() chan *EdgeLink {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.links == nil {
		e.links = make(chan *EdgeLink, 16)
	}
	return e.links
}

// Upgrades is how many signed upgrades the stand-in took, welcomed or not.
func (e *Edge) Upgrades() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.upgrades
}

// link serves GET /edge/link.
func (e *Edge) link(w http.ResponseWriter, r *http.Request) {
	b := e.behaviour()
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	from := r.Header.Get(yardkey.HeaderKey)
	if err := yardkey.VerifyRequest(from, e.PublicKey(), r.Method, r.URL.RequestURI(), r.Header.Get(yardkey.HeaderTime), r.Header.Get(yardkey.HeaderSig), nil, now()); err != nil {
		w.Header().Set("X-Edge-Refusal", "signature")
		http.Error(w, "edge: the link is not signed to this edge", http.StatusUnauthorized)
		return
	}
	yardNonce, err := yardkey.DecodeNonce(r.Header.Get("Yardr-Link-Nonce"))
	if err != nil {
		w.Header().Set("X-Edge-Refusal", "nonce")
		http.Error(w, "edge: no nonce", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	e.mu.Lock()
	e.upgrades++
	e.mu.Unlock()
	ctx := r.Context()
	edgeNonce := make([]byte, yardkey.NonceSize)
	rand.Read(edgeNonce)
	// What the challenge signs: this socket's nonces, unless the test has
	// the stand-in sign over another yard nonce, or with another key.
	signed := yardNonce
	if b.ChallengeOverNonce != nil {
		signed = b.ChallengeOverNonce
	}
	signer := e.key
	if b.ChallengeSigner != nil {
		signer = b.ChallengeSigner
	}
	if err := writeFrame(ctx, c, map[string]string{"type": "challenge", "nonce": yardkey.EncodeNonce(edgeNonce), "sig": yardkey.SignLink(signer, signed, edgeNonce)}); err != nil {
		return
	}
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	_, raw, err := c.Read(hctx)
	cancel()
	if err != nil {
		return
	}
	var hello struct{ Type, Sig string }
	if json.Unmarshal(raw, &hello) != nil || hello.Type != "hello" || yardkey.VerifyLink(from, yardNonce, edgeNonce, hello.Sig) != nil {
		c.Close(websocket.StatusPolicyViolation, "the hello does not answer this challenge")
		return
	}
	l := &EdgeLink{e: e, c: c, session: yardkey.Session(yardNonce, edgeNonce), done: make(chan struct{}), waiting: map[string]chan EdgeAnswer{},
		ackCh: make(chan string, 64), dataCh: make(chan EdgeData, 64), endCh: make(chan string, 64), pongCh: make(chan struct{}, 64)}
	if err := writeFrame(ctx, c, map[string]string{"type": "welcome"}); err != nil {
		return
	}
	e.linkCh() <- l
	defer close(l.done)
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		if l.isSilent() {
			continue
		}
		var f struct {
			Type string `json:"type"`
			EdgeAnswer
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		switch f.Type {
		case "ping":
			if writeFrame(ctx, c, map[string]string{"type": "pong"}) == nil {
				l.mu.Lock()
				l.pongs++
				l.mu.Unlock()
				select {
				case l.pongCh <- struct{}{}:
				default:
				}
			}
		case "response":
			l.mu.Lock()
			l.answers = append(l.answers, f.EdgeAnswer)
			ch := l.waiting[f.ID]
			delete(l.waiting, f.ID)
			l.mu.Unlock()
			if ch != nil {
				ch <- f.EdgeAnswer
			}
		case "ack":
			l.mu.Lock()
			l.acks = append(l.acks, f.ID)
			l.mu.Unlock()
			select {
			case l.ackCh <- f.ID:
			default:
			}
		case "data":
			d := EdgeData{ID: f.ID, Body: f.Body}
			l.mu.Lock()
			l.data = append(l.data, d)
			l.mu.Unlock()
			select {
			case l.dataCh <- d:
			default:
			}
		case "end":
			l.mu.Lock()
			l.ends = append(l.ends, f.ID)
			l.mu.Unlock()
			select {
			case l.endCh <- f.ID:
			default:
			}
		}
	}
}

func writeFrame(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

// Frame is the next request frame of this link, numbered and signed as the
// router signs one, and not yet sent: a test may change it after signing,
// or send it twice.
func (l *EdgeLink) Frame(method, path string, header http.Header, body []byte, queued bool) EdgeFrame {
	l.mu.Lock()
	l.seq++
	l.ids++
	f := EdgeFrame{Type: "request", ID: "r-" + strconv.Itoa(l.ids), Seq: l.seq, Method: method, Path: path,
		Header: header, Body: body, Received: time.Now().UTC().Format(time.RFC3339), Queued: queued}
	l.mu.Unlock()
	if f.Header == nil {
		f.Header = http.Header{}
	}
	if f.Body == nil {
		f.Body = []byte{}
	}
	f.Sig = l.Sign(l.e.key, f)
	return f
}

// Sign is f's signature by key over this link's session; a nil key is the
// edge's own.
func (l *EdgeLink) Sign(key ed25519.PrivateKey, f EdgeFrame) string {
	if key == nil {
		key = l.e.key
	}
	return yardkey.SignFrame(key, yardkey.Frame{Session: l.session, Seq: f.Seq, ID: f.ID, Method: f.Method, Path: f.Path,
		Header: f.Header, Body: f.Body, Received: f.Received, Queued: f.Queued})
}

// Send writes f down the link as it is.
func (l *EdgeLink) Send(ctx context.Context, f EdgeFrame) error {
	return writeFrame(ctx, l.c, f)
}

// Deliver sends f and waits for the yard's answer to its id.
func (l *EdgeLink) Deliver(ctx context.Context, f EdgeFrame) (EdgeAnswer, error) {
	ch := make(chan EdgeAnswer, 1)
	l.mu.Lock()
	l.waiting[f.ID] = ch
	l.mu.Unlock()
	if err := l.Send(ctx, f); err != nil {
		return EdgeAnswer{}, err
	}
	select {
	case a := <-ch:
		return a, nil
	case <-l.done:
		return EdgeAnswer{}, errors.New("the link closed before the yard answered")
	case <-ctx.Done():
		return EdgeAnswer{}, ctx.Err()
	}
}

// Answers are the yard's responses on this link so far, in the order they
// came.
func (l *EdgeLink) Answers() []EdgeAnswer {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]EdgeAnswer(nil), l.answers...)
}

// Acks are the ids the yard acked on this link so far, in the order they
// came: one per queued delivery it recorded or refused.
func (l *EdgeLink) Acks() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.acks...)
}

// AckEvents signals each id the yard acks.
func (l *EdgeLink) AckEvents() <-chan string { return l.ackCh }

// Data are the yard's streamed pieces on this link so far, in the order
// they came.
func (l *EdgeLink) Data() []EdgeData {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]EdgeData(nil), l.data...)
}

// DataEvents signals each piece of a streamed answer the yard sends.
func (l *EdgeLink) DataEvents() <-chan EdgeData { return l.dataCh }

// Ends are the ids the yard ended on this link so far, in the order they
// came.
func (l *EdgeLink) Ends() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ends...)
}

// EndEvents signals each id whose answer the yard ended.
func (l *EdgeLink) EndEvents() <-chan string { return l.endCh }

// SendEnd tells the yard a stream's caller went away: the yard cancels the
// request with that id, as the edge does when a device goes away.
func (l *EdgeLink) SendEnd(ctx context.Context, id string) error {
	return writeFrame(ctx, l.c, map[string]string{"type": "end", "id": id})
}

// Silence has the stand-in hear nothing more on the link and send nothing,
// with no close: a laptop's network gone with neither end told.
func (l *EdgeLink) Silence() {
	l.mu.Lock()
	l.silent = true
	l.mu.Unlock()
}

func (l *EdgeLink) isSilent() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.silent
}

// Pongs is how many of the yard's pings the stand-in has answered.
func (l *EdgeLink) Pongs() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pongs
}

// PongEvents signals each ping answered by the stand-in edge.
func (l *EdgeLink) PongEvents() <-chan struct{} { return l.pongCh }

// Done is closed when the link has ended at the stand-in.
func (l *EdgeLink) Done() <-chan struct{} { return l.done }

// Close ends the link from the edge's side, as a router that restarts does.
func (l *EdgeLink) Close() {
	l.c.Close(websocket.StatusGoingAway, "the edge is going away")
}
