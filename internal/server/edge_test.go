package server

import (
	"bytes"
	"context"
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

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

const hookToken = "s3cret-token"

// edgeYard is a yard with the operator jo, jo's device phone, a token hook
// adhoc and the yard's key, connected to a stand-in edge over TLS when
// connect says so, as yardr edge connect leaves one. It returns the
// device's token and the edge setting's value.
func edgeYard(t *testing.T, c *config.Config, edge *standin.Edge, srv *httptest.Server, connect bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(c.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(ctx)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.AddUser(ctx, "jo", "jo")
	must(err)
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	d, err := st.AddDevice(ctx, "jo", "phone", "jo")
	must(err)
	secret := filepath.Join(c.Home, "adhoc.secret")
	must(os.WriteFile(secret, []byte(hookToken+"\n"), 0o600))
	_, err = st.PutHook(ctx, store.Hook{Name: "adhoc", Source: "token", SecretFile: secret}, "jo")
	must(err)
	_, _, err = yardkey.Ensure(c.PeerKey())
	must(err)
	value := store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "jo"}.String()
	if connect {
		must(st.SetSetting(ctx, store.EdgeSetting, value, "jo"))
	}
	return d.Token, value
}

// stubEdge serves a stand-in edge over TLS, and has the server's link trust
// its certificate and dial again at once, for this test.
func stubEdge(t *testing.T) (*standin.Edge, *httptest.Server) {
	t.Helper()
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(edge)
	t.Cleanup(srv.Close)
	linkTest = func(l *edgelink.Link) {
		l.HTTP = srv.Client()
		l.Backoff, l.BackoffMax = 10*time.Millisecond, 20*time.Millisecond
	}
	t.Cleanup(func() { linkTest = nil })
	return edge, srv
}

func nextLink(t *testing.T, edge *standin.Edge) *standin.EdgeLink {
	t.Helper()
	// The server keeps a preview link beside its yard link; these
	// tests want the yard's, so a preview link is put aside.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l := <-edge.Links():
			if l.Header.Get("Yardr-Link-Role") == "preview" {
				continue
			}
			return l
		case <-deadline:
			t.Fatal("the server's link never came up at the edge")
			return nil
		}
	}
}

// edgeAwayStore gives each subscription what its script says: "ends" a
// stream that ends when end is closed, as the server's does when it
// replaces itself, "away" no answer, as a client's is while the server
// does not answer; once the script is used up, the store's own.
type edgeAwayStore struct {
	store.API
	script   []string
	end      chan struct{}
	attempts chan uint64
	mu       sync.Mutex
	subs     int
}

func (s *edgeAwayStore) Subscribe(ctx context.Context, after uint64) (<-chan store.Event, error) {
	s.mu.Lock()
	s.subs++
	n := s.subs
	s.mu.Unlock()
	if s.attempts != nil {
		s.attempts <- after
	}
	if n > len(s.script) {
		return s.API.Subscribe(ctx, after)
	}
	if s.script[n-1] == "away" {
		return nil, errors.New("yardr server: no server")
	}
	out := make(chan store.Event)
	go func() {
		defer close(out)
		select {
		case <-s.end:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

func overLink(t *testing.T, l *standin.EdgeLink, path string, header http.Header, body string) standin.EdgeAnswer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := l.Deliver(ctx, l.Frame(http.MethodPost, path, header, []byte(body), false))
	if err != nil {
		t.Fatalf("%s over the link: %v", path, err)
	}
	return a
}

// waitEvent is the first event of kind the yard logged, waiting for it.
func waitEvent(t *testing.T, cl store.API, kind string, ok func(store.Event) bool) store.Event {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs, err := cl.Events(context.Background(), "", 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == kind && (ok == nil || ok(e)) {
				return e
			}
		}
	}
	t.Fatalf("no %s event", kind)
	return store.Event{}
}

// With the edge set and no hook_addr, the server dials the edge and serves
// what comes down the link with the listener's own handlers: a delivery is
// verified by the hook's secret, which the edge never holds (the right token
// recorded, a wrong one 401); a device's call is served as the device, and
// GiveSecret without an eligible login-input ask is refused, since no other
// secret crosses the edge; /peers/ is not served through the edge. The link
// is told as an event and in its state, and nothing listens on a port.
func TestEdgeLinkServesTheListenersHandlersWithNoAddress(t *testing.T) {
	edge, srv := stubEdge(t)
	c := home(t)
	token, _ := edgeYard(t, c, edge, srv, true)
	start(t, &runner{c: c})
	l := nextLink(t, edge)
	cl := rpc.NewClient(c.Socket())
	defer cl.Close(context.Background())

	bearer := func(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	// A token delivery names its id: the hook's source repeats bodies, and
	// takes none that does not.
	delivery := bearer(hookToken)
	delivery.Set("Yard-Delivery", "deploy-1")
	if a := overLink(t, l, "/hooks/adhoc", delivery, `{"text":"deployed"}`); a.Status != http.StatusOK {
		t.Errorf("a delivery with the hook's token answered %d %s", a.Status, a.Body)
	}
	if a := overLink(t, l, "/hooks/adhoc", bearer("not-it"), `{"text":"forged"}`); a.Status != http.StatusUnauthorized {
		t.Errorf("a delivery with another token answered %d %s, want 401", a.Status, a.Body)
	}
	got := waitEvent(t, cl, "hook", nil)
	if got.Data["text"] != "deployed" {
		t.Errorf("recorded %+v", got.Data)
	}
	evs, _ := cl.Events(context.Background(), "", 200)
	for _, e := range evs {
		if e.Kind == "hook" && e.Data["text"] == "forged" {
			t.Error("the forged delivery was recorded")
		}
	}
	if a := overLink(t, l, "/user/call/GetUser", bearer(token), `{}`); a.Status != http.StatusOK || !strings.Contains(string(a.Body), `"name":"jo"`) {
		t.Errorf("GetUser over the link answered %d %s", a.Status, a.Body)
	}
	if a := overLink(t, l, "/user/call/GiveSecret", bearer(token), `{}`); !strings.Contains(string(a.Body), "a secret never crosses the edge") {
		t.Errorf("GiveSecret without an ask over the link answered %d %s, want it refused", a.Status, a.Body)
	}
	if a := overLink(t, l, "/peers/messages", nil, `{}`); a.Status != http.StatusNotFound {
		t.Errorf("/peers/ over the link answered %d, want 404", a.Status)
	}
	if up := waitEvent(t, cl, "edge_link_up", func(e store.Event) bool { return e.Data["role"] == "yard" }); up.Data["handle"] != "jo" || up.Data["url"] != srv.URL || up.Data["role"] != "yard" {
		t.Errorf("edge_link_up = %+v", up.Data)
	}
	if s, known := edgelink.ReadState(c); !known || !s.Up || s.Handle != "jo" {
		t.Errorf("the link's state = %+v (known %v)", s, known)
	}
}

// The edge setting is read while the server runs: connecting has a server
// with a listener dial at once, the listener serving on beside the link,
// and disconnecting closes the link, told with its reason.
func TestEdgeLinkFollowsTheSettingBesideTheListener(t *testing.T) {
	for _, role := range []string{"", "web"} {
		t.Run("role="+role, func(t *testing.T) {
			edge, srv := stubEdge(t)
			c := home(t)
			ctx := context.Background()
			var cl store.API
			var addr string
			stopEdge := func() {}
			if role == "" {
				addr = freeAddr(t)
				setHookAddr(t, c, addr)
			}
			_, value := edgeYard(t, c, edge, srv, false)
			if role == "" {
				start(t, &runner{c: c})
				client := rpc.NewClient(c.Socket())
				defer client.Close(ctx)
				cl = client
			} else {
				cl = storetest.NewIn(t, c.Home)
				view := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.RemoteAddr != edgelink.RemoteAddr {
						t.Errorf("remote address = %q", r.RemoteAddr)
					}
					if r.URL.Path == "/events" {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, "data: edge_link_up\n\n")
						w.(http.Flusher).Flush()
						<-r.Context().Done()
						return
					}
					io.WriteString(w, "web home")
				})
				tune := linkTest
				linkTest = func(l *edgelink.Link) {
					tune(l)
					l.PingEvery, l.DeadAfter = 20*time.Millisecond, 2*time.Second
				}
				stopEdge = KeepEdge(ctx, c, cl, role, view, slog.New(slog.NewTextHandler(io.Discard, nil)))
				defer stopEdge()
			}
			if _, err := os.Stat(c.EdgeState()); !os.IsNotExist(err) {
				t.Fatalf("a yard with no edge has a state file: %v", err)
			}
			if err := cl.SetSetting(ctx, store.EdgeSetting, value, "jo"); err != nil {
				t.Fatal(err)
			}
			l := nextLink(t, edge)
			key, err := yardkey.Load(c.PeerKey())
			if err != nil {
				t.Fatal(err)
			}
			checkUpgrade := func(l *standin.EdgeLink) {
				t.Helper()
				if got := l.Header.Get("Yardr-Link-Role"); got != role {
					t.Errorf("upgrade role = %q, want %q", got, role)
				}
				if got := l.Header.Get(yardkey.HeaderKey); got != yardkey.Public(key) {
					t.Errorf("upgrade key = %q, want the yard's key", got)
				}
			}
			checkUpgrade(l)
			eventRole := role
			if eventRole == "" {
				eventRole = "yard"
			}
			hasRole := func(e store.Event) bool { return e.Data["role"] == eventRole }
			waitEvent(t, cl, "edge_link_up", hasRole)
			if role == "" {
				if a := overLink(t, l, "/hooks/adhoc", http.Header{"Authorization": {"Bearer " + hookToken}, "Yard-Delivery": {"linked-1"}}, `{"text":"linked"}`); a.Status != http.StatusOK {
					t.Errorf("a delivery over the link answered %d %s", a.Status, a.Body)
				}
				if !listens(addr) {
					t.Errorf("the listener on %s stopped with the link up", addr)
				}
				if s, known := edgelink.ReadState(c); !known || !s.Up {
					t.Errorf("yard state = %+v, known %v", s, known)
				}
			} else {
				request := func(path string) standin.EdgeAnswer {
					t.Helper()
					callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
					defer cancel()
					a, err := l.Deliver(callCtx, l.Frame(http.MethodGet, path, nil, nil, false))
					if err != nil {
						t.Fatal(err)
					}
					return a
				}
				if a := request("/"); a.Status != http.StatusOK || !bytes.Contains(a.Body, []byte("web home")) {
					t.Errorf("web home = %d %.200s", a.Status, a.Body)
				}
				head := request("/events?format=json&after=0")
				if head.Status != http.StatusOK || head.Header.Get("Content-Type") != "text/event-stream" {
					t.Fatalf("web events head = %+v", head)
				}
				var data []byte
				deadline := time.After(10 * time.Second)
			stream:
				for {
					select {
					case d := <-l.DataEvents():
						if d.ID == head.ID {
							data = append(data, d.Body...)
						}
						if bytes.Contains(data, []byte("edge_link_up")) {
							break stream
						}
					case <-deadline:
						t.Fatal("web events did not stream the link event")
					}
				}
				if err := l.SendEnd(ctx, head.ID); err != nil {
					t.Fatal(err)
				}
				deadline = time.After(10 * time.Second)
			ended:
				for {
					select {
					case id := <-l.EndEvents():
						if id == head.ID {
							break ended
						}
					case <-deadline:
						t.Fatal("web event stream did not end")
					}
				}
				l.Silence()
				down := waitEvent(t, cl, "edge_link_down", hasRole)
				if down.Data["reason"] != edgelink.DownPings {
					t.Errorf("silent web link down = %+v", down.Data)
				}
				l = nextLink(t, edge)
				checkUpgrade(l)
				if a := request("/"); a.Status != http.StatusOK {
					t.Errorf("redialled web home = %d", a.Status)
				}
				if _, err := os.Stat(c.EdgeState()); !os.IsNotExist(err) {
					t.Fatalf("web link wrote state: %v", err)
				}
				// A web process must leave the server's state alone, even on disconnect
				// and shutdown. A sentinel makes removing it as visible as writing it.
				if err := os.WriteFile(c.EdgeState(), []byte("yard state"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := cl.SetSetting(ctx, store.EdgeSetting, "", "jo"); err != nil {
				t.Fatal(err)
			}
			down := waitEvent(t, cl, "edge_link_down", func(e store.Event) bool {
				return hasRole(e) && e.Data["reason"] == edgelink.DownStopped
			})
			if down.Data["role"] != eventRole {
				t.Errorf("edge_link_down = %+v", down.Data)
			}
			select {
			case <-l.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the link stayed open at the edge after disconnecting")
			}
			if role == "" {
				if _, known := edgelink.ReadState(c); known {
					t.Error("a disconnected yard still says how its link is")
				}
			} else {
				stopEdge()
				if got, err := os.ReadFile(c.EdgeState()); err != nil || string(got) != "yard state" {
					t.Errorf("web disconnect/shutdown changed yard state: %q, %v", got, err)
				}
			}
		})
	}
	t.Run("web across a server replace", func(t *testing.T) {
		edge, srv := stubEdge(t)
		c := home(t)
		ctx := context.Background()
		_, value := edgeYard(t, c, edge, srv, false)
		real := storetest.NewIn(t, c.Home)
		head, err := real.Head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		attempts := make(chan uint64, 4)
		away := &edgeAwayStore{
			API:      real,
			script:   []string{"ends", "away", "away"},
			end:      make(chan struct{}),
			attempts: attempts,
		}
		view := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "web home")
		})
		stopEdge := KeepEdge(ctx, c, away, "web", view, slog.New(slog.NewTextHandler(io.Discard, nil)))
		defer stopEdge()
		nextAttempt := func() uint64 {
			t.Helper()
			select {
			case after := <-attempts:
				return after
			case <-time.After(10 * time.Second):
				t.Fatal("the web view did not retry its event subscription")
				return 0
			}
		}
		if got := nextAttempt(); got != head {
			t.Fatalf("initial subscription after = %d, want head %d", got, head)
		}
		// The server replaces itself: the stream ends, and the edge is
		// connected while it does not answer.
		close(away.end)
		if got := nextAttempt(); got != head {
			t.Fatalf("first retry after = %d, want last sequence %d", got, head)
		}
		if err := real.SetSetting(ctx, store.EdgeSetting, value, "jo"); err != nil {
			t.Fatal(err)
		}
		for retry := 2; retry <= 3; retry++ {
			if got := nextAttempt(); got != head {
				t.Fatalf("retry %d after = %d, want last sequence %d", retry, got, head)
			}
		}
		l := nextLink(t, edge)
		if got := l.Header.Get("Yardr-Link-Role"); got != "web" {
			t.Errorf("upgrade role = %q, want web", got)
		}
		key, err := yardkey.Load(c.PeerKey())
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Header.Get(yardkey.HeaderKey); got != yardkey.Public(key) {
			t.Errorf("upgrade key = %q, want the yard's key", got)
		}
		waitEvent(t, real, "edge_link_up", func(e store.Event) bool { return e.Data["role"] == "web" })
	})
}
