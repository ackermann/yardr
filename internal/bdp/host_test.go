package bdp_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/bdp"
)

// The server answers a request whose Host header it serves and refuses any
// other with 421 and no body, before the method or the route is read: a page
// on another site cannot read the yard through a visitor's browser by
// pointing a name of its own at this machine (DNS rebinding). The Scope
// URL's host is a name it serves besides the loopback names.
func TestHostIsChecked(t *testing.T) {
	f := newFixture(t)
	const title = "a title nobody else may read"
	b := f.bead(title, "task")
	// No Host given: the client sends the test server's own address,
	// 127.0.0.1:<port>.
	if r := f.get("beads/" + b.ID); r.status != 200 || !strings.Contains(string(r.body), title) {
		t.Fatalf("Host %s: status %d, %s", f.ts.Listener.Addr(), r.status, r.body)
	}
	for _, host := range []string{f.ts.Listener.Addr().String(), "127.0.0.1", "127.8.8.8:1", "localhost:8765", "LOCALHOST", "[::1]:8765", "[::1]",
		"beads.test", "beads.test:8443", "BEADS.Test"} {
		if r := f.get("beads/"+b.ID, "Host", host); r.status != 200 {
			t.Errorf("Host %s: status %d, want 200", host, r.status)
		}
	}
	for _, host := range []string{"evil.example", "evil.example:8765", "beads.test.evil.example", "evilbeads.test", "sub.beads.test",
		"localhost.evil.example", "192.168.1.5:8765", "10.0.0.1", "[2001:db8::1]:8765"} {
		for _, method := range []string{"GET", "HEAD"} {
			r := f.do(method, "beads/"+b.ID, "Host", host)
			if r.status != http.StatusMisdirectedRequest || len(r.body) != 0 || r.header.Get("Content-Length") != "0" ||
				r.header.Get("Content-Type") != "" || r.header.Get("ETag") != "" {
				t.Errorf("%s Host %s: status %d, headers %v, body %q, want 421 and nothing else", method, host, r.status, r.header, r.body)
			}
		}
	}
	// The refusal comes before the method and the route, so it says
	// nothing of what is there.
	for _, c := range []struct{ method, target string }{{"POST", "bdp.json"}, {"GET", ""}, {"GET", "nothing/here"}, {"GET", "beads/?bogus=1"}} {
		if r := f.do(c.method, c.target, "Host", "evil.example"); r.status != http.StatusMisdirectedRequest || len(r.body) != 0 {
			t.Errorf("%s %s under another site's name: status %d, body %q, want 421 and no body", c.method, c.target, r.status, r.body)
		}
	}
	if r := f.do("POST", "bdp.json"); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST under the server's own name: status %d, want 405", r.status)
	}
}

// An absolute-form target names an authority of its own, which the Scope
// overrules (the request's authority is ignored) and which net/http reports in
// place of the Host header. A browser sends that form to a proxy only, so it
// is not what rebinding sends, and the same name in an origin-form request is
// refused.
func TestAbsoluteFormTargetNamesNoHost(t *testing.T) {
	f := newFixture(t)
	r := rawGet(t, f.ts, "GET http://evil.invalid/acme/bdp.json HTTP/1.1", "Host: evil.example")
	if r.status != 200 {
		t.Errorf("an absolute-form target: status %d, want 200 (BDP: the Scope decides identity)", r.status)
	}
	for _, host := range []string{"evil.invalid", "evil.example"} {
		if r := rawGet(t, f.ts, "GET /acme/bdp.json HTTP/1.1", "Host: "+host); r.status != http.StatusMisdirectedRequest {
			t.Errorf("an origin-form target under Host %s: status %d, want 421", host, r.status)
		}
	}
}

// The Scope URL's host is answered whatever it is: a name, with or without a
// port, or an IP address off loopback, which is no name to rebind.
func TestScopeHostIsAnswered(t *testing.T) {
	for _, c := range []struct {
		scope   string
		ok, not []string
	}{
		{"https://beads.example:8443/team/", []string{"beads.example", "beads.example:8443", "BEADS.EXAMPLE:1"}, []string{"beads.example.evil.example", "example", "other.example:8443"}},
		{"http://192.168.1.5:8765/yard/", []string{"192.168.1.5:8765", "192.168.1.5"}, []string{"192.168.1.6:8765", "10.0.0.1"}},
		{"http://[2001:db8::1]:8765/yard/", []string{"[2001:db8::1]:8765", "[2001:DB8::1]"}, []string{"[2001:db8::2]:8765"}},
	} {
		ts := hostServer(t, c.scope)
		for _, host := range append([]string{""}, c.ok...) {
			if got := statusUnder(t, ts, c.scope, host); got != 200 {
				t.Errorf("scope %s, Host %q: status %d, want 200", c.scope, host, got)
			}
		}
		for _, host := range c.not {
			if got := statusUnder(t, ts, c.scope, host); got != http.StatusMisdirectedRequest {
				t.Errorf("scope %s, Host %s: status %d, want 421", c.scope, host, got)
			}
		}
	}
}

// A request with no Host header at all (HTTP/1.0) names no host the server
// answers to, and a Scope with no host in it (http://:8080/yard/, which
// --addr :8080 --unsafe would default to) does not make an empty name its own.
func TestNoHostIsRefused(t *testing.T) {
	for _, scope := range []string{"https://beads.example/team/", "http://:8080/yard/"} {
		ts := hostServer(t, scope)
		u, err := url.Parse(scope)
		if err != nil {
			t.Fatal(err)
		}
		if r := rawGet(t, ts, "GET "+u.Path+"bdp.json HTTP/1.0"); r.status != http.StatusMisdirectedRequest {
			t.Errorf("scope %s, no Host: status %d, want 421", scope, r.status)
		}
	}
}

// --host without --unsafe: the server answers to the loopback names, the
// Scope's and the names given, in any case and with any port, and to nothing
// it was not given: not the machine's own host name, and no IP address off
// loopback.
func TestAllowHosts(t *testing.T) {
	const scope = "https://beads.example/team/"
	ts := hostServer(t, scope, func(s *bdp.Server) { s.AllowHosts("Proxy.Lan", "proxy.lan", "yard.tailnet-name.ts.net") })
	for _, host := range []string{"", "localhost:8765", "127.0.0.1:8765", "[::1]:8765", "beads.example", "BEADS.example:443",
		"proxy.lan", "PROXY.LAN:8765", "yard.tailnet-name.ts.net:8444"} {
		if got := statusUnder(t, ts, scope, host); got != 200 {
			t.Errorf("Host %q: status %d, want 200", host, got)
		}
	}
	refused := []string{"other.lan", "proxy.lan.evil.example", "x.proxy.lan:8765", "tailnet-name.ts.net", "192.168.1.5:8765", "10.0.0.1", "[2001:db8::1]:8765"}
	// The machine's own host name is one of the names not given: only
	// --unsafe adds it by itself.
	if hostname, _ := os.Hostname(); hostname != "" {
		own := strings.TrimSuffix(strings.ToLower(hostname), ".local")
		if own != "localhost" && own != "proxy.lan" && own != "beads.example" {
			refused = append(refused, own+":8765", own+".local")
		}
	}
	for _, host := range refused {
		if got := statusUnder(t, ts, scope, host); got != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: status %d, want 421", host, got)
		}
	}
}

// --unsafe: a request names the machine by any IP address or by its host
// name, with or without .local; another site's name is still refused.
func TestAllowNetwork(t *testing.T) {
	const scope = "https://beads.example/team/"
	ts := hostServer(t, scope, func(s *bdp.Server) { s.AllowNetwork("DevBox.local", "Yard.Example.net", "devbox") })
	for _, host := range []string{"", "192.168.178.90:8765", "192.168.178.90", "10.0.0.1:1", "[2001:db8::1]:8765", "[fe80::1]",
		"devbox", "devbox.local:8765", "DEVBOX.LOCAL", "yard.example.net:443", "localhost:8765", "beads.example"} {
		if got := statusUnder(t, ts, scope, host); got != 200 {
			t.Errorf("Host %q: status %d, want 200", host, got)
		}
	}
	for _, host := range []string{"evil.example", "evil.example:8765", "192.168.178.90.evil.example", "devbox.evil.example",
		"devbox.local.evil.example", "other.local", "local", ".local"} {
		if got := statusUnder(t, ts, scope, host); got != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: status %d, want 421", host, got)
		}
	}
	// A machine with no host name is reached by IP and by no name.
	bare := hostServer(t, scope, func(s *bdp.Server) { s.AllowNetwork("") })
	if got := statusUnder(t, bare, scope, "10.0.0.1"); got != 200 {
		t.Errorf("no host name, Host 10.0.0.1: status %d, want 200", got)
	}
	for _, host := range []string{".local", "evil.example"} {
		if got := statusUnder(t, bare, scope, host); got != http.StatusMisdirectedRequest {
			t.Errorf("no host name, Host %s: status %d, want 421", host, got)
		}
	}
}

// hostServer serves a Scope with no yard behind it: bdp.json, which the Host
// tests ask for, needs none. allow gets the server before it serves.
func hostServer(t *testing.T, scope string, allow ...func(*bdp.Server)) *httptest.Server {
	t.Helper()
	srv, err := bdp.NewServer(scope, nil, func(context.Context) (*bdp.Graph, error) { return nil, errors.New("no yard") }, bdp.DefaultLimits, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range allow {
		f(srv)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// statusUnder is the status of the Scope's bdp.json asked for under the Host
// header host; "" leaves the client's own, the server's address.
func statusUnder(t *testing.T, ts *httptest.Server, scope, host string) int {
	t.Helper()
	u, err := url.Parse(scope)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", ts.URL+u.Path+"bdp.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	return r.StatusCode
}

// rawGet sends the request line and the header lines as they are given and
// reads the answer, for what the client would not send: a Host that is no
// part of the line, or none.
func rawGet(t *testing.T, ts *httptest.Server, line string, headers ...string) resp {
	t.Helper()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s\r\n%sConnection: close\r\n\r\n", line, strings.Join(append(headers, ""), "\r\n"))
	r, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, b}
}
