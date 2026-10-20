package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// roundTripFunc is the transport of a test's client: what it does with a
// request is the test's.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// edgeYard is a yard (operator jo) beside a stand-in edge it reaches over
// TLS: the client that trusts the edge's certificate is srv.Client(), and
// no other does.
func edgeYard(t *testing.T) (store.API, *standin.Edge, *httptest.Server) {
	t.Helper()
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(edge)
	t.Cleanup(srv.Close)
	return userYard(t), edge, srv
}

// edgeAs runs yardr as actor on st, with edge calls made by hc and asked
// again at once: what it prints, what it says on its error stream, and its
// error.
func edgeAs(t *testing.T, st store.API, hc *http.Client, actor string, args ...string) (string, string, error) {
	t.Helper()
	a := appOn(st)
	a.edgeHTTP, a.edgePoll = hc, time.Millisecond
	var errs bytes.Buffer
	a.stderr = &errs
	out, err := run(t, a.rootCmd(), append(args, "--actor", actor)...)
	return out, errs.String(), err
}

func mustEdge(t *testing.T, st store.API, hc *http.Client, args ...string) (string, string) {
	t.Helper()
	out, errs, err := edgeAs(t, st, hc, "jo", args...)
	if err != nil {
		t.Fatalf("yardr %s: %v\n%s%s", strings.Join(args, " "), err, out, errs)
	}
	return out, errs
}

// yardKeyOf is the public key of the yard st's runs are on, and whether it
// has one.
func yardKeyOf(t *testing.T, st store.API) (string, bool) {
	t.Helper()
	key, err := yardkey.Load(config.At(yardEnv(t, st)["YARDR_HOME"]).PeerKey())
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	} else if err != nil {
		t.Fatal(err)
	}
	return yardkey.Public(key), true
}

func settingsOf(t *testing.T, st store.API) map[string]string {
	t.Helper()
	all, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func edgeSetting(t *testing.T, st store.API) store.Edge {
	t.Helper()
	e, connected, err := store.EdgeOf(settingsOf(t, st))
	if err != nil || !connected {
		t.Fatalf("the edge setting = %q: connected %v, %v", settingsOf(t, st)[store.EdgeSetting], connected, err)
	}
	return e
}

func edgeLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, want := range lines {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("no line %q in:\n%s", want, out)
		}
	}
}

// yardr edge connect fetches the edge's key, prints it with the yard's own,
// enrols the yard by a code, and once the person has approved it writes the
// edge setting with the key it fetched pinned, and hook_public_url to the
// address the edge takes the yard's webhooks at.
func TestEdgeConnect(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)

	out, errs := mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	yard, ok := yardKeyOf(t, st)
	if !ok {
		t.Fatal("connecting made no key for the yard")
	}
	host := strings.TrimPrefix(srv.URL, "https://")
	edgeLines(t, out,
		"The edge's key: ed25519:"+edge.PublicKey()+"  (fetched over HTTPS from "+host+")",
		"This yard's key: ed25519:"+yard,
		"Open "+srv.URL+"/connect and enter the code  WQ4F-7K2M  (10 minutes)",
		"… approved by chris@mckean.co as mckean",
		"Connected: webhooks reach this yard at "+srv.URL+"/hooks/mckean/<hook>",
		"hook_public_url set to "+srv.URL+"/hooks/mckean")
	if errs != "" || strings.Contains(out, "was ") {
		t.Errorf("a connect that replaced nothing says more than that:\n%s\nstderr: %s", out, errs)
	}

	got := edgeSetting(t, st)
	if want := (store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean"}); got != want {
		t.Errorf("the edge setting = %+v, want %+v", got, want)
	}
	if hook := settingsOf(t, st)[store.HookPublicURLSetting]; hook != srv.URL+"/hooks/mckean" {
		t.Errorf("hook_public_url = %q, want the edge's address for this yard", hook)
	}
	// The edge was asked as this yard, by the name it goes by, signed.
	state := edge.State()
	if len(state.Enrols) < 2 || state.Enrols[0].Key != yard || state.Enrols[0].Name != "y" {
		t.Errorf("the edge was asked %+v, want this yard's key and name", state.Enrols)
	}
	if state.KeyFetches != 1 {
		t.Errorf("the edge's key was fetched %d times, want once", state.KeyFetches)
	}
}

// A hook_public_url the operator set by hand is kept inside the edge
// setting, said when connecting, and put back by disconnect, which then
// also asks the edge to drop its row for the yard.
func TestEdgeDisconnectPutsBackAHookPublicURLSetByHand(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://hooks.example.net/hooks")

	out, _ := mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	edgeLines(t, out,
		"hook_public_url set to "+srv.URL+"/hooks/mckean",
		"hook_public_url was https://hooks.example.net/hooks; restored on disconnect")
	if got := edgeSetting(t, st).HookPublicURL; got != "https://hooks.example.net/hooks" {
		t.Errorf("the edge setting keeps %q of the hook_public_url it replaced", got)
	}

	out, errs := mustEdge(t, st, srv.Client(), "edge", "disconnect")
	edgeLines(t, out,
		srv.URL+" was asked to drop its row for this yard",
		"hook_public_url put back to https://hooks.example.net/hooks",
		"disconnected from "+srv.URL)
	if errs != "" {
		t.Errorf("a disconnect the edge took says on stderr: %s", errs)
	}
	all := settingsOf(t, st)
	if all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "https://hooks.example.net/hooks" {
		t.Errorf("after disconnect: edge %q, hook_public_url %q; want the edge cleared and the hand-set address back",
			all[store.EdgeSetting], all[store.HookPublicURLSetting])
	}
	yard, _ := yardKeyOf(t, st)
	if drops := edge.State().Drops; len(drops) != 1 || drops[0] != yard {
		t.Errorf("the edge was asked to drop %v, want this yard's key", drops)
	}
}

// Where hook_public_url was not set before connecting, disconnect clears it
// rather than leave an address of an edge the yard has left.
func TestEdgeDisconnectClearsAHookPublicURLThatWasNotSet(t *testing.T) {
	t.Parallel()
	st, _, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	if got := edgeSetting(t, st).HookPublicURL; got != "" {
		t.Fatalf("connecting kept %q as the hook_public_url it replaced, and replaced none", got)
	}
	out, _ := mustEdge(t, st, srv.Client(), "edge", "disconnect")
	edgeLines(t, out, "hook_public_url cleared: it was not set before connecting")
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("after disconnect: edge %q, hook_public_url %q; want both unset", all[store.EdgeSetting], all[store.HookPublicURLSetting])
	}
	if out, _ := mustEdge(t, st, srv.Client(), "edge", "disconnect"); out != "this yard is not connected to an edge\n" {
		t.Errorf("a second disconnect printed %q", out)
	}
}

// The edge drops the yard's row when it disconnects, so connecting again
// enrols it afresh: a code to enter again, and the settings written again.
func TestEdgeConnectAgainAfterDisconnect(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	mustEdge(t, st, srv.Client(), "edge", "disconnect")
	if got := settingsOf(t, st)[store.EdgeSetting]; got != "" {
		t.Fatalf("the edge setting survived a disconnect: %q", got)
	}

	out, _ := mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	edgeLines(t, out,
		"Open "+srv.URL+"/connect and enter the code  WQ4F-7K2M  (10 minutes)",
		"… approved by chris@mckean.co as mckean")
	want := store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean"}
	if got := edgeSetting(t, st); got != want {
		t.Errorf("the edge setting = %+v, want %+v", got, want)
	}
	if got := settingsOf(t, st)[store.HookPublicURLSetting]; got != srv.URL+"/hooks/mckean" {
		t.Errorf("hook_public_url = %q, want the edge's address for this yard", got)
	}
}

// A hook_public_url changed after connecting is the operator's later word,
// and disconnect leaves it.
func TestEdgeDisconnectLeavesAHookPublicURLChangedSinceConnecting(t *testing.T) {
	t.Parallel()
	st, _, srv := edgeYard(t)
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://old.example.net/hooks")
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://later.example.net/hooks")

	out, _ := mustEdge(t, st, srv.Client(), "edge", "disconnect")
	edgeLines(t, out, "hook_public_url is https://later.example.net/hooks, which is not the address connecting set: left as it is")
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "https://later.example.net/hooks" {
		t.Errorf("after disconnect: edge %q, hook_public_url %q", all[store.EdgeSetting], all[store.HookPublicURLSetting])
	}
}

// The yard is disconnected here whether or not the edge can be told: a
// yard that cannot reach the edge, or that the edge does not know, would
// otherwise stay connected for good. The command says the edge was not
// told.
func TestEdgeDisconnectWhenTheEdgeCannotBeTold(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)

	edge.Behave(func(b *standin.EdgeBehaviour) { b.DisconnectStatus = http.StatusForbidden })
	out, errs := mustEdge(t, st, srv.Client(), "edge", "disconnect")
	if !strings.Contains(errs, srv.URL+" was not told: the edge refused this yard (403). Its row for this yard stays until its operator drops it.") {
		t.Errorf("the edge's refusal is not said on stderr:\n%s", errs)
	}
	edgeLines(t, out, "disconnected from "+srv.URL)
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("after a disconnect the edge refused: edge %q, hook_public_url %q; want both cleared", all[store.EdgeSetting], all[store.HookPublicURLSetting])
	}

	// An edge that is gone altogether.
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	srv.Close()
	out, errs, err := edgeAs(t, st, srv.Client(), "jo", "edge", "disconnect", "--json")
	if err != nil {
		t.Fatalf("disconnect with the edge gone: %v", err)
	}
	var left edgeLeft
	if err := json.Unmarshal([]byte(out), &left); err != nil || left.Connected || left.EdgeTold || left.EdgeError == "" || left.Was == nil || left.Was.Handle != "mckean" {
		t.Errorf("disconnect --json = %+v, %v\n%s", left, err, out)
	}
	if errs != "" {
		t.Errorf("--json says on stderr: %s", errs)
	}
	if got := settingsOf(t, st)[store.EdgeSetting]; got != "" {
		t.Errorf("the edge setting survived a disconnect: %q", got)
	}
}

// Plain http is refused except to this machine, before the key is fetched,
// and a path in the address is no edge's: nothing is asked, written or made.
func TestEdgeConnectRefusesAnAddressItWillNotFetchAKeyFrom(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	nothing := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a request was made to %s", r.URL)
		return nil, errors.New("no network in this test")
	})}
	for url, want := range map[string]string{
		"http://my.yardr.dev":            "http is for a stand-in on this machine only",
		"http://192.0.2.7:8790":          "http is for a stand-in on this machine only",
		"ftp://my.yardr.dev":             "ftp:// is not https://",
		"https://my.yardr.dev/edge":      "the edge's origin alone",
		"https://user:pw@my.yardr.dev":   "no user or password",
		"https://my.yardr.dev?x=1":       "no query or fragment",
		"my.yardr.dev":                   "a public address",
		"https://my.yardr.dev/../hooks/": "its path has no '..' segment",
	} {
		_, _, err := edgeAs(t, st, nothing, "jo", "edge", "connect", url)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("edge connect %s = %v, want %q", url, err, want)
		}
	}
	// The same address in plain http is taken where it is this machine's: a
	// stand-in served in the clear.
	plain := httptest.NewServer(edge)
	t.Cleanup(plain.Close)
	mustEdge(t, st, srv.Client(), "edge", "connect", plain.URL)
	if got := edgeSetting(t, st).URL; got != plain.URL {
		t.Errorf("connected to %q, want the loopback stand-in %q", got, plain.URL)
	}
}

// A certificate this machine does not trust is no edge's: the key is not
// fetched, nothing is signed (the yard has not even made its key), and
// nothing is written.
func TestEdgeConnectRefusesACertificateItDoesNotTrust(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	// A nil client is the system's roots, which hold no stand-in's.
	_, _, err := edgeAs(t, st, nil, "jo", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "is not trusted by this machine") || !strings.Contains(err.Error(), "nothing was signed") {
		t.Fatalf("edge connect to a certificate nobody vouches for = %v", err)
	}
	if _, made := yardKeyOf(t, st); made {
		t.Error("the yard made its key before it trusted the edge's")
	}
	if n := len(edge.State().Enrols); n != 0 {
		t.Errorf("the edge took %d enrolling calls", n)
	}
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("settings written by a refused connect: %v", all)
	}
}

// An enrolment answered by a key other than the one fetched is refused, and
// nothing is written.
func TestEdgeConnectRefusesAnAnswerSignedByAnotherKey(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer = other })
	_, _, err = edgeAs(t, st, srv.Client(), "jo", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "not signed with the key this yard pinned") || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("edge connect, answered by another key = %v", err)
	}
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("settings written by a refused connect: %v", all)
	}

	// Rightly signed to start with and wrongly once the person has approved:
	// the answer that would have carried the handle is the one refused.
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer = b.Served; b.ApproveAfter = 5 })
	hc := srv.Client()
	base, calls := hc.Transport, 0
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/edge/enrol") {
			if calls++; calls == 2 {
				edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer = other })
			}
		}
		return base.RoundTrip(r)
	})
	if _, _, err = edgeAs(t, st, hc, "jo", "edge", "connect", srv.URL); err == nil || !strings.Contains(err.Error(), "not signed with the key this yard pinned") {
		t.Fatalf("edge connect, answered by another key from the second call on = %v", err)
	}
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("settings written by a refused connect: %v", all)
	}
}

// The pin is the key that was fetched before anything was signed. An edge
// that serves another key at /edge/key once the yard is enrolled moves
// nothing: the key was not fetched again, and the setting holds the first.
func TestEdgeConnectPinsTheKeyItFetchedFirst(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	_, later, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.ServedAfterApproval = later })
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)

	if got := edgeSetting(t, st).Key; got != edge.PublicKey() {
		t.Errorf("the pinned key = %q, want the first the edge served, %q", got, edge.PublicKey())
	}
	if n := edge.State().KeyFetches; n != 1 {
		t.Errorf("the edge's key was fetched %d times, want once: before anything was signed", n)
	}
	// What the edge serves now is another key, which is not taken: the yard
	// is connected, and connecting again is refused, so that the pin only
	// moves with a new connection.
	_, _, err = edgeAs(t, st, srv.Client(), "jo", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "this yard is already connected to "+srv.URL+" as mckean: run yardr edge disconnect first") {
		t.Errorf("edge connect to a yard that is connected = %v", err)
	}
	if got := edgeSetting(t, st).Key; got != edge.PublicKey() {
		t.Errorf("the pinned key = %q after a refused connect, want %q", got, edge.PublicKey())
	}
	if n := edge.State().KeyFetches; n != 1 {
		t.Errorf("a refused connect asked the edge for its key: %d fetches", n)
	}
}

// The edge refusing the yard, or answering what the setting will not keep,
// ends the connect with nothing written; and an edge that knows the yard
// already answers its handle with no code to enter.
func TestEdgeConnectWhenTheEdgeRefusesOrKnowsTheYard(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)

	edge.Behave(func(b *standin.EdgeBehaviour) { b.Refuse = http.StatusGone })
	_, _, err := edgeAs(t, st, srv.Client(), "jo", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "the edge refused this yard: expired (410): nothing was written") {
		t.Errorf("edge connect, refused = %v", err)
	}

	// A handle that is more than one segment of a path would point the hook
	// address somewhere else.
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Refuse, b.Handle, b.ApproveAfter = 0, "../x", 0 })
	_, _, err = edgeAs(t, st, srv.Client(), "jo", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "what it answered cannot be kept") || !strings.Contains(err.Error(), "nothing was written") {
		t.Errorf("edge connect, answered with a handle that is a path = %v", err)
	}
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("settings written by a refused connect: %v", all)
	}

	edge.Behave(func(b *standin.EdgeBehaviour) { b.Handle = "mckean" })
	out, _ := mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	if strings.Contains(out, "enter the code") {
		t.Errorf("a yard the edge knows is asked for a code:\n%s", out)
	}
	edgeLines(t, out, "… approved by chris@mckean.co as mckean")
}

// Only the operator connects a yard or disconnects it, and before the edge
// is asked anything: its row is made when the person approves, and a yard
// that may not write the setting would be left with one it cannot use.
func TestEdgeIsTheOperatorsOnly(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	_, _, err := edgeAs(t, st, srv.Client(), "yardmaster", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may connect this yard to an edge; this is yardmaster") {
		t.Errorf("edge connect as the yardmaster = %v", err)
	}
	if state := edge.State(); state.KeyFetches != 0 || len(state.Enrols) != 0 {
		t.Errorf("the edge was asked %+v by a refused connect", state)
	}

	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	_, _, err = edgeAs(t, st, srv.Client(), "yardmaster", "edge", "disconnect")
	if err == nil || !strings.Contains(err.Error(), "only the operator (jo) may disconnect this yard from the edge; this is yardmaster") {
		t.Errorf("edge disconnect as the yardmaster = %v", err)
	}
	if len(edge.State().Drops) != 0 || edgeSetting(t, st).Handle != "mckean" {
		t.Error("a refused disconnect changed the yard or the edge")
	}
	// Reading is anyone's.
	if out, _, err := edgeAs(t, st, srv.Client(), "yardmaster", "edge", "status"); err != nil || !strings.Contains(out, "as handle mckean") {
		t.Errorf("edge status as the yardmaster = %q, %v", out, err)
	}
}

// With --json a connect prints two documents: the keys and the code at
// once, so that whoever runs it can show the code, and the result at the
// end. What it says to a person goes to stderr.
func TestEdgeConnectJSON(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	out, errs := mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL, "--json")
	dec := json.NewDecoder(strings.NewReader(out))
	var first map[string]any
	var result edgeView
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("first document: %v\n%s", err, out)
	}
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("second document: %v\n%s", err, out)
	}
	if dec.More() {
		t.Errorf("more than two documents:\n%s", out)
	}
	yard, _ := yardKeyOf(t, st)
	for key, want := range map[string]string{"edge": srv.URL, "edge_key": edge.PublicKey(), "yard_key": yard, "name": "y", "code": "WQ4F-7K2M", "connect_url": srv.URL + "/connect"} {
		if first[key] != want {
			t.Errorf("first document %s = %v, want %q", key, first[key], want)
		}
	}
	if first["expires"] == nil {
		t.Errorf("the first document has no expiry: %v", first)
	}
	want := edgeView{Connected: true, URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean", HooksURL: srv.URL + "/hooks/mckean",
		HookPublicURL: srv.URL + "/hooks/mckean", ApprovedBy: "chris@mckean.co"}
	if result != want {
		t.Errorf("result = %+v, want %+v", result, want)
	}
	edgeLines(t, errs, "The edge's key: ed25519:"+edge.PublicKey()+"  (fetched over HTTPS from "+strings.TrimPrefix(srv.URL, "https://")+")",
		"Open "+srv.URL+"/connect and enter the code  WQ4F-7K2M  (10 minutes)")
}

// Ctrl+C while the person has not approved the yard ends the wait with
// nothing written: the code stays good at the edge until it expires, and the
// yard is as it was.
func TestEdgeConnectStoppedBeforeTheApproval(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.ApproveAfter = 1 << 20 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc := srv.Client()
	base, asked := hc.Transport, 0
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/edge/enrol") {
			if asked++; asked == 3 {
				cancel()
			}
		}
		return base.RoundTrip(r)
	})
	a := appOn(st)
	a.edgeHTTP, a.edgePoll = hc, time.Millisecond
	root := a.rootCmd()
	root.SetArgs([]string{"edge", "connect", srv.URL, "--actor", "jo"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "stopped before the yard was approved: nothing was written") {
		t.Fatalf("edge connect, stopped = %v", err)
	}
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "" {
		t.Errorf("settings written by a connect that was stopped: %v", all)
	}
}

// edge status reads what connect wrote and asks the edge nothing.
func TestEdgeStatus(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	nothing := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("edge status made a request to %s", r.URL)
		return nil, errors.New("no network in this test")
	})}

	out, _ := mustEdge(t, st, nothing, "edge", "status")
	edgeLines(t, out, "not connected to an edge: yardr edge connect <url>, such as https://my.yardr.dev")
	out, _ = mustEdge(t, st, nothing, "edge", "status", "--json")
	var view edgeView
	if err := json.Unmarshal([]byte(out), &view); err != nil || view != (edgeView{}) {
		t.Errorf("status --json when not connected = %+v, %v", view, err)
	}

	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://hooks.example.net/hooks")
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	out, _ = mustEdge(t, st, nothing, "edge", "status")
	edgeLines(t, out,
		"edge      "+srv.URL+"  (key ed25519:"+edge.PublicKey()+", as handle mckean)",
		"hooks     "+srv.URL+"/hooks/mckean/<hook>",
		"          hook_public_url was https://hooks.example.net/hooks before connecting; yardr edge disconnect puts it back")
	if strings.Contains(out, "note ") {
		t.Errorf("status of a connected yard whose hook_public_url is the edge's has a note:\n%s", out)
	}
	out, _ = mustEdge(t, st, nothing, "edge", "status", "--json")
	want := edgeView{Connected: true, URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean", HooksURL: srv.URL + "/hooks/mckean",
		HookPublicURL: srv.URL + "/hooks/mckean", Replaced: "https://hooks.example.net/hooks"}
	if err := json.Unmarshal([]byte(out), &view); err != nil || view != want {
		t.Errorf("status --json = %+v, %v; want %+v", view, err, want)
	}

	// A hook_public_url that was changed after connecting is told: hooks
	// registered from it do not reach the edge.
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://later.example.net/hooks")
	out, _ = mustEdge(t, st, nothing, "edge", "status")
	edgeLines(t, out, "note      hook_public_url is https://later.example.net/hooks, not the edge's address: hooks registered from it do not reach the edge")
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "")
	out, _ = mustEdge(t, st, nothing, "edge", "status")
	edgeLines(t, out, "note      hook_public_url is unset, not the edge's address: hooks registered from it do not reach the edge")
}

// settingsOverride is a store whose settings carry more than it was given,
// as an older build's store may.
type settingsOverride struct {
	store.API
	extra map[string]string
}

func (s *settingsOverride) Settings(ctx context.Context) (map[string]string, error) {
	all, err := s.API.Settings(ctx)
	for k, v := range s.extra {
		all[k] = v
	}
	return all, err
}

// An edge setting this build cannot read is no connection: connect and
// status say so and how it is cleared, and disconnect clears it, leaving
// hook_public_url as it is, since the address it was to go back to is in the
// value it cannot read.
func TestEdgeSettingThatCannotBeRead(t *testing.T) {
	t.Parallel()
	st, _, srv := edgeYard(t)
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://hooks.example.net/hooks")
	odd := &settingsOverride{API: st, extra: map[string]string{store.EdgeSetting: `{"url":"https://my.yardr.dev","key":"nope"}`}}
	rememberEnv(t, odd, yardEnv(t, st))

	for _, args := range [][]string{{"edge", "status"}, {"edge", "connect", srv.URL}} {
		_, _, err := edgeAs(t, odd, srv.Client(), "jo", args...)
		if err == nil || !strings.Contains(err.Error(), "edge: the edge's key:") || !strings.Contains(err.Error(), "yardr edge disconnect clears it") {
			t.Errorf("yardr %s = %v, want the setting refused with how it is cleared", strings.Join(args, " "), err)
		}
	}
	out, errs := mustEdge(t, odd, srv.Client(), "edge", "disconnect")
	edgeLines(t, out, "hook_public_url is https://hooks.example.net/hooks, which is not the address connecting set: left as it is", "disconnected from the edge")
	if !strings.Contains(errs, "the edge was not told: the edge setting cannot be read") {
		t.Errorf("disconnect of a setting it cannot read says on stderr: %q", errs)
	}
	if got := settingsOf(t, st)[store.HookPublicURLSetting]; got != "https://hooks.example.net/hooks" {
		t.Errorf("hook_public_url = %q, want it left", got)
	}
}

// failingSetting is a store that cannot write one setting.
type failingSetting struct {
	store.API
	key string
}

func (f *failingSetting) SetSetting(ctx context.Context, key, value, actor string) error {
	if key == f.key && value != "" {
		return errors.New("disk full")
	}
	return f.API.SetSetting(ctx, key, value, actor)
}

// The edge setting is written first, and holds the hook_public_url the second
// write replaces: a connect that stops between the two has lost nothing, and
// says what is written and what is not.
func TestEdgeConnectWhenHookPublicURLCannotBeWritten(t *testing.T) {
	t.Parallel()
	st, _, srv := edgeYard(t)
	mustAs(t, st, "jo", "settings", "set", store.HookPublicURLSetting, "https://hooks.example.net/hooks")
	broken := &failingSetting{API: st, key: store.HookPublicURLSetting}
	rememberEnv(t, broken, yardEnv(t, st))

	_, _, err := edgeAs(t, broken, srv.Client(), "jo", "edge", "connect", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "the edge setting is written, and hook_public_url is not (disk full)") {
		t.Fatalf("edge connect, with hook_public_url unwritable = %v", err)
	}
	if got := edgeSetting(t, st); got.HookPublicURL != "https://hooks.example.net/hooks" {
		t.Errorf("the edge setting keeps %q of the hook_public_url, want the one still set", got.HookPublicURL)
	}
	if got := settingsOf(t, st)[store.HookPublicURLSetting]; got != "https://hooks.example.net/hooks" {
		t.Errorf("hook_public_url = %q, want it as it was", got)
	}
	// Disconnect undoes what was written, and leaves the address that was
	// never replaced.
	out, _ := mustEdge(t, st, srv.Client(), "edge", "disconnect")
	edgeLines(t, out, "hook_public_url is https://hooks.example.net/hooks, which is not the address connecting set: left as it is")
	if all := settingsOf(t, st); all[store.EdgeSetting] != "" || all[store.HookPublicURLSetting] != "https://hooks.example.net/hooks" {
		t.Errorf("after disconnect: edge %q, hook_public_url %q", all[store.EdgeSetting], all[store.HookPublicURLSetting])
	}
}

// The edge setting is the operator's own: a run ask cannot propose it, and
// a value that connecting would not have written is refused wherever it is
// set.
func TestEdgeSettingIsCheckedAndAlone(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	key := yardkey.Public(func() ed25519.PrivateKey { _, k, _ := ed25519.GenerateKey(rand.Reader); return k }())
	good := store.Edge{URL: "https://my.yardr.dev", Key: key, Handle: "mckean"}
	mustAs(t, st, "jo", "settings", "set", store.EdgeSetting, good.String())
	if got := edgeSetting(t, st); got != good {
		t.Errorf("edge setting = %+v, want %+v", got, good)
	}
	for value, want := range map[string]string{
		`not json`: "not what yardr edge connect writes",
		(store.Edge{URL: "http://my.yardr.dev", Key: key, Handle: "mckean"}).String():                                             "http is for a stand-in on this machine only",
		(store.Edge{URL: "https://my.yardr.dev/edge", Key: key, Handle: "mckean"}).String():                                       "the edge's origin alone",
		(store.Edge{URL: "https://my.yardr.dev", Key: "short", Handle: "mckean"}).String():                                        "the edge's key",
		(store.Edge{URL: "https://my.yardr.dev", Key: key, Handle: "Mc Kean"}).String():                                           "lower case letters, digits and '-'",
		(store.Edge{URL: "https://my.yardr.dev", Key: key, Handle: "mckean", HookPublicURL: "http://hooks.example.net"}).String(): "the hook_public_url to put back",
	} {
		refusedAs(t, st, "jo", want, "settings", "set", store.EdgeSetting, value)
	}
	refusedAs(t, st, "yardmaster", "only the operator (jo) may change a setting; this is yardmaster", "settings", "set", store.EdgeSetting, good.String())
}
