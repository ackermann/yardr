package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
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
	select {
	case l := <-edge.Links():
		return l
	case <-time.After(10 * time.Second):
		t.Fatal("the server's link never came up at the edge")
		return nil
	}
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
func waitEvent(t *testing.T, cl *rpc.Client, kind string, ok func(store.Event) bool) store.Event {
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
// GiveSecret refused, since secrets never cross the edge; /peers/ is not
// served through the edge. The link is told as an event and in its state,
// and nothing listens on a port.
func TestEdgeLinkServesTheListenersHandlersWithNoAddress(t *testing.T) {
	edge, srv := stubEdge(t)
	c := home(t)
	token, _ := edgeYard(t, c, edge, srv, true)
	start(t, &runner{c: c})
	l := nextLink(t, edge)
	cl := rpc.NewClient(c.Socket())
	defer cl.Close(context.Background())

	bearer := func(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }
	if a := overLink(t, l, "/hooks/adhoc", bearer(hookToken), `{"text":"deployed"}`); a.Status != http.StatusOK {
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
		t.Errorf("GiveSecret over the link answered %d %s, want it refused as never crossing the edge", a.Status, a.Body)
	}
	if a := overLink(t, l, "/peers/messages", nil, `{}`); a.Status != http.StatusNotFound {
		t.Errorf("/peers/ over the link answered %d, want 404", a.Status)
	}
	if up := waitEvent(t, cl, "edge_link_up", nil); up.Data["handle"] != "jo" || up.Data["url"] != srv.URL {
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
	edge, srv := stubEdge(t)
	c := home(t)
	addr := freeAddr(t)
	setHookAddr(t, c, addr)
	_, value := edgeYard(t, c, edge, srv, false)
	start(t, &runner{c: c})
	cl := rpc.NewClient(c.Socket())
	defer cl.Close(context.Background())
	if _, known := edgelink.ReadState(c); known {
		t.Error("a yard with no edge says how a link is")
	}
	ctx := context.Background()
	if err := cl.SetSetting(ctx, store.EdgeSetting, value, "jo"); err != nil {
		t.Fatal(err)
	}
	l := nextLink(t, edge)
	if a := overLink(t, l, "/hooks/adhoc", http.Header{"Authorization": {"Bearer " + hookToken}}, `{"text":"linked"}`); a.Status != http.StatusOK {
		t.Errorf("a delivery over the link answered %d %s", a.Status, a.Body)
	}
	if !listens(addr) {
		t.Errorf("the listener on %s stopped with the link up", addr)
	}
	if err := cl.SetSetting(ctx, store.EdgeSetting, "", "jo"); err != nil {
		t.Fatal(err)
	}
	down := waitEvent(t, cl, "edge_link_down", nil)
	if down.Data["reason"] != edgelink.DownStopped {
		t.Errorf("edge_link_down = %+v", down.Data)
	}
	select {
	case <-l.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the link stayed open at the edge after disconnecting")
	}
	if _, known := edgelink.ReadState(c); known {
		t.Error("a disconnected yard still says how its link is")
	}
}
