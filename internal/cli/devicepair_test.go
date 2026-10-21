package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"rsc.io/qr"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// devicePairYard is userYard with a listener that serves the devices of its
// store, on an address of its own, as the yard's server says it listens.
func devicePairYard(t *testing.T) (st store.API, own *store.Store, addr string) {
	t.Helper()
	api, own := storetest.NewOwned(t)
	st = userYardOn(t, api)
	srv := httptest.NewServer(rpc.NewUserServer(own, func(context.Context) string { return "south" }, nil).Handler())
	t.Cleanup(srv.Close)
	said(t, &config.Config{Home: yardEnv(t, st)["YARDR_HOME"]}, hooks.Listener{Addr: strings.TrimPrefix(srv.URL, "http://"), From: hooks.FromSetting})
	return st, own, srv.URL
}

var devicePairShown = regexp.MustCompile(`(?m)^  address  (\S+)\n  code     ([0-9A-Z]{4}-[0-9A-Z]{4})$`)

// redeemCode calls the yard at addr with the code, as a device does.
func redeemCode(t *testing.T, addr, code string) (int, rpc.Paired) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	resp, err := http.Post(addr+rpc.PairPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got rpc.Paired
	answer, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(answer, &got)
	return resp.StatusCode, got
}

// yardr user device pair opens a pairing and prints the code, how long it
// holds and the address, and never a token: the device that calls the
// address with the code is given it, and is then a device as device add
// makes one. --end ends a pairing, and the code with it.
func TestDevicePairCommand(t *testing.T) {
	t.Parallel()
	st, own, addr := devicePairYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "ada")

	out := mustAs(t, st, "jo", "user", "device", "pair", "jo", "phone")
	m := devicePairShown.FindStringSubmatch(out)
	if m == nil || m[1] != addr {
		t.Fatalf("device pair shows no code at %s:\n%s", addr, out)
	}
	until := regexp.MustCompile(`^Pairing is open for 10 minutes, until (\d\d:\d\d:\d\d), for the device phone of jo\. Give the device this yard's address and the code:\n`).FindStringSubmatch(out)
	if until == nil {
		t.Fatalf("device pair does not say how long the code holds:\n%s", out)
	}
	wantText(t, out,
		"The code is good once: the device is answered with its token, which is shown nowhere else. 5 wrong codes end the pairing, and so does: ",
		" user device pair --end\n",
		"It then calls as jo until: ", " user device rm jo phone\n")
	if strings.Contains(out, "yardr-dev-") || strings.Contains(out, "█") || strings.Contains(out, "is not the operator") || strings.Contains(out, "plain http") {
		t.Fatalf("device pair, without --qr, for the operator, on this machine:\n%s", out)
	}
	if list := mustAs(t, st, "jo", "user", "device", "list"); !strings.HasPrefix(list, "no devices") {
		t.Fatalf("a device is listed before its code was used:\n%s", list)
	}
	refusedAs(t, st, "jo", "a pairing is open already, for the device phone of jo until "+until[1], "user", "device", "pair", "ada", "laptop")

	status, got := redeemCode(t, m[1], m[2])
	if status != http.StatusOK || !deviceToken.MatchString(got.Token) || got.User != "jo" || got.Device != "phone" {
		t.Fatalf("the code at the address = %d %+v", status, got)
	}
	if d, err := own.ResolveDevice(ctx, got.Token, "100.64.0.7"); err != nil || d == nil || d.User != "jo" || d.Name != "phone" {
		t.Fatalf("the token the device was given resolves to %+v, %v", d, err)
	}
	wantText(t, mustAs(t, st, "jo", "user", "device", "list"), "jo    phone   ", " from 100.64.0.7\n")
	for _, args := range [][]string{{"user", "device", "list", "--json"}, {"user", "show", "jo", "--json"}, {"events", "--json"}} {
		if out := mustAs(t, st, "jo", args...); strings.Contains(out, "yardr-dev-") || strings.Contains(out, m[2]) {
			t.Fatalf("yardr %s says something of the token or the code:\n%s", strings.Join(args, " "), out)
		}
	}

	// For a user who is not the operator, at an address of the operator's
	// giving, in JSON: what the QR code holds is there, and no picture.
	var opened devicePaired
	out = mustAs(t, st, "jo", "user", "device", "pair", "ada", "laptop", "--url", "https://mac.example.net/", "--qr", "--json")
	if err := json.Unmarshal([]byte(out), &opened); err != nil || opened.User != "ada" || opened.Device != "laptop" || opened.Pairing == "" ||
		opened.Address != "https://mac.example.net" || opened.QR != "https://mac.example.net#"+opened.Code ||
		time.Until(opened.Expires) > pairing.TTL || time.Until(opened.Expires) < pairing.TTL-time.Minute || strings.Contains(out, "█") {
		t.Fatalf("device pair --json: %+v, %v\n%s", opened, err, out)
	}
	if out := mustAs(t, st, "jo", "user", "device", "pair", "--end"); out != "the pairing for the device laptop of ada is ended: its code is good for nothing\n" {
		t.Fatalf("device pair --end:\n%s", out)
	}
	if status, got := redeemCode(t, addr, opened.Code); status != http.StatusUnauthorized || got.Token != "" {
		t.Fatalf("the code of an ended pairing = %d %+v", status, got)
	}
	refusedAs(t, st, "jo", "this yard has no device pairing open", "user", "device", "pair", "--end")
	out = mustAs(t, st, "jo", "user", "device", "pair", "ada", "laptop", "--url", "http://10.0.0.5:8790")
	wantText(t, out, "  address  http://10.0.0.5:8790\n",
		"ada is not the operator, and the device reads every bead in the yard, with its notes, and the mail of ada alone.\n",
		"http://10.0.0.5:8790 is plain http: whoever is between the device and this yard reads the code, and the token after it.")
	mustAs(t, st, "jo", "user", "device", "pair", "--end")

	refusedAs(t, st, "ada", "only the operator (jo) may add a device; this is ada", "user", "device", "pair", "ada", "watch")
	refusedAs(t, st, "ada", "only the operator (jo) may end a device's pairing; this is ada", "user", "device", "pair", "--end")
	refusedAs(t, st, "jo", "jo has a device phone already", "user", "device", "pair", "jo", "phone")
	refusedAs(t, st, "jo", "user carol: not found", "user", "device", "pair", "carol", "phone")
	refusedAs(t, st, "jo", "the address the device reaches this yard at", "user", "device", "pair", "ada", "watch", "--url", "mac.example.net")
	refusedAs(t, st, "jo", "the address the device reaches this yard at", "user", "device", "pair", "ada", "watch", "--url", "https://mac.example.net/#K7QM-2XPD")
	refusedAs(t, st, "jo", "give the user and the device's name, or --end", "user", "device", "pair", "ada")
	refusedAs(t, st, "jo", "--end ends the device pairing this yard has open: it takes nothing else", "user", "device", "pair", "ada", "watch", "--end")
	if own.DevicePairOpened(ctx) {
		t.Fatal("a refused command left a pairing open")
	}
}

// --qr draws a QR code under the code, and what it holds is the address,
// '#' and the code, and nothing else: read back module by module, the
// picture is the QR code of exactly that.
func TestDevicePairQR(t *testing.T) {
	t.Parallel()
	st, _, _ := devicePairYard(t)
	out := mustAs(t, st, "jo", "user", "device", "pair", "jo", "phone", "--url", "https://mac.example.net", "--qr")
	m := devicePairShown.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("device pair --qr shows no code:\n%s", out)
	}
	holds := m[1] + "#" + m[2]
	if holds != pairQR("https://mac.example.net", m[2]) {
		t.Fatalf("the QR code holds %q", holds)
	}
	want, err := qr.Encode(holds, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	// The picture's lines: the quiet zone around it is part of each, and of
	// their number.
	const quiet = 4
	var rows [][]rune
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "  "); ok && strings.Trim(rest, "█▀▄ ") == "" && len([]rune(rest)) == want.Size+2*quiet {
			rows = append(rows, []rune(rest))
		}
	}
	if len(rows) != (want.Size+2*quiet+1)/2 {
		t.Fatalf("%d lines of a picture for a code of %d modules:\n%s", len(rows), want.Size, out)
	}
	drawn := func(x, y int) bool {
		switch rows[(y+quiet)/2][x+quiet] {
		case '█':
			return true
		case '▀':
			return (y+quiet)%2 == 0
		case '▄':
			return (y+quiet)%2 == 1
		}
		return false
	}
	for y := -quiet; y < want.Size+quiet; y++ {
		for x := -quiet; x < want.Size+quiet; x++ {
			in := x >= 0 && y >= 0 && x < want.Size && y < want.Size
			if drawn(x, y) != (in && want.Black(x, y)) {
				t.Fatalf("the module at %d,%d is drawn %v:\n%s", x, y, drawn(x, y), out)
			}
		}
	}
	// On a terminal it is black on white, whatever the terminal's colours.
	coloured, err := drawQR(holds, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(coloured, "\n"), "\n") {
		if !strings.HasPrefix(line, "  \x1b[30;47m") || !strings.HasSuffix(line, "\x1b[0m") {
			t.Fatalf("a line of the coloured picture is %q", line)
		}
	}
}

// A yard that listens nowhere but whose link to the edge is up opens a
// pairing all the same: the default address is the edge's, and the device
// redeems the code down the link. With the link down, no pairing.
func TestDevicePairOverTheLinkWithNoListener(t *testing.T) {
	t.Parallel()
	api, own := storetest.NewOwned(t)
	st := userYardOn(t, api)
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(edge)
	t.Cleanup(srv.Close)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	cfg := config.At(yardEnv(t, st)["YARDR_HOME"])
	ctx := context.Background()

	sayLink(t, cfg, edgelink.State{URL: srv.URL, Handle: "mckean", Since: time.Now(), Reason: edgelink.DownPings, Ups: 1})
	refusedAs(t, st, "jo", "this yard has no listener, so no device can call it with a code: ", "user", "device", "pair", "jo", "phone")

	sayLink(t, cfg, edgelink.State{URL: srv.URL, Handle: "mckean", Up: true, Since: time.Now(), Ups: 1})
	yard, err := yardkey.Load(cfg.PeerKey())
	if err != nil {
		t.Fatal(err)
	}
	u := rpc.NewUserServer(own, func(context.Context) string { return "south" }, nil)
	l := &edgelink.Link{
		Edge: store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean"},
		Key:  yard, Handler: u.Handler(), HTTP: srv.Client(),
		Backoff: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	}
	lctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); l.Run(lctx) }()
	defer func() { cancel(); <-done }()

	out := mustAs(t, st, "jo", "user", "device", "pair", "jo", "phone")
	m := devicePairShown.FindStringSubmatch(out)
	if m == nil || m[1] != srv.URL {
		t.Fatalf("device pair over the link shows no code at %s:\n%s", srv.URL, out)
	}
	var link *standin.EdgeLink
	select {
	case link = <-edge.Links():
	case <-time.After(10 * time.Second):
		t.Fatal("the yard's link never came up at the edge")
	}
	body, _ := json.Marshal(map[string]string{"code": m[2]})
	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcancel()
	a, err := link.Deliver(dctx, link.Frame(http.MethodPost, rpc.PairPath, nil, body, false))
	if err != nil {
		t.Fatal(err)
	}
	var got rpc.Paired
	if err := json.Unmarshal(a.Body, &got); err != nil || a.Status != http.StatusOK || !deviceToken.MatchString(got.Token) {
		t.Fatalf("the code down the link = %d %s, %v", a.Status, a.Body, err)
	}
	if d, err := own.ResolveDevice(ctx, got.Token, "100.64.0.7"); err != nil || d == nil || d.User != "jo" || d.Name != "phone" {
		t.Fatalf("the token the device was given resolves to %+v, %v", d, err)
	}
}

// A device needs an address to call: a yard without a listener opens no
// pairing, and one that listens on every address says to give --url.
func TestDevicePairNeedsAnAddress(t *testing.T) {
	t.Parallel()
	st, own, _ := devicePairYard(t)
	cfg := &config.Config{Home: yardEnv(t, st)["YARDR_HOME"]}
	said(t, cfg, hooks.Listener{Addr: ":8790", From: hooks.FromSetting})
	refusedAs(t, st, "jo", "the listener's address (:8790) is none to give a device: say where the device reaches this yard with --url", "user", "device", "pair", "jo", "phone")
	said(t, cfg, hooks.Listener{})
	refusedAs(t, st, "jo", "this yard has no listener, so no device can call it with a code: ", "user", "device", "pair", "jo", "phone", "--url", "https://mac.example.net")
	if own.DevicePairOpened(context.Background()) {
		t.Fatal("a refused command left a pairing open")
	}
}
