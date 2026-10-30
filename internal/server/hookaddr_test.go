package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// freeAddr is a loopback address nothing listens on now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// setHookAddr gives the yard in c the hook_addr setting before its server
// starts, as an earlier server's yardr settings set left it.
func setHookAddr(t *testing.T, c *config.Config, addr string) {
	t.Helper()
	st, err := store.Open(c.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	if err := st.SetSetting(context.Background(), hooks.FromSetting, addr, "me"); err != nil {
		t.Fatal(err)
	}
}

func listens(addr string) bool {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// The server takes webhooks on the yard's hook_addr setting when the
// variable is not set where it starts, and says so in its state; with no
// address at all it runs no receiver and says that.
func TestHookAddrFromTheSetting(t *testing.T) {
	c := home(t)
	addr := freeAddr(t)
	setHookAddr(t, c, addr)
	start(t, &runner{c: c})
	l, known := hooks.ReadListener(c)
	if !known || !l.Listening() || l.Addr != addr || l.From != hooks.FromSetting {
		t.Fatalf("listener = %+v (known %v)", l, known)
	}
	if !listens(addr) {
		t.Fatalf("nothing listens on %s", addr)
	}

	none := home(t)
	start(t, &runner{c: none})
	if l, known := hooks.ReadListener(none); !known || l.Addr != "" || l.From != "" || l.Listening() {
		t.Fatalf("listener without an address = %+v (known %v)", l, known)
	}
}

// YARDR_HOOK_ADDR, set where the server starts, wins over the setting.
func TestHookAddrVariableWins(t *testing.T) {
	c := home(t)
	setting, variable := freeAddr(t), freeAddr(t)
	setHookAddr(t, c, setting)
	c.HookAddr = variable
	start(t, &runner{c: c})
	l, known := hooks.ReadListener(c)
	if !known || !l.Listening() || l.Addr != variable || l.From != hooks.FromVariable {
		t.Fatalf("listener = %+v (known %v)", l, known)
	}
	if !listens(variable) || listens(setting) {
		t.Fatalf("variable %s listens: %v; setting %s listens: %v", variable, listens(variable), setting, listens(setting))
	}
}

// Two yards given one address: the second server cannot listen, logs an
// event and says why in its state, and serves its yard all the same. The
// first keeps the address. A server that has stopped leaves no state.
func TestHookAddrTaken(t *testing.T) {
	first, second := home(t), home(t)
	addr := freeAddr(t)
	setHookAddr(t, first, addr)
	setHookAddr(t, second, addr)
	start(t, &runner{c: first})
	_, stop := startStoppable(t, &runner{c: second})

	if l, _ := hooks.ReadListener(first); !l.Listening() {
		t.Fatalf("first = %+v", l)
	}
	l, known := hooks.ReadListener(second)
	if !known || l.Listening() || l.Addr != addr || l.From != hooks.FromSetting || !strings.Contains(l.Error, "address already in use") {
		t.Fatalf("second = %+v (known %v)", l, known)
	}
	ctx := context.Background()
	cl := rpc.NewClient(second.Socket())
	evs, err := cl.Events(ctx, "", 50)
	if err != nil {
		t.Fatalf("the second server does not serve its yard: %v", err)
	}
	var failed *store.Event
	for i, e := range evs {
		if e.Kind == "hook_listener_failed" {
			failed = &evs[i]
		}
	}
	if failed == nil || failed.Data["addr"] != addr || failed.Data["from"] != hooks.FromSetting ||
		!strings.Contains(failed.Data["error"].(string), "address already in use") {
		t.Fatalf("events = %+v", evs)
	}
	if _, text := hooks.Listens(l, known, addr); !strings.Contains(text, "could not listen on "+addr) ||
		!strings.Contains(text, "yardr settings set hook_addr <host:port>") {
		t.Fatalf("text = %s", text)
	}

	stop()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, known := hooks.ReadListener(second); !known {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stopped server's state is still read as a running one's")
		}
	}
}

// The listener that takes webhooks takes the yard's peers too, under
// /peers/, beside /hooks/: a peer's signed message is taken there, and a
// yard that is no peer is refused.
func TestHookListenerTakesPeers(t *testing.T) {
	c := home(t)
	addr := freeAddr(t)
	setHookAddr(t, c, addr)
	start(t, &runner{c: c})
	ctx := context.Background()
	cl := rpc.NewClient(c.Socket())
	defer cl.Close(ctx)

	if _, _, err := yardkey.Ensure(c.PeerKey()); err != nil {
		t.Fatal(err)
	}
	own, err := store.Load(c.PeerKey())
	if err != nil {
		t.Fatal(err)
	}
	north, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	stranger, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "stranger.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.AddPeer(ctx, store.Peer{Name: "north", Key: store.Public(north)}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.AllowPeer(ctx, "north", store.PeerAllow{Receive: []string{"ping"}}, "me"); err != nil {
		t.Fatal(err)
	}
	me := store.Peer{Name: "yard", Key: store.Public(own), URL: "http://" + addr}
	m := store.PeerMessage{ID: "m1", Kind: "ping", Body: []byte(`{}`)}
	if err := (&yards.Caller{Key: north}).Push(ctx, me, m); err != nil {
		t.Fatalf("north's message: %v", err)
	}
	if err := (&yards.Caller{Key: stranger}).Push(ctx, me, m); err == nil || !strings.Contains(err.Error(), "answered 401") {
		t.Fatalf("a stranger's message: %v", err)
	}
	evs, err := cl.Events(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	received := 0
	for _, e := range evs {
		if e.Kind == "peer_message_received" && e.Data["peer"] == "north" && e.Data["id"] == "m1" {
			received++
		}
	}
	if received != 1 {
		t.Fatalf("events = %+v", evs)
	}
	// The hooks are still served beside it, and nothing else is.
	for path, want := range map[string]int{"/hooks/none": http.StatusNotFound, "/call/Peers": http.StatusNotFound, "/": http.StatusNotFound} {
		resp, err := http.Post("http://"+addr+path, "application/json", strings.NewReader("[]"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("POST %s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// The listener serves a user's device under /user/ beside the hooks and the
// peers: with the device's token /user/me says who it is and a call of the
// doc is answered, without it the path is 401, and a method that is not of
// the doc is not there even with it. A yard with no device has no /user/.
func TestUserDeviceOnTheListener(t *testing.T) {
	c := home(t)
	addr := freeAddr(t)
	setHookAddr(t, c, addr)
	st, err := store.Open(c.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "name", "south", "jo"); err != nil {
		t.Fatal(err)
	}
	d, err := st.AddDevice(ctx, "jo", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	st.Close(ctx)
	start(t, &runner{c: c})

	ask := func(addr, method, path, token string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, "http://"+addr+path, strings.NewReader("{}"))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(body))
	}
	if code, body := ask(addr, http.MethodGet, "/user/me", d.Token); code != http.StatusOK || body != `{"user":"jo","said_by":"phone","yard":"south","yardmaster":"yardmaster"}` {
		t.Errorf("GET /user/me with the token = %d %s", code, body)
	}
	if code, body := ask(addr, http.MethodPost, "/user/call/GetUser", d.Token); code != http.StatusOK || !strings.Contains(body, `"name":"jo"`) {
		t.Errorf("POST /user/call/GetUser with the token = %d %s", code, body)
	}
	if code, body := ask(addr, http.MethodPost, "/user/call/AddUser", d.Token); code != http.StatusOK || !strings.Contains(body, "no such method AddUser") {
		t.Errorf("POST /user/call/AddUser with the token = %d %s", code, body)
	}
	for _, path := range []string{"/user", "/user/me", "/user/call/GetUser", "/user/events"} {
		if code, _ := ask(addr, http.MethodGet, path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", path, code)
		}
	}
	// The socket's own paths are still not on the port.
	if code, _ := ask(addr, http.MethodPost, "/call/GetUser", d.Token); code != http.StatusNotFound {
		t.Errorf("POST /call/GetUser on the listener = %d, want 404", code)
	}

	none := home(t)
	bare := freeAddr(t)
	setHookAddr(t, none, bare)
	start(t, &runner{c: none})
	if code, _ := ask(bare, http.MethodGet, "/user/me", d.Token); code != http.StatusNotFound {
		t.Errorf("/user/me in a yard with no device = %d, want 404", code)
	}
}

// A device is enrolled on the listener of a running yard: the operator
// opens a pairing over the socket, and the code posted to /user/pair is
// answered with the token the device then calls with. The yard had no
// device, so /user/ was not there before, and the path is not there for a
// code once the pairing was used.
func TestUserDevicePairedOnTheListener(t *testing.T) {
	c := home(t)
	addr := freeAddr(t)
	setHookAddr(t, c, addr)
	st, err := store.Open(c.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	st.Close(ctx)
	start(t, &runner{c: c})
	cl := rpc.NewClient(c.Socket())
	defer cl.Close(ctx)

	post := func(path, token, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		answer, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(answer))
	}
	if code, _ := post(rpc.PairPath, "", `{"code":"K7QM-2XPD"}`); code != http.StatusNotFound {
		t.Fatalf("POST %s with no pairing open = %d, want 404", rpc.PairPath, code)
	}
	opened, err := cl.DevicePairOpen(ctx, "jo", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := post(rpc.PairPath, "", `{"code":"0000-0000"}`); code != http.StatusForbidden || body != `{"refused":"wrong_code"}` {
		t.Errorf("a wrong code = %d %s, want 403 wrong_code", code, body)
	}
	code, body := post(rpc.PairPath, "", `{"code":"`+opened.Code+`"}`)
	var got rpc.Paired
	if err := json.Unmarshal([]byte(body), &got); code != http.StatusOK || err != nil || got.User != "jo" || got.Device != "phone" {
		t.Fatalf("the code = %d %s (%v)", code, body, err)
	}
	if code, body := post("/user/call/GetUser", got.Token, `{}`); code != http.StatusOK || !strings.Contains(body, `"name":"jo"`) {
		t.Errorf("POST /user/call/GetUser with the token the device was given = %d %s", code, body)
	}
	if code, _ := post(rpc.PairPath, "", `{"code":"`+opened.Code+`"}`); code != http.StatusUnauthorized {
		t.Errorf("the code again = %d, want 401", code)
	}
	devices, err := cl.Devices(ctx, "jo")
	if err != nil || len(devices) != 1 || devices[0].Name != "phone" || devices[0].LastUsed == nil {
		t.Fatalf("devices = %+v, %v", devices, err)
	}
}
