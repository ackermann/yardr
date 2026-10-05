package yards_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// logBuffer is a server's log, written by its handlers and read by the test.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// pairYard is a yard that pairs: its store in a file the test can read, its
// key, and its receiver on a listener of its own, logging to logs.
type pairYard struct {
	name string
	own  *store.Store
	file string
	key  ed25519.PrivateKey
	rc   *yards.Receiver
	srv  *httptest.Server
	logs *logBuffer
}

func newPairYard(t *testing.T, name string) *pairYard {
	t.Helper()
	dir := t.TempDir()
	y := &pairYard{name: name, file: filepath.Join(dir, "yard.db"), logs: &logBuffer{}}
	y.own = storetest.Open(t, y.file)
	var err error
	if y.key, _, err = yardkey.Ensure(filepath.Join(dir, "peer.key")); err != nil {
		t.Fatal(err)
	}
	y.rc = yards.NewReceiver(y.own, filepath.Join(dir, "peer.key"), slog.New(slog.NewTextHandler(y.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	y.srv = httptest.NewServer(y.rc.Handler())
	t.Cleanup(y.srv.Close)
	return y
}

func (y *pairYard) pub() string           { return yardkey.Public(y.key) }
func (y *pairYard) caller() *yards.Caller { return &yards.Caller{Key: y.key} }

// open opens a pairing on y, to save the other yard as as ("" for the name
// it gives), and returns it with its code as both sides compute from it.
func (y *pairYard) open(t *testing.T, as string) (*store.PairingCode, string) {
	t.Helper()
	p, err := y.own.PairOpen(context.Background(), store.PairOffer{Name: y.name, Key: y.pub(), URL: y.srv.URL, As: as}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	code, err := pairing.CleanCode(p.Code)
	if err != nil {
		t.Fatal(err)
	}
	return p, code
}

func (y *pairYard) pairing(t *testing.T) *store.Pairing {
	t.Helper()
	p, err := y.own.GetPairing(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (y *pairYard) peers(t *testing.T) []store.Peer {
	t.Helper()
	ps, err := y.own.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func (y *pairYard) events(t *testing.T, kind string) []store.Event {
	t.Helper()
	evs, err := y.own.Events(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// refusals are the reasons of y's peer_pair_refused events, oldest first.
func (y *pairYard) refusals(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, e := range y.events(t, "peer_pair_refused") {
		r, _ := e.Data["reason"].(string)
		out = append(out, r)
	}
	return out
}

// cheapKeys makes a key from a code cost little, for a test that makes many.
func cheapKeys(t *testing.T) {
	t.Helper()
	old := pairing.Stretch
	pairing.Stretch = 16
	t.Cleanup(func() { pairing.Stretch = old })
}

// refusedFor reports whether err is the other yard's refusal for reason.
func refusedFor(err error, reason string) bool {
	var no *pairing.Refused
	return errors.As(err, &no) && no.Reason == reason
}

// nowhere fails when one of secrets is in the yard's events, its log or its
// store file.
func (y *pairYard) nowhere(t *testing.T, secrets map[string]string) {
	t.Helper()
	evs, err := json.Marshal(y.events(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(y.file)
	if err != nil {
		t.Fatal(err)
	}
	for where, text := range map[string][]byte{"events": evs, "log": []byte(y.logs.String()), "store file": file} {
		for what, s := range secrets {
			if s != "" && bytes.Contains(bytes.ToUpper(text), bytes.ToUpper([]byte(s))) {
				t.Errorf("%s: %s is in its %s", y.name, what, where)
			}
		}
	}
}

// Two yards pair from a code: the one that is called opens the pairing,
// the other calls with the code, both compute the same check, each operator
// confirms, each saves the other, and a ping crosses each way. What is
// saved is what invite, add and allow would have saved. The code is in no
// event, no log and neither store.
func TestPairing(t *testing.T) {
	cheapKeys(t)
	north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
	ctx := context.Background()
	opened, code := north.open(t, "")
	if opened.State != store.PairOpened || opened.Expires.Sub(opened.Expires.Add(-pairing.TTL)) != pairing.TTL || len(opened.Code) != 9 {
		t.Fatalf("opened = %+v", opened)
	}

	called, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := north.pairing(t)
	if p.State != store.PairCalled || p.Check != called.Check() || p.Calls != "laptop" || p.Peer != "laptop" || p.Key != laptop.pub() {
		t.Fatalf("north's pairing after the call = %+v; laptop's check %s", p, called.Check())
	}
	if called.Host.Name != "north" || called.Host.Key != north.pub() || called.Host.URL != north.srv.URL {
		t.Fatalf("laptop was told %+v", called.Host)
	}
	if len(north.peers(t))+len(laptop.peers(t)) != 0 {
		t.Fatal("a peer was saved before anyone confirmed")
	}

	// Laptop's operator confirms first: nothing is saved while north's has not.
	out, err := laptop.caller().PairAnswer(ctx, called, true)
	if err != nil || out.State != pairing.Waiting {
		t.Fatalf("laptop's yes before north's = %+v, %v", out, err)
	}
	if len(north.peers(t)) != 0 {
		t.Fatal("north saved laptop on laptop's word alone")
	}
	if p, err = north.own.PairAnswer(ctx, opened.ID, true, "jo"); err != nil || p.State != store.PairDone {
		t.Fatalf("north's yes = %+v, %v", p, err)
	}
	for range 2 { // asked again, the answer is the same
		if out, err = laptop.caller().PairAnswer(ctx, called, true); err != nil || out.State != pairing.Confirmed {
			t.Fatalf("laptop asked how it stands: %+v, %v", out, err)
		}
	}
	toNorth, err := laptop.own.PairPeer(ctx, store.Peer{Name: "north", Key: called.Host.Key, URL: called.URL, Calls: called.Host.Name}, "jo")
	if err != nil {
		t.Fatal(err)
	}

	// The stored state is the one the invite lines lead to, with ping allowed.
	onNorth, onLaptop := north.peers(t), laptop.peers(t)
	if len(onNorth) != 1 || onNorth[0].Name != "laptop" || onNorth[0].Key != laptop.pub() || onNorth[0].URL != "" || onNorth[0].Calls != "laptop" ||
		strings.Join(onNorth[0].Send, ",") != "ping" || strings.Join(onNorth[0].Receive, ",") != "ping" {
		t.Fatalf("north's peers = %+v", onNorth)
	}
	if len(onLaptop) != 1 || onLaptop[0].Name != "north" || onLaptop[0].Key != north.pub() || onLaptop[0].URL != north.srv.URL ||
		strings.Join(onLaptop[0].Send, ",") != "ping" || strings.Join(onLaptop[0].Receive, ",") != "ping" {
		t.Fatalf("laptop's peers = %+v", onLaptop)
	}

	// A ping from laptop: posted, and its pong asked for.
	ping, err := laptop.own.PeerSend(ctx, "north", store.PeerPing, nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.caller().Push(ctx, *toNorth, ping.PeerMessage); err != nil {
		t.Fatal(err)
	}
	if err := laptop.own.PeerDelivered(ctx, "north", ping.Seq); err != nil {
		t.Fatal(err)
	}
	batch, err := laptop.caller().Pull(ctx, *toNorth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.own.PeerPulled(ctx, "north", batch); err != nil {
		t.Fatal(err)
	}
	if evs := laptop.events(t, "peer_message_received"); len(evs) != 1 || evs[0].Data["re"] != ping.ID {
		t.Fatalf("laptop's pong = %+v", evs)
	}
	// And one from north, which waits to be asked for; its pong is posted.
	ping, err = north.own.PeerSend(ctx, "laptop", store.PeerPing, nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	now, _ := laptop.own.GetPeer(ctx, "north")
	if batch, err = laptop.caller().Pull(ctx, *toNorth, now.Pulled); err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.own.PeerPulled(ctx, "north", batch); err != nil {
		t.Fatal(err)
	}
	pongs, err := laptop.own.PeerOutbox(ctx, "north", 0)
	if err != nil || len(pongs) != 1 || pongs[0].Re != ping.ID {
		t.Fatalf("laptop's outbox = %+v, %v", pongs, err)
	}
	if err := laptop.caller().Push(ctx, *toNorth, pongs[0].PeerMessage); err != nil {
		t.Fatal(err)
	}
	if evs := north.events(t, "peer_message_received"); len(evs) != 2 || evs[1].Data["re"] != ping.ID {
		t.Fatalf("north's pong = %+v", evs)
	}

	// Events: opened and completed on the host, completed on the caller,
	// and what the commands by hand log.
	if evs := north.events(t, "peer_pair_opened"); len(evs) != 1 || evs[0].Data["pairing"] != opened.ID || evs[0].Data["url"] != north.srv.URL || evs[0].Actor != "jo" {
		t.Fatalf("north's peer_pair_opened = %+v", evs)
	}
	if evs := north.events(t, "peer_paired"); len(evs) != 1 || evs[0].Data["role"] != "host" || evs[0].Data["peer"] != "laptop" || evs[0].Data["key"] != laptop.pub() || evs[0].Actor != "jo" {
		t.Fatalf("north's peer_paired = %+v", evs)
	}
	if evs := laptop.events(t, "peer_paired"); len(evs) != 1 || evs[0].Data["role"] != "joiner" || evs[0].Data["peer"] != "north" || evs[0].Data["url"] != north.srv.URL {
		t.Fatalf("laptop's peer_paired = %+v", evs)
	}
	for _, y := range []*pairYard{north, laptop} {
		if len(y.events(t, "peer_added")) != 1 || len(y.events(t, "peer_allowed")) != 1 || len(y.refusals(t)) != 0 {
			t.Errorf("%s: events %+v", y.name, y.events(t, ""))
		}
	}

	// The code, and the key stretched from it, are nowhere.
	key := pairing.Key(code, called.Commit)
	secrets := map[string]string{
		"the code as shown": opened.Code, "the code": code,
		"the key (raw)": string(key), "the key (hex)": hex.EncodeToString(key),
		"the key (base64url)": base64.RawURLEncoding.EncodeToString(key), "the key (base64)": base64.StdEncoding.EncodeToString(key),
	}
	north.nowhere(t, secrets)
	laptop.nowhere(t, secrets)
	if !strings.Contains(north.logs.String(), "a yard called with the code") {
		t.Fatalf("north's log does not say a yard called, so it proves nothing:\n%s", north.logs.String())
	}
}

// A wrong code is refused and counted, and the fifth ends the pairing: the
// right code is then good for nothing. Four wrong ones leave it open.
func TestPairingWrongCode(t *testing.T) {
	cheapKeys(t)
	north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
	ctx := context.Background()
	opened, code := north.open(t, "")
	wrong := "00000000"
	if wrong == code {
		wrong = "11111111"
	}
	for i := 1; i <= pairing.MaxTries; i++ {
		if _, err := laptop.caller().PairCall(ctx, north.srv.URL, wrong, "laptop", nil); !refusedFor(err, pairing.WrongCode) {
			t.Fatalf("wrong code %d = %v", i, err)
		}
		p := north.pairing(t)
		if want := map[bool]string{false: store.PairOpened, true: store.PairEnded}[i == pairing.MaxTries]; p.Tries != i || p.State != want {
			t.Fatalf("after %d wrong codes: %+v", i, p)
		}
	}
	if p := north.pairing(t); p.Reason != pairing.WrongCode {
		t.Fatalf("ended pairing = %+v", p)
	}
	if _, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil); !refusedFor(err, pairing.NoPairing) {
		t.Fatalf("the right code after the pairing ended = %v", err)
	}
	if got := north.refusals(t); len(got) != 1 || got[0] != pairing.WrongCode {
		t.Fatalf("north's refusals = %v", got)
	}
	if evs := north.events(t, "peer_pair_refused"); evs[0].Data["wrong_codes"] != float64(pairing.MaxTries) && evs[0].Data["wrong_codes"] != pairing.MaxTries {
		t.Fatalf("the refusal does not count the tries: %+v", evs[0].Data)
	}
	if len(north.peers(t))+len(laptop.peers(t)) != 0 {
		t.Fatal("a peer was saved")
	}
	secrets := map[string]string{"the code as shown": opened.Code, "the code": code, "the wrong code": wrong}
	north.nowhere(t, secrets)

	// A pairing that took four wrong codes still takes the right one.
	_, code = north.open(t, "")
	for range pairing.MaxTries - 1 {
		if _, err := laptop.caller().PairCall(ctx, north.srv.URL, wrong, "laptop", nil); !refusedFor(err, pairing.WrongCode) {
			t.Fatal(err)
		}
	}
	if _, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil); err != nil {
		t.Fatalf("the right code after four wrong ones = %v", err)
	}
	if p := north.pairing(t); p.State != store.PairCalled || p.Tries != pairing.MaxTries-1 {
		t.Fatalf("pairing = %+v", p)
	}
}

// A code is good for one pairing: a second yard that calls with it, once a
// first has, is refused, and the first's pairing stands as it was. With no
// pairing open at all, a call is told so.
func TestPairingCodeIsUsedOnce(t *testing.T) {
	cheapKeys(t)
	north, laptop, third := newPairYard(t, "north"), newPairYard(t, "laptop"), newPairYard(t, "third")
	ctx := context.Background()
	if _, err := laptop.caller().PairCall(ctx, north.srv.URL, "K7QM2XPD", "laptop", nil); !refusedFor(err, pairing.NoPairing) {
		t.Fatalf("a call with no pairing open = %v", err)
	}
	opened, code := north.open(t, "")
	called, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.caller().PairCall(ctx, north.srv.URL, code, "third", nil); !refusedFor(err, pairing.Used) {
		t.Fatalf("a second yard with the code = %v", err)
	}
	if p := north.pairing(t); p.State != store.PairCalled || p.Key != laptop.pub() || p.Check != called.Check() || p.Tries != 0 {
		t.Fatalf("north's pairing after the second call = %+v", p)
	}
	// Nor while one is open: a yard has one pairing at a time.
	if _, err := north.own.PairOpen(ctx, store.PairOffer{Name: "north", Key: north.pub(), URL: north.srv.URL}, "jo"); err == nil || !strings.Contains(err.Error(), "a pairing is open already") {
		t.Fatalf("a second pairing = %v", err)
	}
	// It ends with the pairing, and once done the code is still used up.
	if _, err := north.own.PairAnswer(ctx, opened.ID, true, "jo"); err != nil {
		t.Fatal(err)
	}
	if out, err := laptop.caller().PairAnswer(ctx, called, true); err != nil || out.State != pairing.Confirmed {
		t.Fatalf("%+v, %v", out, err)
	}
	if _, err := third.caller().PairCall(ctx, north.srv.URL, code, "third", nil); !refusedFor(err, pairing.Used) {
		t.Fatalf("the code after its pairing was done = %v", err)
	}
	if ps := north.peers(t); len(ps) != 1 || ps[0].Name != "laptop" {
		t.Fatalf("north's peers = %+v", ps)
	}
}

// Either operator declining leaves nothing saved on either yard, whoever
// answered first, and each yard is told the other declined.
func TestPairingDeclined(t *testing.T) {
	cheapKeys(t)
	ctx := context.Background()
	t.Run("by the host", func(t *testing.T) {
		north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
		opened, code := north.open(t, "")
		called, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := laptop.caller().PairAnswer(ctx, called, true); err != nil || out.State != pairing.Waiting {
			t.Fatalf("%+v, %v", out, err)
		}
		if p, err := north.own.PairAnswer(ctx, opened.ID, false, "jo"); err != nil || p.State != store.PairEnded || p.Reason != pairing.Declined {
			t.Fatalf("north's no = %+v, %v", p, err)
		}
		out, err := laptop.caller().PairWait(ctx, called, 0)
		if err != nil || out.State != pairing.Ended || out.Reason != pairing.Declined {
			t.Fatalf("laptop is told %+v, %v", out, err)
		}
		if len(north.peers(t))+len(laptop.peers(t)) != 0 {
			t.Fatal("a peer was saved")
		}
		if got := north.refusals(t); len(got) != 1 || got[0] != pairing.Declined {
			t.Fatalf("north's refusals = %v", got)
		}
		// A yes after the no changes nothing.
		if p, err := north.own.PairAnswer(ctx, opened.ID, true, "jo"); err != nil || p.State != store.PairEnded {
			t.Fatalf("a yes after the no = %+v, %v", p, err)
		}
		if len(north.peers(t)) != 0 {
			t.Fatal("a peer was saved after the no")
		}
	})
	t.Run("by the caller", func(t *testing.T) {
		north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
		opened, code := north.open(t, "")
		called, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil)
		if err != nil {
			t.Fatal(err)
		}
		// North's operator has confirmed already: laptop's no still ends it.
		if p, err := north.own.PairAnswer(ctx, opened.ID, true, "jo"); err != nil || p.State != store.PairCalled || !p.Confirmed {
			t.Fatalf("north's yes = %+v, %v", p, err)
		}
		out, err := laptop.caller().PairAnswer(ctx, called, false)
		if err != nil || out.State != pairing.Ended || out.Reason != pairing.PeerDeclined {
			t.Fatalf("laptop's no = %+v, %v", out, err)
		}
		if p := north.pairing(t); p.State != store.PairEnded || p.Reason != pairing.PeerDeclined {
			t.Fatalf("north's pairing = %+v", p)
		}
		// A yes after the no is no yes.
		if out, err := laptop.caller().PairAnswer(ctx, called, true); err != nil || out.State != pairing.Ended {
			t.Fatalf("a yes after the no = %+v, %v", out, err)
		}
		if len(north.peers(t))+len(laptop.peers(t)) != 0 {
			t.Fatal("a peer was saved")
		}
		if got := north.refusals(t); len(got) != 1 || got[0] != pairing.PeerDeclined {
			t.Fatalf("north's refusals = %v", got)
		}
	})
	t.Run("ended by the host", func(t *testing.T) {
		north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
		opened, code := north.open(t, "")
		if err := north.own.PairClose(ctx, "", "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil); !refusedFor(err, pairing.NoPairing) {
			t.Fatalf("a call after the pairing was ended = %v", err)
		}
		if p := north.pairing(t); p.ID != opened.ID || p.Reason != pairing.Closed {
			t.Fatalf("north's pairing = %+v", p)
		}
	})
}

// A yard whose name or key is a peer already is refused in the words peer
// add refuses it with, before either operator is asked, and the pairing
// ends. --name saves it under another name.
func TestPairingAPeerAlready(t *testing.T) {
	cheapKeys(t)
	ctx := context.Background()
	other := newPairYard(t, "other")
	for name, tc := range map[string]struct {
		add func(laptop *pairYard) store.Peer // what north has already
	}{
		"the name": {add: func(*pairYard) store.Peer { return store.Peer{Name: "laptop", Key: other.pub()} }},
		"the key":  {add: func(laptop *pairYard) store.Peer { return store.Peer{Name: "old", Key: laptop.pub()} }},
	} {
		t.Run(name, func(t *testing.T) {
			north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
			if _, err := north.own.AddPeer(ctx, tc.add(laptop), "jo"); err != nil {
				t.Fatal(err)
			}
			_, want := north.own.AddPeer(ctx, store.Peer{Name: "laptop", Key: laptop.pub()}, "jo")
			if want == nil {
				t.Fatal("peer add took it")
			}
			_, code := north.open(t, "")
			if _, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil); !refusedFor(err, pairing.AlreadyPeer) {
				t.Fatalf("the call = %v", err)
			}
			p := north.pairing(t)
			if p.State != store.PairEnded || p.Reason != pairing.AlreadyPeer || p.Detail != want.Error() || p.Check == "" {
				t.Fatalf("north's pairing = %+v\nwant the words of peer add: %v", p, want)
			}
			if ps := north.peers(t); len(ps) != 1 {
				t.Fatalf("north's peers = %+v", ps)
			}
			if got := north.refusals(t); len(got) != 1 || got[0] != pairing.AlreadyPeer {
				t.Fatalf("north's refusals = %v", got)
			}
		})
	}

	// The calling side asks its own store, in the same words.
	north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
	if _, err := laptop.own.AddPeer(ctx, store.Peer{Name: "north", Key: other.pub()}, "jo"); err != nil {
		t.Fatal(err)
	}
	_, want := laptop.own.AddPeer(ctx, store.Peer{Name: "north", Key: north.pub()}, "jo")
	if err := laptop.own.CheckPeer(ctx, store.Peer{Name: "north", Key: north.pub()}); err == nil || err.Error() != want.Error() {
		t.Fatalf("CheckPeer = %v, peer add = %v", err, want)
	}
	if err := laptop.own.CheckPeer(ctx, store.Peer{Name: "the-north", Key: north.pub()}); err != nil {
		t.Fatalf("CheckPeer under another name = %v", err)
	}

	// A name the host gave with --name is the one the yard is saved under.
	if _, err := north.own.AddPeer(ctx, store.Peer{Name: "laptop", Key: other.pub()}, "jo"); err != nil {
		t.Fatal(err)
	}
	opened, code := north.open(t, "jos-laptop")
	called, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := north.pairing(t); p.Peer != "jos-laptop" || p.Calls != "laptop" {
		t.Fatalf("north's pairing = %+v", p)
	}
	if _, err := north.own.PairAnswer(ctx, opened.ID, true, "jo"); err != nil {
		t.Fatal(err)
	}
	if out, err := laptop.caller().PairAnswer(ctx, called, true); err != nil || out.State != pairing.Confirmed {
		t.Fatalf("%+v, %v", out, err)
	}
	if p, err := north.own.GetPeer(ctx, "jos-laptop"); err != nil || p.Key != laptop.pub() || p.Calls != "laptop" {
		t.Fatalf("north's jos-laptop = %+v, %v", p, err)
	}
	// And one that is a peer already is refused when the pairing is opened.
	if _, err := north.own.PairOpen(ctx, store.PairOffer{Name: "north", Key: north.pub(), URL: north.srv.URL, As: "laptop"}, "jo"); err == nil || !strings.Contains(err.Error(), "peer laptop is added already") {
		t.Fatalf("a pairing opened for a name that is a peer = %v", err)
	}
	// A yard is not paired with itself.
	_, code = north.open(t, "")
	if _, err := north.caller().PairCall(ctx, north.srv.URL, code, "north", nil); !refusedFor(err, pairing.Own) {
		t.Fatalf("a yard calling itself = %v", err)
	}
}

// The calling yard's word on the check counts only signed with the key it
// called with: another yard that knows the session, which is no secret,
// cannot confirm or decline in its place.
func TestPairingConfirmIsSigned(t *testing.T) {
	cheapKeys(t)
	north, laptop, third := newPairYard(t, "north"), newPairYard(t, "laptop"), newPairYard(t, "third")
	ctx := context.Background()
	_, code := north.open(t, "")
	called, err := laptop.caller().PairCall(ctx, north.srv.URL, code, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, yes := range []bool{true, false} {
		if out, err := third.caller().PairAnswer(ctx, called, yes); err == nil || !errors.Is(err, yards.ErrUnsigned) {
			t.Fatalf("a third yard's word (%v) = %+v, %v", yes, out, err)
		}
	}
	// Signed by the third yard, under laptop's name for the key.
	body, _ := json.Marshal(pairing.Answer{Session: called.Session(), Confirm: true})
	ts, sig := yardkey.SignRequest(third.key, north.pub(), "POST", pairing.PathConfirm, time.Now(), body)
	req, _ := http.NewRequest("POST", north.srv.URL+pairing.PathConfirm, bytes.NewReader(body))
	req.Header.Set(yardkey.HeaderKey, laptop.pub())
	req.Header.Set(yardkey.HeaderTime, ts)
	req.Header.Set(yardkey.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get(yardkey.HeaderSig) != "" {
		t.Fatalf("a forged word was answered %d", resp.StatusCode)
	}
	if p := north.pairing(t); p.State != store.PairCalled || p.PeerConfirmed {
		t.Fatalf("north's pairing after forged words = %+v", p)
	}
	// Laptop's own still counts.
	if out, err := laptop.caller().PairAnswer(ctx, called, true); err != nil || out.State != pairing.Waiting {
		t.Fatalf("laptop's word = %+v, %v", out, err)
	}
	if p := north.pairing(t); !p.PeerConfirmed {
		t.Fatalf("north's pairing = %+v", p)
	}
}

// middle stands between a calling yard and the yard it means to call, and
// puts its own key in both directions: to the caller it is the host, to the
// host it is the caller. It is given the code, which is the worst case: one
// that does not know it gets no further than a wrong try.
type middle struct {
	t    *testing.T
	key  ed25519.PrivateKey
	code string
	host *pairYard
	srv  *httptest.Server

	mu     sync.Mutex
	asHost pairing.Party // what it told the caller it is
	called *yards.Called // its own pairing with the host
}

func newMiddle(t *testing.T, host *pairYard, code string) *middle {
	t.Helper()
	m := &middle{t: t, code: code, host: host}
	var err error
	if m.key, _, err = yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key")); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+pairing.PathHello, m.hello)
	mux.HandleFunc("POST "+pairing.PathReveal, m.reveal)
	mux.HandleFunc("POST "+pairing.PathConfirm, m.confirm)
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *middle) hello(w http.ResponseWriter, r *http.Request) {
	var h pairing.Hello
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		m.t.Error(err)
	}
	// To the host it is the caller, under the caller's name and its own key.
	called, err := (&yards.Caller{Key: m.key}).PairCall(r.Context(), m.host.srv.URL, m.code, "laptop", nil)
	if err != nil {
		m.t.Errorf("the middle's own call: %v", err)
	}
	m.mu.Lock()
	m.called = called
	// To the caller it is the host, under the host's name and URL.
	m.asHost = pairing.Party{Nonce: pairing.NewNonce(), Name: m.host.name, Key: yardkey.Public(m.key), URL: m.host.srv.URL}
	offer := pairing.NewOffer(pairing.Key(m.code, h.Commit), h.Commit, m.asHost)
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(offer)
}

func (m *middle) reveal(w http.ResponseWriter, r *http.Request) {
	var rv pairing.Reveal
	if err := json.NewDecoder(r.Body).Decode(&rv); err != nil {
		m.t.Error(err)
	}
	t := pairing.Transcript{Commit: rv.Commit, Joiner: rv.Party, Host: m.asHost}
	_ = json.NewEncoder(w).Encode(map[string]string{"session": t.Session()})
}

// confirm answers the caller that all is confirmed, signed with the key the
// caller took for the host's.
func (m *middle) confirm(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(pairing.Outcome{State: pairing.Confirmed})
	w.Header().Set(yardkey.HeaderSig, yardkey.SignResponse(m.key, 200, r.Header.Get(yardkey.HeaderSig), body))
	_, _ = w.Write(body)
}

// A middle that swaps the keys, and knows the code, is what the check is
// for: each yard computes its check from the key it was given, so the two
// checks differ, the operators who compare them decline, and nothing is
// saved on either side. Neither yard has the other's key at any point.
func TestPairingMiddleSwapsAKey(t *testing.T) {
	cheapKeys(t)
	north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
	ctx := context.Background()
	opened, code := north.open(t, "")
	m := newMiddle(t, north, code)

	called, err := laptop.caller().PairCall(ctx, m.srv.URL, code, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := north.pairing(t)
	if p.State != store.PairCalled || called.Host.Name != "north" || p.Calls != "laptop" {
		t.Fatalf("the middle was not taken for the other yard, so this proves nothing: north %+v, laptop %+v", p, called.Host)
	}
	if p.Key == laptop.pub() || called.Host.Key == north.pub() {
		t.Fatal("no key was swapped, so this proves nothing")
	}
	if p.Check == called.Check() {
		t.Fatalf("both yards show %s with a swapped key between them", p.Check)
	}
	// The middle's own checks are the two yards', one each: it cannot make
	// them agree, having committed to its nonce before it saw the other's.
	if m.called.Check() != p.Check {
		t.Fatalf("the middle's check with north is %s, north's is %s", m.called.Check(), p.Check)
	}

	// Each operator, seeing another check than the other reads out, says no.
	if p, err := north.own.PairAnswer(ctx, opened.ID, false, "jo"); err != nil || p.State != store.PairEnded {
		t.Fatalf("north's no = %+v, %v", p, err)
	}
	_, _ = laptop.caller().PairAnswer(ctx, called, false) // to the middle, which says what it likes
	if len(north.peers(t))+len(laptop.peers(t)) != 0 {
		t.Fatalf("a peer was saved: north %+v, laptop %+v", north.peers(t), laptop.peers(t))
	}
	// And the middle, which says yes to anything, cannot confirm for laptop
	// at north any more: the pairing has ended there.
	if out, err := (&yards.Caller{Key: m.key}).PairAnswer(ctx, m.called, true); err != nil || out.State != pairing.Ended {
		t.Fatalf("the middle's yes after north's no = %+v, %v", out, err)
	}
	if len(north.peers(t)) != 0 {
		t.Fatal("north saved the middle")
	}
}

// A middle that does not know the code and changes the host's answer on its
// way is found out before anyone is asked: the answer was not made with the
// code.
func TestPairingMiddleWithoutTheCode(t *testing.T) {
	cheapKeys(t)
	north, laptop := newPairYard(t, "north"), newPairYard(t, "laptop")
	ctx := context.Background()
	_, code := north.open(t, "")
	swap, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key"))
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Post(north.srv.URL+r.URL.Path, "application/json", r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var offer pairing.Offer
		if r.URL.Path == pairing.PathHello && json.Unmarshal(body, &offer) == nil {
			offer.Key = yardkey.Public(swap)
			body, _ = json.Marshal(offer)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}))
	defer relay.Close()
	if _, err := laptop.caller().PairCall(ctx, relay.URL, code, "laptop", nil); !errors.Is(err, yards.ErrNotTheCode) {
		t.Fatalf("a call through a middle that changed the key = %v", err)
	}
	if p := north.pairing(t); p.State != store.PairOpened {
		t.Fatalf("north's pairing = %+v", p)
	}
	// Through one that changes nothing, the yards pair as if it were not there.
	relay2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Post(north.srv.URL+r.URL.Path, "application/json", r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer relay2.Close()
	called, err := laptop.caller().PairCall(ctx, relay2.URL, code, "laptop", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := north.pairing(t); p.Check != called.Check() || p.Key != laptop.pub() {
		t.Fatalf("north's pairing = %+v, laptop's check %s", p, called.Check())
	}
}

// What is no message of a pairing is answered 400 and counts as no try.
func TestPairingBadRequests(t *testing.T) {
	cheapKeys(t)
	north := newPairYard(t, "north")
	north.open(t, "")
	for _, path := range []string{pairing.PathHello, pairing.PathReveal, pairing.PathConfirm} {
		for _, body := range []string{"", "{", `{"commit":"x","mac":"y"}`, `[1]`} {
			resp, err := http.Post(north.srv.URL+path, "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			// A reveal before any hello is refused as such, and a confirm of
			// no session finds no pairing.
			if resp.StatusCode != http.StatusBadRequest && !(path != pairing.PathHello && body == `{"commit":"x","mac":"y"}`) {
				t.Errorf("POST %s %q: status %d", path, body, resp.StatusCode)
			}
		}
	}
	if p := north.pairing(t); p.State != store.PairOpened || p.Tries != 0 {
		t.Fatalf("north's pairing = %+v", p)
	}
}
